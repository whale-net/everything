package fake

import (
	"context"
	"fmt"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// GetDetails mirrors postgres's promotionRepo.GetDetails (FR7-FR9, issue
// #1031) -- see repository.PromotionDetails' doc comment for the shape
// this assembles from f.r.state, and that method's doc comment for why
// every sub-lookup after the initial promotion lookup tolerates "not
// found" as a legitimate state rather than an error.
func (f promotionFake) GetDetails(ctx context.Context, promotionID string) (*repository.PromotionDetails, error) {
	promotion, ok := f.r.state.Promotions[promotionID]
	if !ok {
		return nil, fmt.Errorf("%w: promotion %s", repository.ErrNotFound, promotionID)
	}

	// request_event: the earliest promotion_event row for this
	// promotion_id -- see postgres's GetDetails doc comment for why this is
	// "the creating event".
	var requestEvent repository.PromotionEvent
	var haveRequestEvent bool
	for _, e := range f.r.state.PromotionEvents {
		if e.PromotionID != promotionID {
			continue
		}
		if !haveRequestEvent || e.OccurredAt.Before(requestEvent.OccurredAt) {
			requestEvent = e
			haveRequestEvent = true
		}
	}

	// from_version: the version live for this same (environment_id,
	// target_key) immediately before THIS promotion's own valid_from -- see
	// postgres's GetDetails doc comment for why this differs from
	// GetPrevious.
	var fromVersion string
	var haveFromVersion bool
	var latestPriorValidFrom time.Time
	for _, p := range f.r.state.Promotions {
		if p.EnvironmentID != promotion.EnvironmentID || p.TargetKey != promotion.TargetKey {
			continue
		}
		if !p.ValidFrom.Before(promotion.ValidFrom) {
			continue
		}
		if !haveFromVersion || p.ValidFrom.After(latestPriorValidFrom) {
			fromVersion = f.r.state.Artifacts[p.ArtifactID].Version
			latestPriorValidFrom = p.ValidFrom
			haveFromVersion = true
		}
	}

	// writeback_location/writeback_commit_sha: the most recently created
	// writeback_outbox row for this promotion_id, "" if none exists yet.
	var location, commitSHA string
	var haveOutbox bool
	var latestOutboxCreatedAt time.Time
	for _, o := range f.r.state.WritebackOutbox {
		if o.PromotionID != promotionID {
			continue
		}
		if !haveOutbox || o.CreatedAt.After(latestOutboxCreatedAt) {
			location = o.Location
			commitSHA = o.CommitSHA
			latestOutboxCreatedAt = o.CreatedAt
			haveOutbox = true
		}
	}

	syncEvents, err := f.ListSyncEvents(ctx, promotionID)
	if err != nil {
		return nil, fmt.Errorf("get promotion details for %s: list sync events: %w", promotionID, err)
	}
	outcome, currentSyncStatus, currentHealthStatus := repository.DerivePromotionSyncOutcome(syncEvents)

	return &repository.PromotionDetails{
		Promotion:           promotion,
		RequestEvent:        requestEvent,
		FromVersion:         fromVersion,
		ToVersion:           promotion.Version,
		WritebackLocation:   location,
		WritebackCommitSHA:  commitSHA,
		SyncEvents:          syncEvents,
		CurrentSyncStatus:   currentSyncStatus,
		CurrentHealthStatus: currentHealthStatus,
		Outcome:             outcome,
	}, nil
}
