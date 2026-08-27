package main

import (
	"context"
	"time"
)

// RestartEvidenceClass identifies which observable satisfied a restart signal
// (FR76.4). Recorded for admin/audit only; which class applied is never
// disclosed to the challenger.
type RestartEvidenceClass int

const (
	// RestartEvidenceNone is the zero value; never a valid signal.
	RestartEvidenceNone RestartEvidenceClass = iota
	// RestartEvidenceUptimeRegression: a reading's uptime_s is both lower
	// than the last recorded uptime_s for the board and small (wrap-aware,
	// accounting for the uint32 millisecond wrap at ~49.7 days). The general
	// case, and the only evidence class for any board with a prior reading.
	RestartEvidenceUptimeRegression
	// RestartEvidenceManifestNoReading: narrow exception — a DeviceManifest
	// delivered non-retained, observed for a device_id from which no reading
	// has ever been received. A retained manifest is never a restart signal
	// under any circumstances, and this exception never applies once a
	// reading has been received for the device_id (FR76.4).
	RestartEvidenceManifestNoReading
)

// RestartSignal is one observed board restart, reported so the FR76 claim
// challenge service can match it against an open, challenger-marked round.
type RestartSignal struct {
	DeviceID   string
	ObservedAt time.Time
	Evidence   RestartEvidenceClass
}

// RestartObserver receives restart signals observed on the reading and
// manifest paths (FR76.4). The implementation only marks matching open
// challenge rounds as satisfied (a narrow, idempotent DB write against the
// same claim_challenge_round table leaflab/api owns) — it never decides a
// challenge's overall discharge/ownership outcome, which stays in
// leaflab/api (evaluated lazily, e.g. at PollChallengeState) since that is
// where the household/ownership/audit machinery already lives.
type RestartObserver interface {
	ObserveRestart(ctx context.Context, signal RestartSignal)
}

// noopRestartObserver discards every restart signal. It is the default until
// the processor is wired to a real claim-round evaluator (see main.go).
type noopRestartObserver struct{}

func (noopRestartObserver) ObserveRestart(context.Context, RestartSignal) {}

// smallUptimeThresholdS is the "small" half of FR76.4's wrap-aware restart
// test: a regression to a value below this is treated as a genuine reboot
// (uptime reset near zero), while a regression that lands on another large
// value is treated as message reordering/jitter, not a restart — "a
// regression from a large value to a large value is not a restart; large to
// small is." One hour comfortably separates "just booted" from "has been
// running a while," and is far below the ~49.7-day uint32 millisecond wrap
// period, so it is not itself sensitive to the wrap boundary.
const smallUptimeThresholdS = 3600

// detectUptimeRegression is the FR76.4 restart-signal test: uptime_s is both
// lower than the last recorded value for the board and small (below
// smallUptimeThresholdS), to account for the uint32 millisecond wrap at
// ~49.7 days. Without a prior reading there is nothing to regress from, so
// haveLast=false is never a signal — this also means a board's very first
// reading is never mistaken for a restart.
func detectUptimeRegression(lastUptimeS uint32, haveLast bool, currentUptimeS uint32) bool {
	if !haveLast {
		return false
	}
	if currentUptimeS >= lastUptimeS {
		return false // not a regression at all
	}
	return currentUptimeS < smallUptimeThresholdS
}

// detectManifestRestartException is FR76.4's narrow exception: a
// DeviceManifest delivered non-retained, observed for a device_id from which
// no reading has ever been received, is accepted as that round's restart
// signal. A retained manifest is never a restart signal under any
// circumstances, and the exception never applies once any reading has been
// received for the device_id — not "no accepted config", which would
// wrongly admit the legacy fleet (compile-time sensor list, no config row).
func detectManifestRestartException(retained bool, hasEverReceivedReading bool) bool {
	if hasEverReceivedReading {
		return false
	}
	return !retained
}
