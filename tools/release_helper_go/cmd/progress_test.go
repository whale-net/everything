package cmd

import (
	"context"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
)

// TestTargetProgressReporter_EmptyReleaseRunID_NoOp pins ExecuteNotifyBuild's
// identical skip: no Temporal release run behind this dispatch (manual/bot
// fallback) means the returned callback must be a true no-op, never
// dialing or calling the client.
func TestTargetProgressReporter_EmptyReleaseRunID_NoOp(t *testing.T) {
	client := NewFakeReleaseRegistryClient()
	report := targetProgressReporter(context.Background(), "", 42, client, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "demo-widget")
	report("built")
	report("pushed")
	if len(client.ReportTargetProgressCalls) != 0 {
		t.Fatalf("expected zero calls with an empty release run id, got %d", len(client.ReportTargetProgressCalls))
	}
}

// TestTargetProgressReporter_ReportsBuiltAndPushed pins the happy path:
// each state maps to its own pb.ReleaseRunTargetState, with owner_full_name
// and kind passed through verbatim -- the reporter is kind-agnostic, so the
// same closure serves an image target and a chart target.
func TestTargetProgressReporter_ReportsBuiltAndPushed(t *testing.T) {
	client := NewFakeReleaseRegistryClient()
	report := targetProgressReporter(context.Background(), "run-1", 99, client, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "demo-widget")

	report("built")
	report("pushed")

	if len(client.ReportTargetProgressCalls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(client.ReportTargetProgressCalls))
	}
	built := client.ReportTargetProgressCalls[0]
	if built.ReleaseRunId != "run-1" || built.GithubRunId != 99 || built.OwnerFullName != "demo-widget" ||
		built.Kind != pb.ArtifactKind_ARTIFACT_KIND_IMAGE || built.State != pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILT {
		t.Fatalf("unexpected built request: %+v", built)
	}
	pushed := client.ReportTargetProgressCalls[1]
	if pushed.State != pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_PUSHED {
		t.Fatalf("unexpected pushed request: %+v", pushed)
	}
}

// TestTargetProgressReporter_ReportsBuilding pins the per-target
// start-of-build report: "building" maps to BUILDING and is sent with the
// caller's kind, which is what lets only the in-flight target show as
// BUILDING instead of the whole batch.
func TestTargetProgressReporter_ReportsBuilding(t *testing.T) {
	client := NewFakeReleaseRegistryClient()
	report := targetProgressReporter(context.Background(), "run-1", 99, client, pb.ArtifactKind_ARTIFACT_KIND_CHART, "app-registry")

	report("building")

	if len(client.ReportTargetProgressCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(client.ReportTargetProgressCalls))
	}
	got := client.ReportTargetProgressCalls[0]
	if got.State != pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING {
		t.Fatalf("expected BUILDING, got %s", got.State)
	}
	if got.Kind != pb.ArtifactKind_ARTIFACT_KIND_CHART || got.OwnerFullName != "app-registry" {
		t.Fatalf("expected chart kind and verbatim owner name, got %+v", got)
	}
}

// TestTargetProgressReporter_UnknownState_SkipsReport pins the defensive
// default: an OnProgress call with any state other than "built"/"pushed"
// must not reach the RPC at all.
func TestTargetProgressReporter_UnknownState_SkipsReport(t *testing.T) {
	client := NewFakeReleaseRegistryClient()
	report := targetProgressReporter(context.Background(), "run-1", 99, client, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "demo-widget")
	report("queued")
	if len(client.ReportTargetProgressCalls) != 0 {
		t.Fatalf("expected zero calls for an unknown state, got %d", len(client.ReportTargetProgressCalls))
	}
}

// TestTargetProgressReporter_RPCErrorSwallowed pins the best-effort
// contract: a failing RPC must not panic or be surfaced to the caller --
// BuildAppParams.OnProgress has no error return for exactly this reason.
func TestTargetProgressReporter_RPCErrorSwallowed(t *testing.T) {
	client := NewFakeReleaseRegistryClient()
	client.ReportTargetProgressFn = func(ctx context.Context, in *pb.ReportTargetProgressRequest, opts ...grpc.CallOption) (*pb.ReportTargetProgressResponse, error) {
		return nil, context.DeadlineExceeded
	}
	report := targetProgressReporter(context.Background(), "run-1", 99, client, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "demo-widget")
	report("built") // must not panic
}
