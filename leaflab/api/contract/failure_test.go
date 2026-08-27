package contract

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestFromError_RoundTripsStructuredDetail proves a caller can recover
// class/entity/field from a Failure-carrying error via FromError -- a typed
// accessor over the gRPC status detail -- without ever touching the status
// message string (FR59.1).
func TestFromError_RoundTripsStructuredDetail(t *testing.T) {
	err := NotFound("board", "device_id", "This device could not be found.")

	detail, ok := FromError(err)
	if !ok {
		t.Fatalf("FromError(%v) = _, false; want a Failure detail", err)
	}
	if detail.Class != string(FailureNotFound) {
		t.Errorf("Class = %q, want %q", detail.Class, FailureNotFound)
	}
	if detail.Entity != "board" {
		t.Errorf("Entity = %q, want %q", detail.Entity, "board")
	}
	if detail.Field != "device_id" {
		t.Errorf("Field = %q, want %q", detail.Field, "device_id")
	}
}

// TestFromError_ClassDistinguishesWithoutMessageParsing constructs two
// errors that share the exact same reason text (so the status message
// string is identical) but different classes. If a caller ever needed to
// parse the message to tell them apart, this test would be unable to
// distinguish them by class alone -- it can, because the class lives in the
// structured detail, not in prose (FR59.1).
func TestFromError_ClassDistinguishesWithoutMessageParsing(t *testing.T) {
	const sharedReason = "Same reason text used for both errors on purpose."
	notFoundErr := NotFound("board", "device_id", sharedReason)
	invalidErr := InvalidArgument("board", "device_id", sharedReason)

	stA, _ := status.FromError(notFoundErr)
	stB, _ := status.FromError(invalidErr)
	if stA.Message() != stB.Message() {
		t.Fatalf("test setup: expected identical status messages, got %q and %q", stA.Message(), stB.Message())
	}

	detailA, ok := FromError(notFoundErr)
	if !ok {
		t.Fatal("FromError(notFoundErr) missing detail")
	}
	detailB, ok := FromError(invalidErr)
	if !ok {
		t.Fatal("FromError(invalidErr) missing detail")
	}

	if detailA.Class == detailB.Class {
		t.Fatalf("expected distinct classes despite identical message text, got %q for both", detailA.Class)
	}
	if detailA.Class != string(FailureNotFound) {
		t.Errorf("detailA.Class = %q, want %q", detailA.Class, FailureNotFound)
	}
	if detailB.Class != string(FailureInvalidArgument) {
		t.Errorf("detailB.Class = %q, want %q", detailB.Class, FailureInvalidArgument)
	}
}

// TestFromError_NoDetail_ReturnsFalse proves FromError degrades gracefully
// (no panic, no zero-value Failure with ok=true) for an error that never
// carried a Failure detail.
func TestFromError_NoDetail_ReturnsFalse(t *testing.T) {
	err := status.New(codes.Internal, "boom").Err()
	if _, ok := FromError(err); ok {
		t.Fatal("FromError on a detail-less status returned ok=true")
	}
	if _, ok := FromError(nil); ok {
		t.Fatal("FromError(nil) returned ok=true")
	}
}

// TestRefuse_CarriesReasonAndAlternative_DistinctFromInvalidArgument covers
// FR59.3: Refuse's detail carries both reason and alternative, and is
// distinguishable from an ordinary invalid_argument failure by class (and
// gRPC code) alone -- never by parsing the alternative text out of a
// combined message string.
func TestRefuse_CarriesReasonAndAlternative_DistinctFromInvalidArgument(t *testing.T) {
	refuseErr := Refuse("board", "device_id",
		"This board cannot be deleted while it has active sensors.",
		"Deactivate the board instead.")
	plainErr := InvalidArgument("board", "device_id",
		"This board cannot be deleted while it has active sensors.")

	refuseDetail, ok := FromError(refuseErr)
	if !ok {
		t.Fatal("FromError(refuseErr) missing detail")
	}
	if refuseDetail.Class != string(FailureRefusedWithAlternative) {
		t.Errorf("refuseDetail.Class = %q, want %q", refuseDetail.Class, FailureRefusedWithAlternative)
	}
	if refuseDetail.Reason == "" {
		t.Error("refuseDetail.Reason is empty, want the refusal reason")
	}
	if refuseDetail.Alternative == "" {
		t.Error("refuseDetail.Alternative is empty, want the named alternative path")
	}

	plainDetail, ok := FromError(plainErr)
	if !ok {
		t.Fatal("FromError(plainErr) missing detail")
	}
	if plainDetail.Class == refuseDetail.Class {
		t.Fatalf("ordinary invalid_argument and Refuse share class %q, want distinct classes", plainDetail.Class)
	}
	if plainDetail.Alternative != "" {
		t.Errorf("plainDetail.Alternative = %q, want empty for an ordinary invalid_argument failure", plainDetail.Alternative)
	}

	stRefuse, _ := status.FromError(refuseErr)
	stPlain, _ := status.FromError(plainErr)
	if stRefuse.Code() == stPlain.Code() {
		t.Fatalf("Refuse and InvalidArgument share gRPC code %v, want distinct codes", stRefuse.Code())
	}
	if stRefuse.Code() != codes.FailedPrecondition {
		t.Errorf("Refuse gRPC code = %v, want %v", stRefuse.Code(), codes.FailedPrecondition)
	}
}

// TestInternal_ReasonMustBeCallerSuppliedGenericText documents (via a
// passing assertion, not enforcement -- reason text is a caller
// responsibility per Internal's doc comment) that the detail returned by
// Internal carries exactly the reason the caller passed, proving no
// underlying error text leaks in automatically.
func TestInternal_ReasonMustBeCallerSuppliedGenericText(t *testing.T) {
	const generic = "Could not process this request right now. Please try again."
	err := Internal("device_config", "", generic)
	detail, ok := FromError(err)
	if !ok {
		t.Fatal("FromError(err) missing detail")
	}
	if detail.Reason != generic {
		t.Errorf("Reason = %q, want %q", detail.Reason, generic)
	}
	if detail.Class != string(FailureInternal) {
		t.Errorf("Class = %q, want %q", detail.Class, FailureInternal)
	}
}
