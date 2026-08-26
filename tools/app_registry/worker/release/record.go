// record.go implements VerifyPublished and RecordTargetState: the two
// activities that read/write release_run/release_run_target state
// directly against Postgres via repository.Registry -- the same "worker
// talks to Postgres directly, not through the gRPC API" pattern
// worker/main.go's outbox drainer already uses (repo :=
// postgres.NewRepository(pool)), not routed through pb.ReleaseRegistryClient
// (which has no RPC for mutating release_run_target -- TriggerRelease/
// GetRelease/ListReleases are read/create only, see
// server/handlers/release.go).
package release

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// RecordResolvedPlan implements ReleaseActivities.RecordResolvedPlan (issue
// #906, validation finding #903): stamps resolvedPlan onto
// release_run.resolved_plan for releaseRunID via
// Registry.ReleaseRuns().SetResolvedPlan, the same direct-Postgres pattern
// VerifyPublished/RecordTargetState use (see this file's package doc
// comment).
func (a *Activities) RecordResolvedPlan(ctx context.Context, releaseRunID string, resolvedPlan []byte) error {
	if a.Registry == nil {
		return fmt.Errorf("record resolved plan for release run %s: Activities.Registry not configured", releaseRunID)
	}
	if err := a.Registry.ReleaseRuns().SetResolvedPlan(ctx, releaseRunID, resolvedPlan); err != nil {
		return fmt.Errorf("record resolved plan for release run %s: %w", releaseRunID, err)
	}
	return nil
}

