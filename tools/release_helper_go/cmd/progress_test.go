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
	report := targetProgressReporter(context.Background(), "", 42, client, "demo", "widget")
	report("built")
	report("pushed")
	if len(client.ReportTargetProgressCalls) != 0 {
		t.Fatalf("expected zero calls with an empty release run id, got %d", len(client.ReportTargetProgressCalls))
	}
}

// TestTargetProgressReporter_ReportsBuiltAndPushed pins the happy path:
// each state maps to its own pb.ReleaseRunTargetState, with owner_full_name
// derived as "<domain>-<app>" and Kind always ARTIFACT_KIND_IMAGE (this
// reporter is image-only).
func TestTargetProgressReporter_ReportsBuiltAndPushed(t *testing.T) {
	client := NewFakeReleaseRegistryClient()
	report := targetProgressReporter(context.Background(), "run-1", 99, client, "demo", "widget")

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

// TestTargetProgressReporter_UnknownState_SkipsReport pins the defensive
// default: an OnProgress call with any state other than "built"/"pushed"
// must not reach the RPC at all.
func TestTargetProgressReporter_UnknownState_SkipsReport(t *testing.T) {
	client := NewFakeReleaseRegistryClient()
	report := targetProgressReporter(context.Background(), "run-1", 99, client, "demo", "widget")
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
	report := targetProgressReporter(context.Background(), "run-1", 99, client, "demo", "widget")
	report("built") // must not panic
}
