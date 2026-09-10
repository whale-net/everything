package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
)

// TestNotifyBuild_SkipOnEmptyReleaseRunID pins the fallback-dispatch
// contract: a run dispatched WITHOUT a release_run_id (the manual/bot
// fallback path release-v2.yml still supports) must skip the RPC entirely
// and exit 0 -- the notify job must never fail a run that has nothing to
// notify. The fake client records zero calls.
func TestNotifyBuild_SkipOnEmptyReleaseRunID(t *testing.T) {
	fake := NewFakeReleaseRegistryClient()
	withReleaseRegistryClient(fake, func() {
		err := ExecuteNotifyBuild(NotifyBuildParams{
			Ctx:    context.Background(),
			Status: "success",
		})
		if err != nil {
			t.Fatalf("expected nil error for empty release-run-id, got: %v", err)
		}
		if len(fake.NotifyBuildCompleteCalls) != 0 {
			t.Fatalf("expected no RPC calls for empty release-run-id, got %d", len(fake.NotifyBuildCompleteCalls))
		}
	})
}

// TestNotifyBuild_StatusMapping pins the succeeded bit per GitHub Actions'
// own job-result vocabulary (needs.<job>.result forwarded verbatim):
// success/skipped -> true, failure/cancelled -> false, anything else ->
// error.
func TestNotifyBuild_StatusMapping(t *testing.T) {
	tests := []struct {
		status    string
		succeeded bool
		wantErr   bool
	}{
		{status: "success", succeeded: true},
		{status: "skipped", succeeded: true},
		{status: "failure", succeeded: false},
		{status: "cancelled", succeeded: false},
		{status: "in_progress", wantErr: true},
		{status: "", wantErr: true},
	}
	for _, tt := range tests {
		fake := NewFakeReleaseRegistryClient()
		withReleaseRegistryClient(fake, func() {
			err := ExecuteNotifyBuild(NotifyBuildParams{
				Ctx:          context.Background(),
				ReleaseRunID: "run-1",
				GitHubRunID:  410,
				Status:       tt.status,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("status %q: expected error, got nil", tt.status)
				}
				if len(fake.NotifyBuildCompleteCalls) != 0 {
					t.Fatalf("status %q: unexpected RPC before validation", tt.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("status %q: unexpected error: %v", tt.status, err)
			}
			if len(fake.NotifyBuildCompleteCalls) != 1 {
				t.Fatalf("status %q: expected 1 RPC call, got %d", tt.status, len(fake.NotifyBuildCompleteCalls))
			}
			got := fake.NotifyBuildCompleteCalls[0]
			if got.Succeeded != tt.succeeded {
				t.Fatalf("status %q: expected succeeded=%v, got %v", tt.status, tt.succeeded, got.Succeeded)
			}
		})
	}
}

// TestNotifyBuild_RequestFields pins the RPC payload: release_run_id and
// github_run_id threaded through, and the detail line composed as
// "run <id> conclusion=<status>" with the optional --detail appended in
// parens.
func TestNotifyBuild_RequestFields(t *testing.T) {
	tests := []struct {
		name       string
		runID      int64
		status     string
		detail     string
		wantDetail string
	}{
		{
			name:       "plain",
			runID:      410,
			status:     "success",
			wantDetail: "run 410 conclusion=success",
		},
		{
			name:       "with extra detail",
			runID:      410,
			status:     "failure",
			detail:     "plan-release=success build-release-artifacts=failure",
			wantDetail: "run 410 conclusion=failure (plan-release=success build-release-artifacts=failure)",
		},
		{
			name:       "zero run id passed through",
			runID:      0,
			status:     "cancelled",
			wantDetail: "run 0 conclusion=cancelled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := NewFakeReleaseRegistryClient()
			withReleaseRegistryClient(fake, func() {
				err := ExecuteNotifyBuild(NotifyBuildParams{
					Ctx:          context.Background(),
					ReleaseRunID: "run-1",
					GitHubRunID:  tt.runID,
					Status:       tt.status,
					Detail:       tt.detail,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(fake.NotifyBuildCompleteCalls) != 1 {
					t.Fatalf("expected 1 RPC call, got %d", len(fake.NotifyBuildCompleteCalls))
				}
				got := fake.NotifyBuildCompleteCalls[0]
				if got.ReleaseRunId != "run-1" {
					t.Fatalf("expected release_run_id 'run-1', got %q", got.ReleaseRunId)
				}
				if got.GithubRunId != tt.runID {
					t.Fatalf("expected github_run_id %d, got %d", tt.runID, got.GithubRunId)
				}
				if got.Detail != tt.wantDetail {
					t.Fatalf("expected detail %q, got %q", tt.wantDetail, got.Detail)
				}
			})
		})
	}
}

// TestNotifyBuild_SignaledFalseIsNotAnError pins the Signaled=false
// contract: the workflow execution is unknown to Temporal (already
// completed, stale id) and PollBuild's fallback owns the outcome, so the
// CLI must report success, not an error the notify job would surface.
func TestNotifyBuild_SignaledFalseIsNotAnError(t *testing.T) {
	fake := NewFakeReleaseRegistryClient()
	fake.NotifyBuildCompleteFn = func(ctx context.Context, in *pb.NotifyBuildCompleteRequest, opts ...grpc.CallOption) (*pb.NotifyBuildCompleteResponse, error) {
		return &pb.NotifyBuildCompleteResponse{Signaled: false}, nil
	}
	withReleaseRegistryClient(fake, func() {
		err := ExecuteNotifyBuild(NotifyBuildParams{
			Ctx:          context.Background(),
			ReleaseRunID: "run-1",
			GitHubRunID:  42,
			Status:       "success",
		})
		if err != nil {
			t.Fatalf("expected nil error for Signaled=false, got: %v", err)
		}
		if len(fake.NotifyBuildCompleteCalls) != 1 {
			t.Fatalf("expected 1 RPC call, got %d", len(fake.NotifyBuildCompleteCalls))
		}
	})
}

// TestNotifyBuild_RPCErrorPropagates pins that a real RPC failure (auth
// rejection, network) surfaces as an error -- the notify job's
// continue-on-error protects the release, but the human still sees why.
func TestNotifyBuild_RPCErrorPropagates(t *testing.T) {
	fake := NewFakeReleaseRegistryClient()
	fake.NotifyBuildCompleteFn = func(ctx context.Context, in *pb.NotifyBuildCompleteRequest, opts ...grpc.CallOption) (*pb.NotifyBuildCompleteResponse, error) {
		return nil, fmt.Errorf("permission denied")
	}
	withReleaseRegistryClient(fake, func() {
		err := ExecuteNotifyBuild(NotifyBuildParams{
			Ctx:          context.Background(),
			ReleaseRunID: "run-1",
			GitHubRunID:  42,
			Status:       "success",
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("expected RPC error to propagate, got: %v", err)
		}
	})
}

// TestFakeReleaseRegistryClient pins the fake's default behavior: no Fn
// set means Signaled=true and the call is recorded.
func TestFakeReleaseRegistryClient(t *testing.T) {
	fake := NewFakeReleaseRegistryClient()
	resp, err := fake.NotifyBuildComplete(context.Background(), &pb.NotifyBuildCompleteRequest{ReleaseRunId: "r1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Signaled {
		t.Fatal("expected default Signaled=true")
	}
	if len(fake.NotifyBuildCompleteCalls) != 1 || fake.NotifyBuildCompleteCalls[0].ReleaseRunId != "r1" {
		t.Fatalf("expected recorded call, got %+v", fake.NotifyBuildCompleteCalls)
	}
}
