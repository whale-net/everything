// Package ackwait implements FR47's bounded wait: AwaitConfigAck registers
// a waiter for one pushed config version's ack and resolves it exactly
// once -- either when this process observes that version's ack (via
// Registry.Notify, called from the leaflab/invalidation Subscriber's
// KindAck handler -- see that package's doc comment) or when the requested
// wait (clamped through Clamp) elapses first, whichever comes first. A
// deadline never produces an error: Wait returns ResultStillPendingAtDeadline.
//
// This package has no database dependency and does not itself decide
// whether a version has *already* resolved before Wait was called --
// leaflab/api's AwaitConfigAck handler (Implementation-phase work) is
// responsible for checking device_config's current state first and calling
// Wait only when it is still pending, or an ack that committed moments
// before the caller registered would never be observed here.
//
// One Registry per API process (not a shared store) is sufficient to
// satisfy NFR15's every-replica broadcast constraint: every API replica
// runs its own leaflab/invalidation Subscriber bound to the same fanout
// exchange, so every replica's own Registry independently observes every
// KindAck event and resolves its own waiters identically -- see
// leaflab/invalidation's doc comment for why fanout (not a competing-consumer
// queue) is the mechanism choice this depends on.
package ackwait
