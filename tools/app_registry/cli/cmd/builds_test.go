package cmd

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// TestIncompleteArtifacts_FiltersOutPublished proves the client-side filter
// backing `builds status --incomplete` (AR-7d, issue #558): only artifacts
// NOT in state PUBLISHED survive, and their relative order is preserved.
func TestIncompleteArtifacts_FiltersOutPublished(t *testing.T) {
	in := []*pb.Artifact{
		{ArtifactId: "a-published", State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
		{ArtifactId: "b-publishing", State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHING},
		{ArtifactId: "c-failed", State: pb.ArtifactState_ARTIFACT_STATE_FAILED},
		{ArtifactId: "d-allocated", State: pb.ArtifactState_ARTIFACT_STATE_ALLOCATED},
	}

	out := incompleteArtifacts(in)

	if len(out) != 3 {
		t.Fatalf("expected 3 incomplete artifacts, got %d", len(out))
	}
	want := []string{"b-publishing", "c-failed", "d-allocated"}
	for i, id := range want {
		if out[i].ArtifactId != id {
			t.Errorf("out[%d] = %q, want %q", i, out[i].ArtifactId, id)
		}
	}
}

// TestIncompleteArtifacts_AllPublishedYieldsEmpty proves a fully-completed
// run reports zero incomplete artifacts, not nil-vs-empty ambiguity that
// would print differently.
func TestIncompleteArtifacts_AllPublishedYieldsEmpty(t *testing.T) {
	in := []*pb.Artifact{
		{ArtifactId: "a", State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
		{ArtifactId: "b", State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
	}

	out := incompleteArtifacts(in)

	if len(out) != 0 {
		t.Fatalf("expected 0 incomplete artifacts, got %d: %+v", len(out), out)
	}
}

// TestNewBuildsStatusCmd_RegistersIncompleteAndAttemptFlags locks in the
// flag surface `builds status` exposes -- a future accidental rename would
// break release.yml/OPERATIONS.md's documented invocation silently
// otherwise.
func TestNewBuildsStatusCmd_RegistersIncompleteAndAttemptFlags(t *testing.T) {
	c := newBuildsStatusCmd()

	incomplete := c.Flags().Lookup("incomplete")
	if incomplete == nil {
		t.Fatal("expected a --incomplete flag")
	}
	if incomplete.DefValue != "false" {
		t.Errorf("--incomplete default = %q, want false", incomplete.DefValue)
	}

	attempt := c.Flags().Lookup("attempt")
	if attempt == nil {
		t.Fatal("expected an --attempt flag")
	}
	if attempt.DefValue != "0" {
		t.Errorf("--attempt default = %q, want 0 (latest)", attempt.DefValue)
	}

	if c.Use != "status <workflow-run-id>" {
		t.Errorf("Use = %q, want %q", c.Use, "status <workflow-run-id>")
	}
}
