// FR45/FR47/NFR15: AwaitConfigAck's handler-level coverage against
// fakeRepo/fakeAuthz (see server_test.go's fixtures and
// server_config_lifecycle_test.go's boardScopedAuthz/nonMemberAuthz,
// reused here) -- an already-resolved version returns immediately without
// ever registering a waiter, a still-pending version blocks on
// ackwait.Registry.Wait until Notify or the deadline, and a deadline is
// never surfaced as an error (FR47). ackwait.Registry's own Clamp/Wait/
// Notify semantics are covered in isolation, with short custom durations,
// by leaflab/api/ackwait/registry_test.go; this file proves AwaitConfigAck
// wires that package in correctly, not the package's own timing behaviour.
// NFR15's real cross-process, every-replica broadcast (a bounded wait
// pinned to one replica resolving via a KindAck event from another
// process) is proven against real RabbitMQ by
// nfr15_ack_broadcast_integration_test.go.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/api/ackwait"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// -- Already-resolved: no waiter registered --------------------------------

// TestAwaitConfigAck_AlreadyAccepted_ReturnsImmediately_NoRegistryNeeded
// proves a version that already resolved (accepted) before the call ever
// registers a waiter -- it need not even have a Registry wired (nil here)
// -- so a caller who calls AwaitConfigAck after already missing the ack
// broadcast (FR47's own doc comment: "an already-resolved version returns
// immediately, never registering a waiter that would miss it") still gets
// the right answer instead of hanging or timing out.
func TestAwaitConfigAck_AlreadyAccepted_ReturnsImmediately_NoRegistryNeeded(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: true, PushedAt: fixedPushedAt, AckedAt: &fixedAckedAtTest,
	}}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger())
	// No WithAckWaitRegistry call: nil ackWait must never be reached for an
	// already-resolved version.

	resp, err := server.AwaitConfigAck(authedTestCtx("alice"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 3, RequestedWaitSeconds: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result != pb.AckWaitResult_ACK_WAIT_RESULT_ACCEPTED {
		t.Errorf("Result = %v, want ACK_WAIT_RESULT_ACCEPTED", resp.Result)
	}
}

// TestAwaitConfigAck_AlreadyRejected_ReturnsVerbatimReason_Immediately
// proves the same immediate-return path for a rejected version carries the
// firmware's verbatim rejection reason.
func TestAwaitConfigAck_AlreadyRejected_ReturnsVerbatimReason_Immediately(t *testing.T) {
	const reason = "I2C bus TIMEOUT -- addr=0x44"
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: &fixedAckedAtTest, RejectionReason: reason,
	}}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger())

	resp, err := server.AwaitConfigAck(authedTestCtx("alice"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 3, RequestedWaitSeconds: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result != pb.AckWaitResult_ACK_WAIT_RESULT_REJECTED {
		t.Errorf("Result = %v, want ACK_WAIT_RESULT_REJECTED", resp.Result)
	}
	if resp.RejectionReason != reason {
		t.Errorf("RejectionReason = %q, want verbatim %q", resp.RejectionReason, reason)
	}
}

// TestAwaitConfigAck_NoSuchVersion_NotFound proves a nonexistent version
// maps to the same not-found failure GetConfigStatus uses (FR34.1's "never
// indistinguishable from no push at all" extends to this RPC).
func TestAwaitConfigAck_NoSuchVersion_NotFound(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: nil}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger())

	_, err := server.AwaitConfigAck(authedTestCtx("alice"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 999})
	if err == nil {
		t.Fatal("AwaitConfigAck for a nonexistent version returned nil error, want not-found")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatal("error carries no Failure detail")
	}
	if detail.Entity != "device_config" {
		t.Errorf("Failure.Entity = %q, want %q", detail.Entity, "device_config")
	}
}

// TestAwaitConfigAck_NonMember_Refused proves NFR2's non-member refusal
// short-circuits before any repository read, same as FR34/FR35's RPCs.
func TestAwaitConfigAck_NonMember_Refused(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{Version: 1, PushedAt: fixedPushedAt}}
	server := NewLeafLabAPIServer(repo, nonMemberAuthz(7), nil, nil, nil, nil, discardLogger())

	_, err := server.AwaitConfigAck(authedTestCtx("mallory"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 1})
	if err == nil {
		t.Fatal("AwaitConfigAck for a non-member caller returned nil error, want a refusal")
	}
	if len(repo.getDeviceConfigVersionCalls) != 0 {
		t.Errorf("repository was reached %d times by a non-member caller, want 0", len(repo.getDeviceConfigVersionCalls))
	}
}

// -- Still-pending: registers and waits on ackwait.Registry -----------------

