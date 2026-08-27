package main

import (
	"context"
	"log/slog"
)

// evidenceClassColumnValue maps a RestartEvidenceClass to the value stored in
// claim_challenge_round.evidence_class (FR76.4 — admin/audit only, never
// disclosed to the challenger).
func evidenceClassColumnValue(evidence RestartEvidenceClass) string {
	switch evidence {
	case RestartEvidenceUptimeRegression:
		return "uptime_regression"
	case RestartEvidenceManifestNoReading:
		return "manifest_no_reading"
	default:
		return ""
	}
}

// claimRoundObserver implements RestartObserver by writing directly to the
// claim_challenge_round table leaflab/api owns (same Postgres database). It
// deliberately does no more than that: matching a restart signal to a round
// is a narrow, idempotent write, whereas deciding a challenge's overall
// discharge/ownership outcome needs the household/ownership/audit machinery
// that lives in leaflab/api, which evaluates it lazily instead.
type claimRoundObserver struct {
	repo   *Repository
	logger *slog.Logger
}

// NewClaimRoundObserver returns a RestartObserver that marks matching open
// challenge rounds as satisfied (FR76.4).
func NewClaimRoundObserver(repo *Repository, logger *slog.Logger) RestartObserver {
	return &claimRoundObserver{repo: repo, logger: logger}
}

func (o *claimRoundObserver) ObserveRestart(ctx context.Context, signal RestartSignal) {
	evidenceClass := evidenceClassColumnValue(signal.Evidence)
	if evidenceClass == "" {
		return // RestartEvidenceNone or unrecognized: never a valid signal.
	}
	// Best-effort: a failed write here just means a genuine restart doesn't
	// get credited to an open round this time — the claimant can retry
	// within the round's bound, so this never blocks message processing.
	if err := o.repo.MarkRoundSatisfied(ctx, signal.DeviceID, signal.ObservedAt, evidenceClass); err != nil {
		o.logger.Warn("mark claim round satisfied failed", "device_id", signal.DeviceID, "err", err)
	}
}
