package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository"
	apiworkshop "github.com/whale-net/everything/manmanv2/api/workshop"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// fakeWorkshopCacheRepo is a minimal in-memory WorkshopCacheRepository fake for
// WorkshopCacheStatusConsumer tests -- no Postgres, no broker. Embedding the nil
// interface means any method this consumer never calls panics loudly instead of
// silently returning a zero value, following fakePendingRestartRepo's pattern in
// session_restart_consumer_test.go.
type fakeWorkshopCacheRepo struct {
	repository.WorkshopCacheRepository

	mu sync.Mutex

	// entriesByKey mimics UpsertCacheEntry's real idempotent insert-or-return-existing
	// behavior on cache_key (NFR4): a second Upsert for the same key returns the same
	// row (with the same CacheEntryID) rather than creating a duplicate.
	entriesByKey map[string]*manman.WorkshopCacheEntry
	nextEntryID  int64

	upsertCalls int

	touchCalls    []touchCacheEntryCall
	presenceCalls []presenceCall
}

type touchCacheEntryCall struct {
	cacheEntryID int64
	verifiedAt   time.Time
}

type presenceCall struct {
	cacheEntryID int64
	serverID     int64
}

func newFakeWorkshopCacheRepo() *fakeWorkshopCacheRepo {
	return &fakeWorkshopCacheRepo{entriesByKey: map[string]*manman.WorkshopCacheEntry{}}
}

func (f *fakeWorkshopCacheRepo) UpsertCacheEntry(ctx context.Context, entry *manman.WorkshopCacheEntry) (*manman.WorkshopCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCalls++

	if existing, ok := f.entriesByKey[entry.CacheKey]; ok {
		if entry.SizeBytes != nil {
			existing.SizeBytes = entry.SizeBytes
		}
		return existing, nil
	}

	f.nextEntryID++
	saved := *entry
	saved.CacheEntryID = f.nextEntryID
	f.entriesByKey[entry.CacheKey] = &saved
	return &saved, nil
}

func (f *fakeWorkshopCacheRepo) TouchCacheEntryVerified(ctx context.Context, cacheEntryID int64, verifiedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touchCalls = append(f.touchCalls, touchCacheEntryCall{cacheEntryID: cacheEntryID, verifiedAt: verifiedAt})
	return nil
}

func (f *fakeWorkshopCacheRepo) UpsertHostPresence(ctx context.Context, cacheEntryID, serverID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presenceCalls = append(f.presenceCalls, presenceCall{cacheEntryID: cacheEntryID, serverID: serverID})
	return nil
}

func (f *fakeWorkshopCacheRepo) entryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entriesByKey)
}

func newTestWorkshopCacheStatusConsumer(repo *fakeWorkshopCacheRepo) *WorkshopCacheStatusConsumer {
	return &WorkshopCacheStatusConsumer{
		cacheRepo: repo,
		logger:    slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
	}
}

func mustMarshalCacheStatusUpdate(t *testing.T, update hostrmq.WorkshopCacheStatusUpdate) []byte {
	t.Helper()
	body, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("failed to marshal WorkshopCacheStatusUpdate: %v", err)
	}
	return body
}

// TestHandleStatusUpdate_VerifiedUnchanged_TouchesCacheEntry covers the
// verified_unchanged event: it must call TouchCacheEntryVerified with the given
// cache_entry_id and verified_at, and must not create or touch host presence.
func TestHandleStatusUpdate_VerifiedUnchanged_TouchesCacheEntry(t *testing.T) {
	repo := newFakeWorkshopCacheRepo()
	h := newTestWorkshopCacheStatusConsumer(repo)

	verifiedAt := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	body := mustMarshalCacheStatusUpdate(t, hostrmq.WorkshopCacheStatusUpdate{
		ServerID:     1,
		WorkshopID:   "987654321",
		CacheEntryID: 42,
		Event:        "verified_unchanged",
		VerifiedAt:   verifiedAt,
	})

	err := h.handleStatusUpdate(context.Background(), rmq.Message{RoutingKey: "status.host.1.workshop.cache", Body: body})
	if err != nil {
		t.Fatalf("handleStatusUpdate returned error: %v", err)
	}

	if len(repo.touchCalls) != 1 {
		t.Fatalf("expected exactly 1 TouchCacheEntryVerified call, got %d", len(repo.touchCalls))
	}
	if repo.touchCalls[0].cacheEntryID != 42 {
		t.Errorf("expected cache_entry_id 42, got %d", repo.touchCalls[0].cacheEntryID)
	}
	if !repo.touchCalls[0].verifiedAt.Equal(verifiedAt) {
		t.Errorf("expected verified_at %v, got %v", verifiedAt, repo.touchCalls[0].verifiedAt)
	}
	if len(repo.presenceCalls) != 0 {
		t.Errorf("verified_unchanged must not record host presence, got %d calls", len(repo.presenceCalls))
	}
}

