package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/worker/writeback"
	"google.golang.org/grpc/codes"
)

// promoteChart promotes the shared fixture's chart artifact ("demo-achart")
// to env and returns its promotion_id -- the one promotion kind
// RetryArgoSync ever operates against, since shouldEnqueueWriteback
// (promotion.go) gates ArgoCD sync tracking to CHART promotions.
func promoteChart(t *testing.T, f *promotionFixture, env, key string) string {
	t.Helper()
	resp, err := f.promo.Promote(authedCtx(), promoteReq(env, "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, key))
	if err != nil {
		t.Fatalf("promote chart: %v", err)
	}
	return resp.Promotion.PromotionId
}

// TestRetryArgoSync_Authorization mirrors TestSetAppStatus_Authorization's
// three-way shape (authz_test.go): RetryArgoSync requires the flat
// auth.RoleAdmin role (FR10) -- builder/promoter roles, which every other
// write RPC on this service accepts, must not be enough (FR11), and an
// unauthenticated caller is rejected the same way. Uses the fixture's own
// nil-temporal PromotionServer (f.promo) since the auth gate runs before
// any Temporal access -- see RetryArgoSync's doc comment.
func TestRetryArgoSync_Authorization(t *testing.T) {
	f := newPromotionFixture(t)
	promotionID := promoteChart(t, f, "dev", "retry-authz")
	req := &pb.RetryArgoSyncRequest{PromotionId: promotionID}

	t.Run("correct role allowed", func(t *testing.T) {
		if _, err := f.promo.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), req); err != nil {
			t.Fatalf("expected admin to be allowed, got %v", err)
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		// A promoter-dev principal -- holds real power elsewhere on this
		// service (Promote/Rollback), but not admin.
		_, err := f.promo.RetryArgoSync(ctxWithRoles(auth.RolePromoterDev), req)
		requireCode(t, err, codes.PermissionDenied, "RetryArgoSync")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		_, err := f.promo.RetryArgoSync(context.Background(), req)
		requireCode(t, err, codes.Unauthenticated, "RetryArgoSync")
	})
}

// TestRetryArgoSync_MissingPromotionID_InvalidArgument mirrors
// TestGetPromotionDetails_MissingPromotionID_InvalidArgument's pattern for
// this RPC's own request-shape validation.
func TestRetryArgoSync_MissingPromotionID_InvalidArgument(t *testing.T) {
	f := newPromotionFixture(t)
	_, err := f.promo.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{})
	requireCode(t, err, codes.InvalidArgument, "RetryArgoSync")
}

// TestRetryArgoSync_UnknownPromotionID_NotFound proves mapRepoErr
// translates repository.ErrNotFound (via GetDetails) to codes.NotFound.
func TestRetryArgoSync_UnknownPromotionID_NotFound(t *testing.T) {
	f := newPromotionFixture(t)
	_, err := f.promo.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{PromotionId: "nonexistent"})
	requireCode(t, err, codes.NotFound, "RetryArgoSync")
}

// TestRetryArgoSync_NonChartPromotion_FailedPrecondition proves FR13's own
// kind of precondition failure: an IMAGE promotion never went through
// ArgoCD sync tracking in the first place (shouldEnqueueWriteback gates
// WritebackWorkflow to CHART promotions only), so retrying it is rejected
// rather than resolving a zero-value ChartID.
func TestRetryArgoSync_NonChartPromotion_FailedPrecondition(t *testing.T) {
	f := newPromotionFixture(t)
	promoted, err := f.promo.Promote(authedCtx(), promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "retry-nonchart"))
	if err != nil {
		t.Fatalf("promote image: %v", err)
	}

	_, err = f.promo.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{PromotionId: promoted.Promotion.PromotionId})
	requireCode(t, err, codes.FailedPrecondition, "RetryArgoSync (non-chart promotion)")
}

// newMockTemporalPromotionServer builds a PromotionServer sharing f's
// repository but wired to a mocks.Client instead of f.promo's nil one, so
// tests in this file can assert on the ExecuteWorkflow call itself -- same
// pattern worker/outbox/drain_test.go uses for Drainer.Temporal.
func newMockTemporalPromotionServer(f *promotionFixture) (*PromotionServer, *mocks.Client) {
	temporalClient := &mocks.Client{}
	return NewPromotionServer(f.repo, temporalClient), temporalClient
}

