// FR47's Registry/Clamp coverage: this file proves the clamping bound in
// isolation (deterministic, no real 30s wait) and the Wait/Notify pairing
// (accept, reject, deadline, ctx cancellation, multiple waiters, no-op
// notify) with short custom durations so the suite stays fast. See
// leaflab/api/server_awaitconfigack_test.go for the handler-level coverage
// that wires this Registry into AwaitConfigAck itself.
package ackwait

import (
	"context"
	"sync"
	"testing"
	"time"
)

// -- Clamp: FR47's 30s ceiling, proven without ever waiting 30s -----------

// TestClamp_RequestOver30s_ClampsTo30s is the issue's literal example: a
// requested deadline of 120s is clamped to exactly 30s, not rejected.
func TestClamp_RequestOver30s_ClampsTo30s(t *testing.T) {
	got := Clamp(120 * time.Second)
	if got != MaxWait {
		t.Errorf("Clamp(120s) = %v, want %v", got, MaxWait)
	}
}

// TestClamp_RequestUnderMax_Unchanged proves a request already within the
// bound passes through untouched -- clamping only ever lowers, and only
// when the request exceeds MaxWait.
func TestClamp_RequestUnderMax_Unchanged(t *testing.T) {
	got := Clamp(5 * time.Second)
	if got != 5*time.Second {
		t.Errorf("Clamp(5s) = %v, want 5s unchanged", got)
	}
}

// TestClamp_ExactlyMax_Unchanged proves the boundary itself is not clamped
// (the check is strictly greater-than).
func TestClamp_ExactlyMax_Unchanged(t *testing.T) {
	got := Clamp(MaxWait)
	if got != MaxWait {
		t.Errorf("Clamp(MaxWait) = %v, want %v unchanged", got, MaxWait)
	}
}

// TestClamp_ZeroOrNegative_ClampsToMax proves an unset/zero request is not
// "return immediately" -- it clamps up to the server's full bound, per
// Clamp's own doc comment.
func TestClamp_ZeroOrNegative_ClampsToMax(t *testing.T) {
	for _, requested := range []time.Duration{0, -1 * time.Second} {
		if got := Clamp(requested); got != MaxWait {
			t.Errorf("Clamp(%v) = %v, want %v", requested, got, MaxWait)
		}
	}
}

// -- Wait/Notify: accept, reject, deadline, cancellation -------------------