// TestHandleStatusUpdate_Refreshed_UpsertsEntryAndPresence covers the refreshed event
// (FR9's write side): it must upsert the cache entry (keyed on cache_key) and then
// record this host's presence against the resulting entry id.
func TestHandleStatusUpdate_Refreshed_UpsertsEntryAndPresence(t *testing.T) {
	repo := newFakeWorkshopCacheRepo()
	h := newTestWorkshopCacheStatusConsumer(repo)

	body := mustMarshalCacheStatusUpdate(t, hostrmq.WorkshopCacheStatusUpdate{
		ServerID:       3,
		WorkshopID:     "987654321",
		ContentVersion: "17000099",
		Event:          "refreshed",
		SizeBytes:      1024,
		VerifiedAt:     time.Now(),
	})

	err := h.handleStatusUpdate(context.Background(), rmq.Message{RoutingKey: "status.host.3.workshop.cache", Body: body})
	if err != nil {
		t.Fatalf("handleStatusUpdate returned error: %v", err)
	}

	expectedKey := apiworkshop.CacheKey("987654321", "17000099")
	entry, ok := repo.entriesByKey[expectedKey]
	if !ok {
		t.Fatalf("expected a cache entry to be upserted under key %q", expectedKey)
	}
	if entry.WorkshopID != "987654321" || entry.ContentVersion != "17000099" {
		t.Errorf("unexpected entry identity: %+v", entry)
	}

	if len(repo.presenceCalls) != 1 {
		t.Fatalf("expected exactly 1 UpsertHostPresence call, got %d", len(repo.presenceCalls))
	}
	if repo.presenceCalls[0].cacheEntryID != entry.CacheEntryID || repo.presenceCalls[0].serverID != 3 {
		t.Errorf("unexpected presence call: %+v (expected cache_entry_id=%d server_id=3)", repo.presenceCalls[0], entry.CacheEntryID)
	}
}

// TestHandleStatusUpdate_Populated_UpsertsEntryAndPresence covers the populated event
// (first-ever install), which must be handled identically to refreshed.
func TestHandleStatusUpdate_Populated_UpsertsEntryAndPresence(t *testing.T) {
	repo := newFakeWorkshopCacheRepo()
	h := newTestWorkshopCacheStatusConsumer(repo)

	body := mustMarshalCacheStatusUpdate(t, hostrmq.WorkshopCacheStatusUpdate{
		ServerID:       5,
		WorkshopID:     "111222333",
		ContentVersion: "17000000",
		Event:          "populated",
		SizeBytes:      2048,
	})

	err := h.handleStatusUpdate(context.Background(), rmq.Message{RoutingKey: "status.host.5.workshop.cache", Body: body})
	if err != nil {
		t.Fatalf("handleStatusUpdate returned error: %v", err)
	}

	if repo.entryCount() != 1 {
		t.Fatalf("expected exactly 1 cache entry, got %d", repo.entryCount())
	}
	if len(repo.presenceCalls) != 1 || repo.presenceCalls[0].serverID != 5 {
		t.Errorf("expected exactly 1 presence call for server 5, got %+v", repo.presenceCalls)
	}
}

// TestHandleStatusUpdate_Present_OnlyRecordsPresence covers the present event: it must
// record host presence against the given cache_entry_id without upserting any entry.
func TestHandleStatusUpdate_Present_OnlyRecordsPresence(t *testing.T) {
	repo := newFakeWorkshopCacheRepo()
	h := newTestWorkshopCacheStatusConsumer(repo)

	body := mustMarshalCacheStatusUpdate(t, hostrmq.WorkshopCacheStatusUpdate{
		ServerID:     9,
		WorkshopID:   "987654321",
		CacheEntryID: 17,
		Event:        "present",
	})

	err := h.handleStatusUpdate(context.Background(), rmq.Message{RoutingKey: "status.host.9.workshop.cache", Body: body})
	if err != nil {
		t.Fatalf("handleStatusUpdate returned error: %v", err)
	}

	if repo.upsertCalls != 0 {
		t.Errorf("present must never upsert a cache entry, got %d upsert calls", repo.upsertCalls)
	}
	if len(repo.presenceCalls) != 1 || repo.presenceCalls[0].cacheEntryID != 17 || repo.presenceCalls[0].serverID != 9 {
		t.Errorf("unexpected presence calls: %+v", repo.presenceCalls)
	}
}

