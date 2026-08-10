package repository

import "time"

// ReconcileWatermark is the state of the singleton reconcile_watermark row
// (migration 006) at the instant it was read -- see ARCHITECTURE.md
// "Reconcile watermark" and issue #545. A GitSHA of "" is the sentinel value
// the migration seeds the row with and means the SAME thing a caller sees
// as "there is no watermark yet" -- see ShouldApplyReconcile.
type ReconcileWatermark struct {
	GitSHA            string
	SourceCommittedAt int64
	DiscoveredAt      int64
	UpdatedAt         time.Time
}

// ReconcileSource is the ordering metadata one ReconcileApps call carries,
// sourced directly from AppManifestSet.git_sha / source_committed_at /
// discovered_at (appmeta.proto). Passed to AppRepository.Reconcile so the
// watermark comparison lives next to the write it guards, inside the same
// transaction.
type ReconcileSource struct {
	GitSHA            string
	SourceCommittedAt int64
	DiscoveredAt      int64
}

// effectiveOrderingKey is source_committed_at when populated (nonzero),
// falling back to discovered_at otherwise. See appmeta.proto's doc comment
// on source_committed_at for why discovered_at (sweep time) is not a valid
// ordering key on its own, and why the fallback exists anyway: backward
// compatibility with an AppManifestSet produced by a release_helper_go
// binary that predates the field.
func effectiveOrderingKey(sourceCommittedAt, discoveredAt int64) int64 {
	if sourceCommittedAt != 0 {
		return sourceCommittedAt
	}
	return discoveredAt
}

// ShouldApplyReconcile implements the watermark comparison and tie-break
// rules from ARCHITECTURE.md "Reconcile watermark":
//
//   - No watermark yet (current == nil, or current.GitSHA == "" -- the
//     sentinel row migration 006 seeds, indistinguishable to callers from a
//     wholly absent row): apply. The very first reconcile is always
//     accepted.
//   - incoming.GitSHA == current.GitSHA: apply, unconditionally. The
//     identical commit reconciled twice (a manual re-run of the same
//     workflow run, or the CLI pointed at the same manifest set again with
//     a fresh idempotency key) is harmless to re-apply -- idempotency
//     already covers true RPC-level replays, this covers the same commit
//     arriving via two different calls -- and clock skew between two
//     sweeps of the SAME commit must never produce a false-stale rejection.
//   - Otherwise, compare effectiveOrderingKey(incoming) against
//     effectiveOrderingKey(current). Strictly older: skip (stale). Equal or
//     newer: apply. The equal case is deliberate, not an edge case being
//     waved through -- two different commits landing in the same
//     wall-clock second (or two manifest sets both falling back to
//     discovered_at) must not block a legitimate merge just because they
//     tied.
//
// This is shared, not duplicated, by every AppRepository implementation
// (postgres and fake) — see promotability.go's DerivePromotability for the
// same pattern and rationale.
func ShouldApplyReconcile(incoming ReconcileSource, current *ReconcileWatermark) bool {
	if current == nil || current.GitSHA == "" {
		return true
	}
	if incoming.GitSHA == current.GitSHA {
		return true
	}
	return effectiveOrderingKey(incoming.SourceCommittedAt, incoming.DiscoveredAt) >=
		effectiveOrderingKey(current.SourceCommittedAt, current.DiscoveredAt)
}
