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
// manifest paths (FR76.4). The claim-challenge service (leaflab/api)
// implements this to evaluate open challenge rounds; the processor only
// reports what it saw and never decides the outcome of a round.
//
// Scaffold note: this interface and its two call sites (handleSensorReading,
// handleManifest) are the wiring points named by #1195's Scaffold phase. The
// detection logic itself — the wrap-aware uptime_s regression test, the
// retained-vs-non-retained manifest distinction plumbed from the MQTT/AMQP
// consumer, and the "no reading ever received" lookup — is Implementation
// phase work and is intentionally not built yet (see detectUptimeRegression
// and the handleManifest call site below).
type RestartObserver interface {
	ObserveRestart(ctx context.Context, signal RestartSignal)
}

// noopRestartObserver discards every restart signal. It is the default until
// the processor is wired to a real claim-challenge evaluator.
type noopRestartObserver struct{}

func (noopRestartObserver) ObserveRestart(context.Context, RestartSignal) {}

// detectUptimeRegression is the FR76.4 restart-signal test: uptime_s is both
// lower than the last recorded value for the board and small, to account for
// the uint32 millisecond wrap at ~49.7 days. Implemented in the Implementation
// phase (#1195); the scaffold always reports no regression so the observer
// hook compiles and is wired, without fabricating signals.
func detectUptimeRegression(lastUptimeS uint32, haveLast bool, currentUptimeS uint32) bool {
	_ = lastUptimeS
	_ = haveLast
	_ = currentUptimeS
	// TODO(#1195 Implementation): lastUptimeS > currentUptimeS (regression) AND
	// currentUptimeS is small (wrap-aware — a genuine restart, not a wrap of a
	// still-running counter).
	return false
}

// detectManifestRestartException is FR76.4's narrow exception: a
// DeviceManifest delivered non-retained, observed for a device_id from which
// no reading has ever been received, is accepted as that round's restart
// signal. A retained manifest is never a restart signal under any
// circumstances, and the exception never applies once any reading has been
// received for the device_id — not "no accepted config", which would
// wrongly admit the legacy fleet (compile-time sensor list, no config row).
//
// Implemented in the Implementation phase (#1195): retained-vs-non-retained
// plumbing from the MQTT/AMQP consumer, and the "no reading ever received"
// lookup, do not exist yet. The scaffold always reports false.
func detectManifestRestartException(retained bool, hasEverReceivedReading bool) bool {
	_ = retained
	_ = hasEverReceivedReading
	return false
}