// TestRetryArgoSync_Success_StartsWorkflowWithCorrectInput proves FR12: a
// successful call starts writeback.RetryArgoSyncWorkflow on
// writeback.TaskQueue, with an ArgoSyncInput carrying the SAME
// (Domain, ApplicationName) the original WritebackWorkflow would have
// resolved for this exact promotion (chart.Domain, chart.FullName()+"-"+
// EnvironmentKey -- "demo"/"demo-achart-dev" for the fixture's chart) and
// IsRetry set, and the response's workflow id has the "retry-<promotion_id>-"
// prefix RetryArgoSyncWorkflowID documents.
func TestRetryArgoSync_Success_StartsWorkflowWithCorrectInput(t *testing.T) {
	f := newPromotionFixture(t)
	promotionID := promoteChart(t, f, "dev", "retry-success")
	srv, temporalClient := newMockTemporalPromotionServer(f)

	wantArgoIn := writeback.ArgoSyncInput{
		PromotionID: promotionID, Domain: "demo", ApplicationName: "demo-achart-dev", IsRetry: true,
	}
	run := &mocks.WorkflowRun{}
	temporalClient.On("ExecuteWorkflow", mock.Anything, mock.MatchedBy(func(opts client.StartWorkflowOptions) bool {
		return strings.HasPrefix(opts.ID, "retry-"+promotionID+"-") && opts.TaskQueue == writeback.TaskQueue
	}), mock.Anything, wantArgoIn).Return(run, nil)

	resp, err := srv.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{PromotionId: promotionID})
	if err != nil {
		t.Fatalf("RetryArgoSync: %v", err)
	}
	if !strings.HasPrefix(resp.TemporalWorkflowId, "retry-"+promotionID+"-") {
		t.Errorf("TemporalWorkflowId = %q, want prefix %q", resp.TemporalWorkflowId, "retry-"+promotionID+"-")
	}
	temporalClient.AssertExpectations(t)
}

// TestRetryArgoSync_WorkflowAlreadyStarted_FailedPrecondition proves
// serviceerror.WorkflowExecutionAlreadyStarted from ExecuteWorkflow is
// translated into codes.FailedPrecondition -- mirroring TriggerRelease's
// identical translation (release.go).
func TestRetryArgoSync_WorkflowAlreadyStarted_FailedPrecondition(t *testing.T) {
	f := newPromotionFixture(t)
	promotionID := promoteChart(t, f, "dev", "retry-already-started")
	srv, temporalClient := newMockTemporalPromotionServer(f)

	temporalClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{})

	_, err := srv.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{PromotionId: promotionID})
	requireCode(t, err, codes.FailedPrecondition, "RetryArgoSync (already started)")
}

// TestRetryArgoSync_TemporalError_Internal proves any other ExecuteWorkflow
// error (e.g. Temporal frontend unreachable) is translated into
// codes.Internal -- mirroring TriggerRelease's default branch.
func TestRetryArgoSync_TemporalError_Internal(t *testing.T) {
	f := newPromotionFixture(t)
	promotionID := promoteChart(t, f, "dev", "retry-temporal-error")
	srv, temporalClient := newMockTemporalPromotionServer(f)

	temporalClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, context.DeadlineExceeded)

	_, err := srv.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{PromotionId: promotionID})
	requireCode(t, err, codes.Internal, "RetryArgoSync (temporal error)")
}

// TestRetryArgoSync_RepeatedRetries_DistinctWorkflowIDs proves FR12's "an
// admin must be able to click retry more than once for the same promotion"
// -- two successive calls for the same promotion_id both start (distinct
// workflow ids), neither colliding with WorkflowExecutionAlreadyStarted.
func TestRetryArgoSync_RepeatedRetries_DistinctWorkflowIDs(t *testing.T) {
	f := newPromotionFixture(t)
	promotionID := promoteChart(t, f, "dev", "retry-repeated")
	srv, temporalClient := newMockTemporalPromotionServer(f)

	var seenIDs []string
	temporalClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(&mocks.WorkflowRun{}, nil).
		Run(func(args mock.Arguments) {
			opts := args.Get(1).(client.StartWorkflowOptions)
			seenIDs = append(seenIDs, opts.ID)
		})

	first, err := srv.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{PromotionId: promotionID})
	if err != nil {
		t.Fatalf("first RetryArgoSync: %v", err)
	}
	time.Sleep(time.Microsecond) // guarantees a distinct UnixNano() suffix regardless of clock resolution
	second, err := srv.RetryArgoSync(ctxWithRoles(auth.RoleAdmin), &pb.RetryArgoSyncRequest{PromotionId: promotionID})
	if err != nil {
		t.Fatalf("second RetryArgoSync: %v", err)
	}

	if first.TemporalWorkflowId == second.TemporalWorkflowId {
		t.Fatalf("expected distinct workflow ids across repeated retries, got %q twice", first.TemporalWorkflowId)
	}
	if len(seenIDs) != 2 || seenIDs[0] == seenIDs[1] {
		t.Fatalf("expected 2 distinct ExecuteWorkflow calls, got %v", seenIDs)
	}
}