// TestWait_NotifyAccept_ResolvesAccepted proves a waiter registered ahead
// of Notify resolves ACCEPTED with no reason, well before its deadline.
func TestWait_NotifyAccept_ResolvesAccepted(t *testing.T) {
	r := NewRegistry()

	resultCh := make(chan Result, 1)
	go func() {
		res, _ := r.Wait(context.Background(), "board-a", 3, 5*time.Second)
		resultCh <- res
	}()

	waitUntilWaiterRegistered(t, r, "board-a", 3)
	r.Notify("board-a", 3, true, "")

	select {
	case res := <-resultCh:
		if res != ResultAccepted {
			t.Errorf("Wait result = %v, want %v", res, ResultAccepted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not resolve after Notify")
	}
}

// TestWait_NotifyReject_ResolvesRejectedWithVerbatimReason proves a
// rejecting ack carries the firmware's exact reason string through to the
// caller, never paraphrased.
func TestWait_NotifyReject_ResolvesRejectedWithVerbatimReason(t *testing.T) {
	r := NewRegistry()
	const reason = "I2C bus TIMEOUT -- addr=0x44"

	resultCh := make(chan struct {
		res    Result
		reason string
	}, 1)
	go func() {
		res, reason := r.Wait(context.Background(), "board-a", 3, 5*time.Second)
		resultCh <- struct {
			res    Result
			reason string
		}{res, reason}
	}()

	waitUntilWaiterRegistered(t, r, "board-a", 3)
	r.Notify("board-a", 3, false, reason)

	select {
	case got := <-resultCh:
		if got.res != ResultRejected {
			t.Errorf("Wait result = %v, want %v", got.res, ResultRejected)
		}
		if got.reason != reason {
			t.Errorf("Wait reason = %q, want verbatim %q", got.reason, reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not resolve after Notify")
	}
}

// TestWait_NoNotifyBeforeDeadline_ResolvesStillPendingAtDeadline_NeverError
// proves FR47's "never surfaced as an error" property: a waiter that never
// gets notified resolves StillPendingAtDeadline once its (short, for test
// speed) requested duration elapses -- Wait's signature has no error
// return at all, so "not an error" is structural, not just a runtime
// property, but this exercises the actual timeout path.
func TestWait_NoNotifyBeforeDeadline_ResolvesStillPendingAtDeadline_NeverError(t *testing.T) {
	r := NewRegistry()

	start := time.Now()
	res, reason := r.Wait(context.Background(), "board-a", 3, 50*time.Millisecond)
	elapsed := time.Since(start)

	if res != ResultStillPendingAtDeadline {
		t.Errorf("Wait result = %v, want %v", res, ResultStillPendingAtDeadline)
	}
	if reason != "" {
		t.Errorf("Wait reason = %q, want empty on a still-pending result", reason)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Wait took %v to resolve a 50ms deadline, want close to 50ms", elapsed)
	}
}

// TestWait_CtxCancelled_ResolvesStillPendingAtDeadline proves a cancelled
// context (e.g. the caller hung up) resolves the same never-an-error way
// as a real deadline, not a distinct error path.
func TestWait_CtxCancelled_ResolvesStillPendingAtDeadline(t *testing.T) {
	r := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())

	resultCh := make(chan Result, 1)
	go func() {
		res, _ := r.Wait(ctx, "board-a", 3, 5*time.Second)
		resultCh <- res
	}()

	waitUntilWaiterRegistered(t, r, "board-a", 3)
	cancel()

	select {
	case res := <-resultCh:
		if res != ResultStillPendingAtDeadline {
			t.Errorf("Wait result after ctx cancel = %v, want %v", res, ResultStillPendingAtDeadline)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not resolve after ctx cancellation")
	}
}

// TestNotify_MultipleWaitersSameKey_AllResolve proves two concurrent
// AwaitConfigAck callers waiting on the same (device_id, version) both get
// the same outcome from one Notify -- neither is left hanging because the
// other "consumed" the notification.
func TestNotify_MultipleWaitersSameKey_AllResolve(t *testing.T) {
	r := NewRegistry()
	const n = 3

	var wg sync.WaitGroup
	results := make([]Result, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, _ := r.Wait(context.Background(), "board-a", 3, 5*time.Second)
			results[i] = res
		}(i)
	}

	waitUntilWaiterCount(t, r, "board-a", 3, n)
	r.Notify("board-a", 3, true, "")
	wg.Wait()

	for i, res := range results {
		if res != ResultAccepted {
			t.Errorf("waiter %d result = %v, want %v", i, res, ResultAccepted)
		}
	}
}

// TestNotify_NoWaiterRegistered_NoOp proves Notify for a key nobody is
// currently awaiting (e.g. every waiter already timed out, or nobody ever
// called AwaitConfigAck for this version) does not panic or block.
func TestNotify_NoWaiterRegistered_NoOp(t *testing.T) {
	r := NewRegistry()
	r.Notify("board-nobody-waiting", 99, true, "")
}

// TestNotify_DistinctVersionsIndependent proves Notify for one version
// does not resolve a waiter registered on a different version of the same
// device -- keys are (device_id, version), not device_id alone.
func TestNotify_DistinctVersionsIndependent(t *testing.T) {
	r := NewRegistry()

	resultCh := make(chan Result, 1)
	go func() {
		res, _ := r.Wait(context.Background(), "board-a", 3, 5*time.Second)
		resultCh <- res
	}()
	waitUntilWaiterRegistered(t, r, "board-a", 3)

	// Notify a different version on the same device -- must not resolve
	// the version-3 waiter above.
	r.Notify("board-a", 4, true, "")

	select {
	case res := <-resultCh:
		t.Fatalf("waiter for version 3 resolved (%v) after Notify for version 4 -- keys are not device-scoped", res)
	case <-time.After(200 * time.Millisecond):
		// Expected: no resolution yet.
	}

	r.Notify("board-a", 3, true, "")
	select {
	case res := <-resultCh:
		if res != ResultAccepted {
			t.Errorf("Wait result = %v, want %v", res, ResultAccepted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait for version 3 never resolved after its own Notify")
	}
}

// waiterCount is a whitebox helper (this file is package ackwait, not
// ackwait_test) reading r.waiters directly -- used only to synchronise
// tests with Wait's registration having actually happened, avoiding a
// fixed sleep before Notify.
func waiterCount(r *Registry, deviceID string, version int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.waiters[key{deviceID: deviceID, version: version}])
}

func waitUntilWaiterRegistered(t *testing.T, r *Registry, deviceID string, version int64) {
	t.Helper()
	waitUntilWaiterCount(t, r, deviceID, version, 1)
}

func waitUntilWaiterCount(t *testing.T, r *Registry, deviceID string, version int64, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if waiterCount(r, deviceID, version) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d waiter(s) to register for (%s, %d)", want, deviceID, version)
}

// TestRegistry_WaiterRemovedAfterTimeout proves Wait's deferred remove
// actually drops the waiter from r.waiters once it resolves by deadline --
// a waiter that leaks would grow the map unboundedly across repeated
// AwaitConfigAck calls that never get notified.
func TestRegistry_WaiterRemovedAfterTimeout(t *testing.T) {
	r := NewRegistry()
	r.Wait(context.Background(), "board-a", 3, 20*time.Millisecond)

	r.mu.Lock()
	_, stillPresent := r.waiters[key{deviceID: "board-a", version: 3}]
	r.mu.Unlock()
	if stillPresent {
		t.Error("waiter map still holds an entry for a key after its only waiter timed out -- Wait's deferred remove did not run")
	}
}
