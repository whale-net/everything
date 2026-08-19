package writeback

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/protobuf/encoding/protojson"
)

// StubActivities is AR-4b's stub Writeback implementation: it renders
// environment state by calling the AppRegistry API (never Postgres
// directly -- see ARCHITECTURE.md "The API is the write path; git is the
// delivery path") and publishes nowhere except a local directory. No
// gitops commit, no S3 put -- both explicitly out of scope for AR-4b, see
// PLAN.md.
//
// State survives a worker restart via a plain sidecar file
// (<env>.json.state_hash next to <env>.json), not an in-memory map, so
// no-op detection keeps working across restarts without a second
// dependency.
type StubActivities struct {
	// Client reads current promotion state. Any credential with reader
	// access works -- GetEnvironmentState only requires
	// auth.RequireAuthenticated, see ARCHITECTURE.md "Authorization".
	Client pb.PromotionRegistryClient
	// OutDir is the local directory rendered documents are written to.
	// Created if missing.
	OutDir string
}

// NewStubActivities constructs a StubActivities. outDir is created lazily
// by Publish, not here, so RenderEnvironmentState-only use (e.g. tests)
// never touches disk.
func NewStubActivities(client pb.PromotionRegistryClient, outDir string) *StubActivities {
	return &StubActivities{Client: client, OutDir: outDir}
}

// RenderEnvironmentState implements Writeback. See that interface's doc
// comment.
func (a *StubActivities) RenderEnvironmentState(ctx context.Context, in WritebackInput) (RenderedState, error) {
	resp, err := a.Client.GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{EnvironmentKey: in.EnvironmentKey})
	if err != nil {
		return RenderedState{}, fmt.Errorf("render environment state for %s (promotion %s): %w", in.EnvironmentKey, in.PromotionID, err)
	}
	doc, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(resp)
	if err != nil {
		return RenderedState{}, fmt.Errorf("marshal rendered state for %s: %w", in.EnvironmentKey, err)
	}
	return RenderedState{
		EnvironmentKey: in.EnvironmentKey,
		Domain:         in.Domain,
		StateHash:      resp.StateHash,
		RenderedAt:     time.Now().UTC(),
		Document:       doc,
	}, nil
}

// Publish implements Writeback. See that interface's doc comment for the
// no-op detection contract.
func (a *StubActivities) Publish(ctx context.Context, state RenderedState) (PublishResult, error) {
	if err := os.MkdirAll(a.OutDir, 0o755); err != nil {
		return PublishResult{}, fmt.Errorf("create writeback output dir %s: %w", a.OutDir, err)
	}
	docPath := filepath.Join(a.OutDir, state.EnvironmentKey+".json")
	hashPath := docPath + ".state_hash"

	if last, err := os.ReadFile(hashPath); err == nil && string(last) == state.StateHash {
		// No-op detection: the last thing published for this environment
		// already matches, so writing again would be a byte-identical,
		// pointless commit -- see ARCHITECTURE.md "Activities are
		// individually retryable and idempotent."
		return PublishResult{Location: docPath, Skipped: true}, nil
	}

	if err := os.WriteFile(docPath, state.Document, 0o644); err != nil {
		return PublishResult{}, fmt.Errorf("write rendered state to %s: %w", docPath, err)
	}
	if err := os.WriteFile(hashPath, []byte(state.StateHash), 0o644); err != nil {
		return PublishResult{}, fmt.Errorf("write state hash to %s: %w", hashPath, err)
	}
	return PublishResult{Location: docPath}, nil
}
