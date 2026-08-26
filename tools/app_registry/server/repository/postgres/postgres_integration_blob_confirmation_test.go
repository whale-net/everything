//go:build integration

// Integration tests for blob confirmation state verification during artifact publish (FR-46).
package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestCompletePublish_BlobConfirmationState_FR46 proves that completePublish
// verifies every blob has confirmation_state == 'confirmed' before transitioning
// to published (FR-46). Tests both success (all confirmed) and failure (any unconfirmed).
func TestCompletePublish_BlobConfirmationState_FR46(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	appID := seedApp(t, pool, "acme", "myapp", "binary")
	buildID := seedBuild(t, pool, "run-123")

	// Create a blob_record in unconfirmed state
	blobID := uuid.NewString()
	digest := "sha256:unconfirmed-blob"
	if _, err := pool.Exec(ctx, `
		INSERT INTO blob_record (blob_id, uncompressed_content_digest, stored_encoding, content_type, confirmation_state)
		VALUES ($1, $2, 'gzip', 'application/octet-stream', 'unconfirmed')`,
		blobID, digest); err != nil {
		t.Fatalf("create unconfirmed blob_record: %v", err)
	}

	// Begin publish (creates artifact in "publishing" state)
	artifact := repository.Artifact{
		Kind:       repository.ArtifactKindBinary,
		AppID:      appID,
		Repository: "releases.acme.com/myapp",
		Version:    "v1.0.0",
		BuildID:    buildID,
	}
	publishing, err := reg.Artifacts().BeginPublish(ctx, artifact.Kind, appID, artifact.Version, buildID, artifact.Repository, repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}
	if publishing.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state=publishing, got %v", publishing.State)
	}

	// Link the unconfirmed blob to the artifact
	if err := reg.BlobVersions().CreateBlobVersion(ctx, &repository.BlobVersion{
		BlobID:     blobID,
		ArtifactID: publishing.ArtifactID,
	}); err != nil {
		t.Fatalf("link blob to artifact: %v", err)
	}

	// Attempt to record/complete publish with unconfirmed blob
	recordReq := repository.Artifact{
		Kind:       publishing.Kind,
		AppID:      appID,
		Repository: artifact.Repository,
		Version:    artifact.Version,
		Digest:     "sha256:artifact-digest",
	}
	_, _, err = reg.Artifacts().RecordArtifact(ctx, recordReq, nil, repository.DomainAdoptionStageObserve)
	if err == nil {
		t.Fatalf("expected RecordArtifact to fail with unconfirmed blob, got nil error")
	}
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unconfirmed blobs") {
		t.Fatalf("expected error mentioning 'unconfirmed blobs', got: %v", err)
	}

	// Verify artifact is still in publishing state (not transitioned)
	after, err := reg.Artifacts().GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: publishing.ArtifactID})
	if err != nil {
		t.Fatalf("GetArtifact after failed RecordArtifact: %v", err)
	}
	if after.State != repository.ArtifactStatePublishing {
		t.Fatalf("expected state to remain publishing, got %v", after.State)
	}

	// Now update the blob to confirmed
	if _, err := pool.Exec(ctx, `
		UPDATE blob_record SET confirmation_state = 'confirmed', confirmation_changed_at = NOW()
		WHERE blob_id = $1`, blobID); err != nil {
		t.Fatalf("update blob to confirmed: %v", err)
	}

	// Now publish should succeed
	_, _, err = reg.Artifacts().RecordArtifact(ctx, recordReq, nil, repository.DomainAdoptionStageObserve)
	if err != nil {
		t.Fatalf("RecordArtifact with confirmed blob failed: %v", err)
	}

	// Verify artifact is now published
	published, err := reg.Artifacts().GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: publishing.ArtifactID})
	if err != nil {
		t.Fatalf("GetArtifact after successful RecordArtifact: %v", err)
	}
	if published.State != repository.ArtifactStatePublished {
		t.Fatalf("expected state=published, got %v", published.State)
	}
	if published.Digest != recordReq.Digest {
		t.Fatalf("expected digest=%s, got %s", recordReq.Digest, published.Digest)
	}
}

// TestCompletePublish_NoBlobs_FR46 proves that when an artifact has no blobs linked,
// it can publish successfully (no unconfirmed blobs to block it).
func TestCompletePublish_NoBlobs_FR46(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	appID := seedApp(t, pool, "acme", "noblob", "binary")
	buildID := seedBuild(t, pool, "run-noblob")

	// Begin publish (creates artifact in "publishing" state)
	artifact := repository.Artifact{
		Kind:       repository.ArtifactKindBinary,
		AppID:      appID,
		Repository: "releases.acme.com/noblob",
		Version:    "v1.0.0",
		BuildID:    buildID,
	}
	publishing, err := reg.Artifacts().BeginPublish(ctx, artifact.Kind, appID, artifact.Version, buildID, artifact.Repository, repository.VersionSourceTag)
	if err != nil {
		t.Fatalf("BeginPublish: %v", err)
	}

	// Record/complete publish WITHOUT linking any blobs
	recordReq := repository.Artifact{
		Kind:       publishing.Kind,
		AppID:      appID,
		Repository: artifact.Repository,
		Version:    artifact.Version,
		Digest:     "sha256:artifact-no-blobs",
	}
	result, _, err := reg.Artifacts().RecordArtifact(ctx, recordReq, nil, repository.DomainAdoptionStageObserve)
	if err != nil {
		t.Fatalf("RecordArtifact with no blobs failed: %v", err)
	}

	// Verify artifact is now published
	if result.State != repository.ArtifactStatePublished {
		t.Fatalf("expected state=published, got %v", result.State)
	}
}