// TestHandleStatusUpdate_DuplicateRefreshed_IsIdempotent is the idempotency guarantee
// the issue's Testing section calls for directly: "a duplicate refreshed for the same
// key is idempotent" -- two refreshed events for the same (workshop_id,
// content_version) must converge on a single cache entry (NFR4), with presence recorded
// each time (redelivery-safe) rather than erroring or duplicating the row.
func TestHandleStatusUpdate_DuplicateRefreshed_IsIdempotent(t *testing.T) {
	repo := newFakeWorkshopCacheRepo()
	h := newTestWorkshopCacheStatusConsumer(repo)

	update := hostrmq.WorkshopCacheStatusUpdate{
		ServerID:       11,
		WorkshopID:     "987654321",
		ContentVersion: "17000099",
		Event:          "refreshed",
		SizeBytes:      1024,
	}
	body := mustMarshalCacheStatusUpdate(t, update)

	if err := h.handleStatusUpdate(context.Background(), rmq.Message{RoutingKey: "status.host.11.workshop.cache", Body: body}); err != nil {
		t.Fatalf("first handleStatusUpdate returned error: %v", err)
	}
	if err := h.handleStatusUpdate(context.Background(), rmq.Message{RoutingKey: "status.host.11.workshop.cache", Body: body}); err != nil {
		t.Fatalf("duplicate handleStatusUpdate returned error: %v", err)
	}

	if repo.entryCount() != 1 {
		t.Fatalf("expected exactly 1 cache entry after a duplicate refreshed delivery, got %d", repo.entryCount())
	}
	if repo.upsertCalls != 2 {
		t.Errorf("expected UpsertCacheEntry to be called twice (idempotently), got %d", repo.upsertCalls)
	}
	// Presence is recorded on each delivery -- redelivery-safe, per UpsertHostPresence's
	// own idempotent semantics on the real repository (an upsert on (cache_entry_id,
	// server_id), not an insert).
	if len(repo.presenceCalls) != 2 {
		t.Fatalf("expected 2 presence calls (one per delivery), got %d", len(repo.presenceCalls))
	}
	// The decisive idempotency check: both deliveries must resolve to the exact same
	// cache_entry_id, not just "some" single row existing under the key (entryCount()
	// alone can't distinguish an idempotent upsert from two upserts that happen to
	// collide on the same map key by coincidence).
	if repo.presenceCalls[0].cacheEntryID != repo.presenceCalls[1].cacheEntryID {
		t.Errorf("expected both deliveries to resolve to the same cache_entry_id, got %d and %d",
			repo.presenceCalls[0].cacheEntryID, repo.presenceCalls[1].cacheEntryID)
	}
}

// TestHandleStatusUpdate_UnknownEvent_DroppedNotErrored proves an unrecognized event
// name is logged and dropped (acked) rather than treated as a processing error that
// would requeue-loop forever.
func TestHandleStatusUpdate_UnknownEvent_DroppedNotErrored(t *testing.T) {
	repo := newFakeWorkshopCacheRepo()
	h := newTestWorkshopCacheStatusConsumer(repo)

	body := mustMarshalCacheStatusUpdate(t, hostrmq.WorkshopCacheStatusUpdate{
		ServerID:   1,
		WorkshopID: "987654321",
		Event:      "some_future_event",
	})

	err := h.handleStatusUpdate(context.Background(), rmq.Message{RoutingKey: "status.host.1.workshop.cache", Body: body})
	if err != nil {
		t.Fatalf("expected an unknown event to be dropped without error, got: %v", err)
	}
	if repo.upsertCalls != 0 || len(repo.touchCalls) != 0 || len(repo.presenceCalls) != 0 {
		t.Error("an unknown event must not touch the repository at all")
	}
}

// TestHandleStatusUpdate_MalformedBody_DroppedNotErrored proves a malformed message
// body is logged and dropped (acked) rather than causing a requeue loop.
func TestHandleStatusUpdate_MalformedBody_DroppedNotErrored(t *testing.T) {
	repo := newFakeWorkshopCacheRepo()
	h := newTestWorkshopCacheStatusConsumer(repo)

	err := h.handleStatusUpdate(context.Background(), rmq.Message{
		RoutingKey: "status.host.1.workshop.cache",
		Body:       []byte("not valid json"),
	})
	if err != nil {
		t.Fatalf("expected a malformed body to be dropped without error, got: %v", err)
	}
	if repo.upsertCalls != 0 || len(repo.touchCalls) != 0 || len(repo.presenceCalls) != 0 {
		t.Error("a malformed body must not touch the repository at all")
	}
}
