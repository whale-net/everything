package cmd

import (
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/apierrors"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// statusWithReason builds a gRPC status error carrying an errdetails.ErrorInfo
// with the given reason, mirroring what server/handlers/errors.go's
// mapRepoErr attaches for the owner-not-reconciled case (issue #547).
func statusWithReason(t *testing.T, code codes.Code, reason string) error {
	t.Helper()
	st := status.New(code, "some message")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason,
		Domain: apierrors.ErrorDomain,
	})
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	return withDetails.Err()
}

// TestExitCodeFor_OwnerNotReconciled locks in the exit code
// .github/actions/app-registry-record-image/action.yml and release.yml's
// inline chart-recording loop branch on to print an actionable "app isn't
// registered yet" annotation instead of a generic registry-error one.
// Changing exitOwnerNotReconciled's value without updating those two call
// sites would silently break that branch -- this test is what should catch
// a regression on the Go side; the shell side has no equivalent, which is
// exactly why exitOwnerNotReconciled's doc comment calls that out.
func TestExitCodeFor_OwnerNotReconciled(t *testing.T) {
	err := statusWithReason(t, codes.InvalidArgument, apierrors.ReasonOwnerNotReconciled)
	if got := exitCodeFor(err); got != exitOwnerNotReconciled {
		t.Errorf("exitCodeFor() = %d, want %d (exitOwnerNotReconciled)", got, exitOwnerNotReconciled)
	}
}

// TestExitCodeFor_OtherReasonFallsBackToOne covers the "registry error"
// branch: a gRPC status with a *different* (or absent) ErrorInfo reason must
// not be misclassified as owner-not-reconciled.
func TestExitCodeFor_OtherReasonFallsBackToOne(t *testing.T) {
	err := statusWithReason(t, codes.InvalidArgument, "SOME_OTHER_REASON")
	if got := exitCodeFor(err); got != 1 {
		t.Errorf("exitCodeFor() = %d, want 1", got)
	}
}

// TestExitCodeFor_PlainGRPCErrorFallsBackToOne covers the common case: most
// gRPC failures (Unauthenticated, Unavailable, a plain InvalidArgument with
// no details) carry no ErrorInfo at all.
func TestExitCodeFor_PlainGRPCErrorFallsBackToOne(t *testing.T) {
	err := status.Error(codes.Unavailable, "registry unreachable")
	if got := exitCodeFor(err); got != 1 {
		t.Errorf("exitCodeFor() = %d, want 1", got)
	}
}

// TestExitCodeFor_NonGRPCErrorFallsBackToOne covers a failure that never
// reached the server at all (e.g. a dial error or flag validation) -- these
// aren't gRPC statuses and must not panic isOwnerNotReconciled's type
// assertion path.
func TestExitCodeFor_NonGRPCErrorFallsBackToOne(t *testing.T) {
	err := errors.New("some local failure, e.g. failed to dial")
	if got := exitCodeFor(err); got != 1 {
		t.Errorf("exitCodeFor() = %d, want 1", got)
	}
}

// TestExitCodeFor_Unimplemented locks in the exit code every
// app-registry-*/action.yml composite action and release.yml's inline chart
// loops branch on to distinguish version skew (issue #570) from a plain
// outage. Changing exitVersionSkew's value without updating those call
// sites would silently break that branch, same risk exitOwnerNotReconciled's
// doc comment already calls out for issue #547.
func TestExitCodeFor_Unimplemented(t *testing.T) {
	err := status.Error(codes.Unimplemented, "unknown method BeginPublishBatch for service appregistry.v1.ArtifactRegistry")
	if got := exitCodeFor(err); got != exitVersionSkew {
		t.Errorf("exitCodeFor() = %d, want %d (exitVersionSkew)", got, exitVersionSkew)
	}
}

// TestExitCodeFor_UnimplementedOutranksOwnerNotReconciled is a belt-and-
// suspenders check: the two carved-out reasons are mutually exclusive in
// practice (Unimplemented means the server never got far enough to attach
// an ErrorInfo detail), but isOwnerNotReconciled is checked first in
// exitCodeFor, so this pins the actual precedence rather than leaving it
// implicit in the code's dependency line above.
func TestExitCodeFor_UnimplementedOutranksOwnerNotReconciled(t *testing.T) {
	err := status.Error(codes.Unimplemented, "unknown method")
	if isOwnerNotReconciled(err) {
		t.Fatalf("isOwnerNotReconciled() = true for a plain Unimplemented status, want false")
	}
	if got := exitCodeFor(err); got != exitVersionSkew {
		t.Errorf("exitCodeFor() = %d, want %d (exitVersionSkew)", got, exitVersionSkew)
	}
}

// TestExitCodeFor_OtherCodesFallBackToOne covers the specific codes issue
// #570 says must keep today's best-effort behavior: a plain outage
// (Unavailable, DeadlineExceeded) or an auth failure must not be
// misclassified as version skew.
func TestExitCodeFor_OtherCodesFallBackToOne(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument} {
		err := status.Error(code, "some failure")
		if got := exitCodeFor(err); got != 1 {
			t.Errorf("exitCodeFor(%s) = %d, want 1", code, got)
		}
	}
}