// VerifyPublished implements ReleaseActivities.VerifyPublished (FR12,
// issue #973): for every release_run_target row under releaseRunID,
// confirms an artifact for that target's owner+kind currently exists in
// the published state (repository.ArtifactStatePublished) via
// Registry.Artifacts().GetArtifact with LatestPublished -- the same
// registry-visible signal GetEnvironmentState/promotion already treat as
// "this exists and is usable" -- AND (when expectedVersions has an entry
// for the target) that its version matches.
//
// expectedVersions is keyed by repository.TargetKey format and comes
// directly from FinalizePublish's own per-target result
// (FinalizeResult.Targets' EffectiveVersion, threaded through by
// workflow.go's ReleaseWorkflow) -- NOT re-derived from
// release_run.resolved_plan's plan-time JSON as PR #976's first pass did.
// That re-derivation could not distinguish a real finalize failure from
// ExecuteRelease's legitimate no-op-rebuild path, where the actually-
// published version legitimately differs from the plan-time requested one
// (see FinalizeTargetOutcome's doc comment) -- comparing against
// FinalizePublish's own reported EffectiveVersion instead makes that
// distinction for free, since EffectiveVersion already reflects whichever
// version was actually reused/published.
//
// This closes the gap issue #973 reported: FinalizePublish deliberately
// does not fail the workflow when one target's finalize-app/finalize-chart
// call fails (e.g. a GHCR retag returning DENIED), leaving that target's
// RecordArtifact call never made -- but if an OLDER version of the same
// target was already Published from a prior release, the presence+state
// check alone still found *that* artifact and reported the target
// satisfied, masking the real failure. In practice, workflow.go's
// ReleaseWorkflow now routes a real finalize failure straight to that
// target's Failed state without even calling into this method's result for
// it (see FinalizeResult.Targets' doc comment) -- the version comparison
// here remains as defense in depth for any target VerifyPublished is still
// asked to check.
//
// Defensive fallback: a target with no entry in expectedVersions (should
// not happen for any target FinalizePublish actually processed -- kept
// only for old/test data and any future caller that omits it) skips the
// version check and falls back to the prior presence+Published-state-only
// check for that target.
func (a *Activities) VerifyPublished(ctx context.Context, releaseRunID string, expectedVersions map[string]string) (VerifyResult, error) {
	if a.Registry == nil {
		return VerifyResult{}, fmt.Errorf("verify published for release run %s: Activities.Registry not configured", releaseRunID)
	}
	_, targets, err := a.Registry.ReleaseRuns().GetReleaseRun(ctx, releaseRunID)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("verify published for release run %s: %w", releaseRunID, err)
	}

	result := VerifyResult{AllPublished: true}
	for _, t := range targets {
		key := repository.TargetKey(t.Kind, t.OwnerFullName)
		// Every app target's Kind on the release_run_target row itself is
		// ArtifactKindImage regardless of the app's own AppType -- see
		// targetsFromAppsAndCharts' doc comment (ui/release_scope.go) for
		// why: TriggerRelease's Kind only distinguishes "app" from "chart"
		// (server/handlers/release.go only accepts IMAGE/CHART), and the
		// real image-vs-CLI-binary distinction is meant to be "resolved
		// downstream, per full_name, driven by the app's AppType" (that
		// same doc comment). recordCLIBinaryArtifact (finalize.go) already
		// does this resolution on the write side -- app_type "cli"/"binary"
		// apps get their published artifact recorded under
		// ArtifactKindBinary, never ArtifactKindImage (build-app skips the
		// image build for them entirely -- build_app.go's
		// ExecuteBuildApp). VerifyPublished must do the same resolution on
		// the read side, or it permanently reports "no published artifact
		// found" for every cli/binary app release: looking up Kind IMAGE
		// for an app that will never publish one is not a transient miss,
		// it can never succeed. Resolve via the app's real AppType (an
		// extra Registry.Apps() lookup, same one recordCLIBinaryArtifact
		// already does) rather than a hardcoded app-name list, so a future
		// cli/binary app is covered automatically instead of needing a
		// second edit to keep in sync -- mirrors ui/release_scope.go's
		// releaseTargetKindFromApp and cmd/metadata.go's
		// determineArtifactKind (both intentionally duplicated rather than
		// imported across those packages' boundaries; this one can't be
		// merged into either without introducing a new cross-package
		// dependency for a single two-line switch). key/expectedVersions
		// stay keyed by t.Kind (IMAGE) unchanged, matching finalizeTargets'
		// own ArtifactKindImage-keyed map -- only the actual GetArtifact
		// lookup kind changes.
		lookupKind := t.Kind
		if t.Kind == repository.ArtifactKindImage {
			app, aerr := a.Registry.Apps().GetAppByFullName(ctx, t.OwnerFullName)
			if aerr != nil {
				result.AllPublished = false
				if result.Failed == nil {
					result.Failed = map[string]string{}
				}
				result.Failed[key] = fmt.Sprintf("look up app %q to resolve its published artifact kind: %v", t.OwnerFullName, aerr)
				continue
			}
			if app.AppType == "cli" || app.AppType == "binary" {
				lookupKind = repository.ArtifactKindBinary
			}
		}
		artifact, aerr := a.Registry.Artifacts().GetArtifact(ctx, repository.ArtifactLookup{
			OwnerFullName:   t.OwnerFullName,
			Kind:            lookupKind,
			LatestPublished: true,
		})
		if aerr != nil {
			result.AllPublished = false
			if result.Failed == nil {
				result.Failed = map[string]string{}
			}
			result.Failed[key] = fmt.Sprintf("no published artifact found: %v", aerr)
			continue
		}
		if artifact.State != repository.ArtifactStatePublished {
			result.AllPublished = false
			if result.Failed == nil {
				result.Failed = map[string]string{}
			}
			result.Failed[key] = fmt.Sprintf("latest artifact is in state %q, not published", artifact.State)
			continue
		}
		if expectedVersion, ok := expectedVersions[key]; ok && artifact.Version != expectedVersion {
			result.AllPublished = false
			if result.Failed == nil {
				result.Failed = map[string]string{}
			}
			result.Failed[key] = fmt.Sprintf("published artifact version %q does not match expected version %q", artifact.Version, expectedVersion)
		}
	}
	return result, nil
}

// releaseRunTargetStateOrder is the legal linear progression
// repository.ReleaseRunTargetState's doc comment describes: queued ->
// building -> publishing -> recording -> succeeded. Used by
// RecordTargetState to walk from a target's current state up to a desired
// terminal state one legal transition at a time -- see that method's doc
// comment for why a single UpdateTargetState call straight from queued to
// succeeded is not legal (server/repository/postgres/release_run.go's
// legalReleaseRunTargetTransitions only allows adjacent steps, mirrored in
// server/repository/fake).
var releaseRunTargetStateOrder = []repository.ReleaseRunTargetState{
	repository.ReleaseRunTargetStateQueued,
	repository.ReleaseRunTargetStateBuilding,
	repository.ReleaseRunTargetStatePublishing,
	repository.ReleaseRunTargetStateRecording,
	repository.ReleaseRunTargetStateSucceeded,
}

