package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestWritebackFake_RecordResult_PersistsLocationAndCommitSHA is FR7a's
// (issue #1029) unit coverage for writebackFake.RecordResult, mirroring
// postgres's own RecordResult integration test
// (postgres_integration_promotion_test.go's
// TestWritebackOutbox_RecordResult_PersistsLocationAndCommitSHA): it must
// persist location/commit_sha onto the outbox row matching promotion_id,
// keyed by promotion_id (not outbox_id -- see RecordResult's doc comment),
// and must NOT disturb status/completed_at, the distinction from MarkDone
// (which fires when the workflow merely STARTS, not when Publish
// COMPLETES) that this method exists to preserve.
func TestWritebackFake_RecordResult_PersistsLocationAndCommitSHA(t *testing.T) {
	r := New()
	ctx := context.Background()
	promotion := r.SeedPromotion(repository.Promotion{EnvironmentID: "env-1", TargetKey: "image:acme-widget"})

	outbox, err := r.Writeback().Enqueue(ctx, repository.WritebackOutbox{
		PromotionID: promotion.PromotionID, EnvironmentID: "env-1", EnvironmentKey: "dev", Domain: "acme",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if outbox.Location != "" || outbox.CommitSHA != "" {
		t.Fatalf("expected a freshly enqueued outbox row to have empty location/commit_sha, got %+v", outbox)
	}

	if err := r.Writeback().MarkDone(ctx, outbox.OutboxID, promotion.PromotionID, "run-1"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	before, err := r.Writeback().Get(ctx, outbox.OutboxID)
	if err != nil {
		t.Fatalf("get before record result: %v", err)
	}

	wantLocation := "acme/acme-widget/versions/dev.yaml"
	wantCommitSHA := "0123456789abcdef0123456789abcdef01234567"
	if err := r.Writeback().RecordResult(ctx, outbox.OutboxID, promotion.PromotionID, wantLocation, wantCommitSHA); err != nil {
		t.Fatalf("record result: %v", err)
	}

	got, err := r.Writeback().Get(ctx, outbox.OutboxID)
	if err != nil {
		t.Fatalf("get after record result: %v", err)
	}
	if got.Location != wantLocation {
		t.Fatalf("expected location %q, got %q", wantLocation, got.Location)
	}
	if got.CommitSHA != wantCommitSHA {
		t.Fatalf("expected commit_sha %q, got %q", wantCommitSHA, got.CommitSHA)
	}
	// RecordResult must not disturb status/completed_at -- a distinct write
	// from MarkDone, see that method's and RecordResult's own doc comments.
	if got.Status != before.Status {
		t.Fatalf("expected status to stay %q after RecordResult, got %q", before.Status, got.Status)
	}
	if !got.CompletedAt.Equal(*before.CompletedAt) {
		t.Fatalf("expected completed_at to stay %v after RecordResult, got %v", *before.CompletedAt, *got.CompletedAt)
	}
}

// TestWritebackFake_RecordResult_UnknownPromotionReturnsNotFound proves
// writebackFake.RecordResult surfaces repository.ErrNotFound (rather than
// silently no-op-ing) when no outbox row's promotion_id matches -- mirrors
// postgres's own RecordResult error contract.
func TestWritebackFake_RecordResult_UnknownPromotionReturnsNotFound(t *testing.T) {
	r := New()
	ctx := context.Background()

	err := r.Writeback().RecordResult(ctx, "", "unknown-promotion", "some/path.yaml", "deadbeef")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound for an unknown promotion_id, got %v", err)
	}
}
