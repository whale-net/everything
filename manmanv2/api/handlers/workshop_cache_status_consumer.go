package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	"github.com/whale-net/everything/manmanv2/models"
)

// workshopCacheStatusConsumerQueue is the dedicated queue name for this consumer's
// status.host.*.workshop.cache binding (#2184, plan #2175 FR8/FR9/FR10). A wildcard
// binding key with a real "*" segment (the host's server_id) requires this to be its own
// queue, following the same dedicated-queue pattern as SessionRestartConsumer and
// WorkshopStatusHandler -- sharing a queue with either would make them compete for
// messages instead of both seeing every one.
const workshopCacheStatusConsumerQueue = "control-api.workshop.cache.status"

// Workshop cache status event names, mirrored from hostrmq.WorkshopCacheStatusUpdate's doc
// comment and manmanv2/host/workshop/orchestrator.go's publishing side.
const (
	workshopCacheEventVerifiedUnchanged = "verified_unchanged"
	workshopCacheEventRefreshed         = "refreshed"
	workshopCacheEventPopulated         = "populated"
	workshopCacheEventPresent           = "present"
)

// WorkshopCacheStatusConsumer consumes WorkshopCacheStatusUpdate messages published by
// host-manager's install-time verify/cache-refresh flow (#2184) on the new
// "status.host.<serverID>.workshop.cache" routing key -- additive alongside (never
// replacing) WorkshopStatusHandler's existing "status.workshop.installation.#"
// consumption, per NFR3.
//
// It binds its own dedicated queue directly to the "manman" exchange, exactly like
// SessionRestartConsumer and WorkshopStatusHandler.
type WorkshopCacheStatusConsumer struct {
	cacheRepo repository.WorkshopCacheRepository
	consumer  *rmq.Consumer
	logger    *slog.Logger
}

// NewWorkshopCacheStatusConsumer creates the consumer, declares its dedicated queue, and
// binds it to status.host.*.workshop.cache on the "manman" exchange. It does not start
// consuming -- call Start(ctx) for that.
//
// The bound routing key uses a real AMQP "*" wildcard (one word, the host's server_id),
// which the broker itself evaluates correctly on QueueBind; the local handler is
// registered under "#" (matches unconditionally) rather than repeating the wildcard
// pattern, since libs/go/rmq's in-process handler dispatch (matchesRoutingKey) only
// supports exact matches and trailing "#" prefixes, not "*" -- and this queue's single
// handler only ever sees messages the broker has already filtered to this binding.
func NewWorkshopCacheStatusConsumer(
	cacheRepo repository.WorkshopCacheRepository,
	rmqConn *rmq.Connection,
	logger *slog.Logger,
) (*WorkshopCacheStatusConsumer, error) {
	consumer, err := rmq.NewConsumerWithOpts(rmqConn, workshopCacheStatusConsumerQueue, false, false, 0, 0)
	if err != nil {
		return nil, err
	}

	if err := consumer.BindExchange("manman", []string{"status.host.*.workshop.cache"}); err != nil {
		consumer.Close()
		return nil, err
	}

	h := &WorkshopCacheStatusConsumer{
		cacheRepo: cacheRepo,
		consumer:  consumer,
		logger:    logger,
	}

	consumer.RegisterHandler("#", h.handleStatusUpdate)

	return h, nil
}

// Start starts consuming workshop cache status messages. It blocks until ctx is cancelled
// or the underlying consumer returns an error.
func (h *WorkshopCacheStatusConsumer) Start(ctx context.Context) error {
	return h.consumer.Start(ctx)
}

// Close closes the consumer's channel.
func (h *WorkshopCacheStatusConsumer) Close() error {
	return h.consumer.Close()
}

// handleStatusUpdate processes a status.host.*.workshop.cache message per the event table
// in issue #2184:
//   - verified_unchanged (or any other verify event) -> TouchCacheEntryVerified
//   - refreshed / populated -> UpsertCacheEntry (idempotent on cache_key), then
//     UpsertHostPresence
//   - present -> UpsertHostPresence only
//
// UpsertCacheEntry's idempotent insert-or-return-existing on cache_key (NFR4) makes a
// duplicate refreshed/populated delivery for the same key a no-op beyond re-touching
// size_bytes, so redelivery is safe without any additional dedup here.
func (h *WorkshopCacheStatusConsumer) handleStatusUpdate(ctx context.Context, msg rmq.Message) error {
	var update hostrmq.WorkshopCacheStatusUpdate
	if err := json.Unmarshal(msg.Body, &update); err != nil {
		// Malformed payload is a permanent error: log and drop rather than
		// requeue-loop it. Returning nil acks the message.
		h.logger.Warn("failed to unmarshal workshop cache status update", "error", err)
		return nil
	}

	switch update.Event {
	case workshopCacheEventVerifiedUnchanged:
		if update.CacheEntryID == 0 {
			h.logger.Warn("workshop cache verify event missing cache_entry_id, dropping",
				"workshop_id", update.WorkshopID, "event", update.Event)
			return nil
		}
		verifiedAt := update.VerifiedAt
		if verifiedAt.IsZero() {
			verifiedAt = time.Now()
		}
		if err := h.cacheRepo.TouchCacheEntryVerified(ctx, update.CacheEntryID, verifiedAt); err != nil {
			h.logger.Warn("failed to record workshop cache verify", "cache_entry_id", update.CacheEntryID, "error", err)
			return err
		}
		return nil

	case workshopCacheEventRefreshed, workshopCacheEventPopulated:
		cacheKey := workshop.CacheKey(update.WorkshopID, update.ContentVersion)
		entry := &manman.WorkshopCacheEntry{
			WorkshopID:     update.WorkshopID,
			ContentVersion: update.ContentVersion,
			CacheKey:       cacheKey,
			S3Key:          workshop.S3Key(cacheKey),
		}
		if update.SizeBytes > 0 {
			size := update.SizeBytes
			entry.SizeBytes = &size
		}

		saved, err := h.cacheRepo.UpsertCacheEntry(ctx, entry)
		if err != nil {
			h.logger.Warn("failed to upsert workshop cache entry", "cache_key", cacheKey, "error", err)
			return err
		}

		if err := h.cacheRepo.UpsertHostPresence(ctx, saved.CacheEntryID, update.ServerID); err != nil {
			h.logger.Warn("failed to record workshop cache host presence",
				"cache_entry_id", saved.CacheEntryID, "server_id", update.ServerID, "error", err)
			return err
		}

		h.logger.Info("recorded workshop cache write",
			"cache_entry_id", saved.CacheEntryID, "server_id", update.ServerID, "event", update.Event)
		return nil

	case workshopCacheEventPresent:
		if update.CacheEntryID == 0 {
			h.logger.Warn("workshop cache present event missing cache_entry_id, dropping", "workshop_id", update.WorkshopID)
			return nil
		}
		if err := h.cacheRepo.UpsertHostPresence(ctx, update.CacheEntryID, update.ServerID); err != nil {
			h.logger.Warn("failed to record workshop cache host presence",
				"cache_entry_id", update.CacheEntryID, "server_id", update.ServerID, "error", err)
			return err
		}
		return nil

	default:
		h.logger.Warn("unknown workshop cache status event, dropping", "event", update.Event, "workshop_id", update.WorkshopID)
		return nil
	}
}