// RecordTargetState implements ReleaseActivities.RecordTargetState (FR10,
// FR11/NFR3 idempotency). newState is the target's DESIRED final state for
// this workflow execution -- ReleaseWorkflow calls this activity exactly
// once per target, after VerifyPublished, with newState already resolved
// to Succeeded or Failed (see workflow.go's ReleaseWorkflow/recordFailure).
// Since repository.ReleaseRunRepository.UpdateTargetState enforces the
// adjacent-only legal-transition table, reaching Succeeded from Queued in
// one call requires walking every intermediate state
// (releaseRunTargetStateOrder) in order first; Failed is legal directly
// from any non-terminal state, so it skips the walk.
//
// Idempotent (NFR3): resolves the target's CURRENT state first and returns
// immediately (no-op) if it already equals newState, or if it is already
// terminal (Succeeded/Failed) at all -- covering both a redelivered
// activity retry after this exact write already landed, and (defensively)
// a second, contradictory final call, which should never happen within one
// workflow execution but must not crash the workflow if it somehow did.
func (a *Activities) RecordTargetState(ctx context.Context, releaseRunID string, target ReleaseTarget, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	if a.Registry == nil {
		return fmt.Errorf("record target state for release run %s: Activities.Registry not configured", releaseRunID)
	}
	_, targets, err := a.Registry.ReleaseRuns().GetReleaseRun(ctx, releaseRunID)
	if err != nil {
		return fmt.Errorf("record target state for release run %s: %w", releaseRunID, err)
	}

	var row *repository.ReleaseRunTarget
	for i := range targets {
		if targets[i].OwnerFullName == target.OwnerFullName && targets[i].Kind == target.Kind {
			row = &targets[i]
			break
		}
	}
	if row == nil {
		return fmt.Errorf("record target state for release run %s: no release_run_target row for %s", releaseRunID, target.key())
	}

	if row.State == newState || isTerminalState(row.State) {
		// Already where we want to be (retry redelivery), or already
		// settled to a different terminal state (defensive no-op -- see
		// doc comment above).
		return nil
	}

	repo := a.Registry.ReleaseRuns()

	if newState == repository.ReleaseRunTargetStateFailed {
		return repo.UpdateTargetState(ctx, row.ReleaseRunTargetID, repository.ReleaseRunTargetStateFailed, buildID, errorDetail)
	}

	startIdx := indexOfState(row.State)
	endIdx := indexOfState(newState)
	if startIdx < 0 || endIdx < 0 || endIdx < startIdx {
		return fmt.Errorf("record target state for release run %s: cannot walk %s -> %s for %s", releaseRunID, row.State, newState, target.key())
	}
	for i := startIdx + 1; i <= endIdx; i++ {
		step := releaseRunTargetStateOrder[i]
		stepBuildID, stepErrorDetail := "", ""
		if i == endIdx {
			// Only the final step in the walk carries buildID/errorDetail
			// -- UpdateTargetState's "empty string leaves the existing
			// value unchanged" contract means intermediate steps don't
			// need to repeat them.
			stepBuildID, stepErrorDetail = buildID, errorDetail
		}
		if err := repo.UpdateTargetState(ctx, row.ReleaseRunTargetID, step, stepBuildID, stepErrorDetail); err != nil {
			return fmt.Errorf("record target state for release run %s: transition %s -> %s for %s: %w", releaseRunID, row.State, step, target.key(), err)
		}
	}
	return nil
}

func isTerminalState(s repository.ReleaseRunTargetState) bool {
	return s == repository.ReleaseRunTargetStateSucceeded || s == repository.ReleaseRunTargetStateFailed
}

func indexOfState(s repository.ReleaseRunTargetState) int {
	for i, v := range releaseRunTargetStateOrder {
		if v == s {
			return i
		}
	}
	return -1
}
