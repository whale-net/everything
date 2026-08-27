package ackwait

import (
	"context"
	"sync"
	"time"
)

// Result is AwaitConfigAck's FR47 outcome -- mirrors
// leaflabapipb.AckWaitResult exactly (leaflab/api/proto/api.proto), kept as
// a package-local type so this package stays free of a proto dependency.
// leaflab/api's AwaitConfigAck handler is the one place that converts
// between the two.
type Result string

const (
	// ResultAccepted: the board's ack for this version accepted it.
	ResultAccepted Result = "accepted"
	// ResultRejected: the board's ack for this version rejected it. The
	// verbatim reason accompanies this Result out of Wait, not out of
	// Result itself.
	ResultRejected Result = "rejected"
	// ResultStillPendingAtDeadline: neither outcome was observed before
	// the (possibly clamped) deadline elapsed. Never an error (FR47).
	ResultStillPendingAtDeadline Result = "still_pending_at_deadline"
)

// MaxWait is FR47's server-enforced ceiling on a caller's requested wait.
// A longer request is clamped down to this by Clamp -- never rejected as
// invalid, and never honoured past it.
const MaxWait = 30 * time.Second

// Clamp returns requested bounded to (0, MaxWait]. A non-positive
// requested duration also clamps up to MaxWait: FR47 defines no meaning
// for "wait zero seconds" (that is what GetConfigStatus, a single
// immediate check, is for), so an unset/zero request gets the server's
// full bound rather than returning still-pending-at-deadline instantly.
func Clamp(requested time.Duration) time.Duration {
	if requested <= 0 || requested > MaxWait {
		return MaxWait
	}
	return requested
}

// key identifies one in-flight (or resolvable) ack wait.
type key struct {
	deviceID string
	version  int64
}

// outcome is what Notify hands to every waiter registered for a key.
type outcome struct {
	accepted bool
	reason   string
}

// waiter is one call to Wait's registration in Registry.waiters.
type waiter struct {
	ch chan outcome
}

// Registry tracks this process's in-flight AwaitConfigAck waiters, keyed
// by (device_id, version). Safe for concurrent use. See this package's doc
// comment for why one Registry per API replica -- not a shared store --
// satisfies NFR15's every-replica broadcast constraint.
type Registry struct {
	mu      sync.Mutex
	waiters map[key][]*waiter
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{waiters: make(map[key][]*waiter)}
}

// Wait blocks until Notify is called for (deviceID, version), ctx is
// cancelled, or requested (clamped through Clamp) elapses -- whichever
// comes first. A ctx cancellation and a deadline both resolve as
// ResultStillPendingAtDeadline: FR47 never surfaces this wait as an error
// to the caller; leaflab/api's AwaitConfigAck handler is responsible for
// distinguishing a genuinely cancelled RPC (client hung up) from a real
// deadline if it needs to, e.g. for metrics.
//
// Wait does not itself check whether (deviceID, version) has already
// resolved before this call began -- see this package's doc comment.
func (r *Registry) Wait(ctx context.Context, deviceID string, version int64, requested time.Duration) (Result, string) {
	k := key{deviceID: deviceID, version: version}
	w := &waiter{ch: make(chan outcome, 1)}

	r.mu.Lock()
	r.waiters[k] = append(r.waiters[k], w)
	r.mu.Unlock()

	defer r.remove(k, w)

	timer := time.NewTimer(Clamp(requested))
	defer timer.Stop()

	select {
	case res := <-w.ch:
		if res.accepted {
			return ResultAccepted, ""
		}
		return ResultRejected, res.reason
	case <-timer.C:
		return ResultStillPendingAtDeadline, ""
	case <-ctx.Done():
		return ResultStillPendingAtDeadline, ""
	}
}

// remove drops w from key's waiter list, e.g. after Wait's own deadline or
// ctx cancellation fires -- so a waiter that never gets notified does not
// leak in r.waiters forever.
func (r *Registry) remove(k key, w *waiter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ws := r.waiters[k]
	for i, cand := range ws {
		if cand == w {
			r.waiters[k] = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	if len(r.waiters[k]) == 0 {
		delete(r.waiters, k)
	}
}

// Notify resolves every waiter currently registered for (deviceID,
// version) with the given ack outcome, exactly once each. A no-op if no
// waiter is registered for that key -- e.g. nobody is currently awaiting
// this version's ack, or every waiter for it already timed out.
//
// Callers invoke this from a leaflab/invalidation Handler for KindAck
// events (see that package's doc comment); Notify itself has no
// dependency on the invalidation package, so it is independently testable
// with a synthetic outcome.
func (r *Registry) Notify(deviceID string, version int64, accepted bool, reason string) {
	k := key{deviceID: deviceID, version: version}

	r.mu.Lock()
	ws := r.waiters[k]
	delete(r.waiters, k)
	r.mu.Unlock()

	for _, w := range ws {
		w.ch <- outcome{accepted: accepted, reason: reason}
	}
}