// TestAwaitConfigAck_PendingThenNotifyAccept_ResolvesAccepted proves a
// still-pending version registers a waiter and resolves ACCEPTED the
// moment Notify is called for it -- standing in for the KindAck event a
// real Subscriber would deliver (see nfr15_ack_broadcast_integration_test.go
// for that real delivery path).
func TestAwaitConfigAck_PendingThenNotifyAccept_ResolvesAccepted(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: nil,
	}}
	registry := ackwait.NewRegistry()
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger()).WithAckWaitRegistry(registry)

	respCh := make(chan *pb.AwaitConfigAckResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := server.AwaitConfigAck(authedTestCtx("alice"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 3, RequestedWaitSeconds: 5})
		respCh <- resp
		errCh <- err
	}()

	// Give the handler's goroutine time to reach ackwait.Registry.Wait and
	// register before Notify -- Notify is a no-op for a key nobody is
	// waiting on yet (see registry_test.go's own NoWaiterRegistered test),
	// so this must actually happen after registration, not before.
	time.Sleep(100 * time.Millisecond)
	registry.Notify("board-a", 3, true, "")

	select {
	case resp := <-respCh:
		if err := <-errCh; err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Result != pb.AckWaitResult_ACK_WAIT_RESULT_ACCEPTED {
			t.Errorf("Result = %v, want ACK_WAIT_RESULT_ACCEPTED", resp.Result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AwaitConfigAck did not resolve after Notify")
	}
}

// TestAwaitConfigAck_PendingThenNotifyReject_ReturnsVerbatimReason mirrors
// the accept case for a rejecting ack.
func TestAwaitConfigAck_PendingThenNotifyReject_ReturnsVerbatimReason(t *testing.T) {
	const reason = "config CRC mismatch"
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: nil,
	}}
	registry := ackwait.NewRegistry()
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger()).WithAckWaitRegistry(registry)

	respCh := make(chan *pb.AwaitConfigAckResponse, 1)
	go func() {
		resp, err := server.AwaitConfigAck(authedTestCtx("alice"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 3, RequestedWaitSeconds: 5})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		respCh <- resp
	}()

	time.Sleep(100 * time.Millisecond)
	registry.Notify("board-a", 3, false, reason)

	select {
	case resp := <-respCh:
		if resp.Result != pb.AckWaitResult_ACK_WAIT_RESULT_REJECTED {
			t.Errorf("Result = %v, want ACK_WAIT_RESULT_REJECTED", resp.Result)
		}
		if resp.RejectionReason != reason {
			t.Errorf("RejectionReason = %q, want verbatim %q", resp.RejectionReason, reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AwaitConfigAck did not resolve after Notify")
	}
}

// TestAwaitConfigAck_DeadlineElapses_ReturnsStillPendingAtDeadline_NeverError
// proves a caller who never gets notified gets
// ACK_WAIT_RESULT_STILL_PENDING_AT_DEADLINE, not a gRPC error -- FR47's
// "the server honours a maximum wait ... and returns still-pending-at-
// deadline rather than an error". A short RequestedWaitSeconds keeps this
// test fast; ackwait/registry_test.go's TestClamp_RequestOver30s_ClampsTo30s
// proves the exact 120s -> 30s clamp value in isolation without a real 30s
// wait here.
func TestAwaitConfigAck_DeadlineElapses_ReturnsStillPendingAtDeadline_NeverError(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: nil,
	}}
	registry := ackwait.NewRegistry()
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger()).WithAckWaitRegistry(registry)

	// requested_wait_seconds is whole seconds on the wire (proto uint32) --
	// the smallest nonzero value is 1s, still fast enough for a unit test.
	resp, err := server.AwaitConfigAck(authedTestCtx("alice"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 3, RequestedWaitSeconds: 1})
	if err != nil {
		t.Fatalf("AwaitConfigAck returned an error for an unresolved deadline, want ACK_WAIT_RESULT_STILL_PENDING_AT_DEADLINE with nil error: %v", err)
	}
	if resp.Result != pb.AckWaitResult_ACK_WAIT_RESULT_STILL_PENDING_AT_DEADLINE {
		t.Errorf("Result = %v, want ACK_WAIT_RESULT_STILL_PENDING_AT_DEADLINE", resp.Result)
	}
}

// TestAwaitConfigAck_NoRegistryWired_InternalFailure proves the defensive
// branch in AwaitConfigAck (a still-pending version but no
// ackwait.Registry attached -- should never happen once main.go's wiring
// is in place, but must fail closed rather than nil-pointer panic if it
// ever did) returns an Internal failure instead of panicking.
func TestAwaitConfigAck_NoRegistryWired_InternalFailure(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: nil,
	}}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger())
	// Deliberately no WithAckWaitRegistry call.

	_, err := server.AwaitConfigAck(authedTestCtx("alice"), &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 3, RequestedWaitSeconds: 5})
	if err == nil {
		t.Fatal("AwaitConfigAck with no ackwait.Registry wired returned nil error, want a refusal (not a panic)")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no Failure detail: %v", err)
	}
	if detail.Class != string(contract.FailureInternal) {
		t.Errorf("Failure.Class = %q, want %q", detail.Class, contract.FailureInternal)
	}
}

// TestAwaitConfigAck_CtxCancelled_ResolvesStillPendingAtDeadline_NotError
// proves a client that hangs up mid-wait resolves the same
// never-an-error way as a real deadline (ackwait.Registry.Wait's ctx.Done
// branch), exercised end to end through the handler.
func TestAwaitConfigAck_CtxCancelled_ResolvesStillPendingAtDeadline_NotError(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: nil,
	}}
	registry := ackwait.NewRegistry()
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, nil, nil, discardLogger()).WithAckWaitRegistry(registry)

	ctx, cancel := context.WithCancel(authedTestCtx("alice"))
	respCh := make(chan *pb.AwaitConfigAckResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := server.AwaitConfigAck(ctx, &pb.AwaitConfigAckRequest{DeviceId: "board-a", Version: 3, RequestedWaitSeconds: 30})
		respCh <- resp
		errCh <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case resp := <-respCh:
		if err := <-errCh; err != nil {
			t.Fatalf("unexpected error after ctx cancellation: %v", err)
		}
		if resp.Result != pb.AckWaitResult_ACK_WAIT_RESULT_STILL_PENDING_AT_DEADLINE {
			t.Errorf("Result = %v, want ACK_WAIT_RESULT_STILL_PENDING_AT_DEADLINE", resp.Result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AwaitConfigAck did not resolve after ctx cancellation")
	}
}
