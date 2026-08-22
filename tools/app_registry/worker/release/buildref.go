// buildref.go implements FR9/FR10's watermark-free release ref resolution:
// given a release target, resolve app_build_log's current-pointer row
// (repository.AppBuildLogRepository.GetCurrentBuildLog) to a commit SHA,
// falling back to a literal branch name when no current row exists yet.
//
// Wired into DispatchBuild via resolveDispatchRef below, which forwards the
// result as release-v2.yml's `build_ref` workflow input (activities.go) --
// NOT GitHubDispatcher.Dispatch's `ref` parameter, since GitHub's
// workflow_dispatch API rejects a commit SHA there (see github.go's
// Dispatch doc comment). release-v2.yml checks out `build_ref` explicitly
// in each job that needs the pinned commit.
package release

import (
	"context"
	"errors"
	"fmt"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// resolveBuildRef implements FR9/FR10 for a single release target: resolve
// ownerFullName/kind to an owner_id (App/Chart identity lookup), then
// app_build_log's current-pointer row for that owner_id. Returns
// fallbackRef, not an error, when no current app_build_log row exists yet
// (FR10 -- a fresh environment, or an app added in a commit reconcile
// hasn't processed yet) so a missing app_build_log row never fails the
// release.
func resolveBuildRef(ctx context.Context, reg repository.Registry, ownerFullName string, kind repository.ArtifactKind, fallbackRef string) (string, error) {
	ownerID, err := resolveBuildLogOwnerID(ctx, reg, ownerFullName, kind)
	if err != nil {
		return "", err
	}

	current, err := reg.AppBuildLogs().GetCurrentBuildLog(ctx, ownerID)
	if errors.Is(err, repository.ErrNotFound) {
		return fallbackRef, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve build ref for %s: %w", ownerFullName, err)
	}
	return current.GitSHA, nil
}

// resolveBuildLogOwnerID resolves ownerFullName/kind to the app_id/chart_id
// app_build_log rows key on -- mirroring recordAppManifestSweep's owner
// resolution (postgres/app.go), but read-only and against
// AppRepository.GetAppByFullName/GetChartByFullName rather than a
// reconcile write.
// resolveDispatchRef implements FR9-FR11 for a whole DispatchBuild batch:
// resolves each target's build ref (resolveBuildRef, FR9/FR10, using
// fallbackRef for any target with no current app_build_log row), then
// requires the batch to agree on a single ref before returning it.
//
// The GitHub Actions workflow_dispatch API accepts exactly one `ref` per
// call, not a per-target map, so a batch with genuinely divergent
// per-target resolved refs cannot be dispatched atomically against all of
// them at once -- this mirrors uniformVersion's existing version-
// uniformity limitation (github.go's package doc comment,
// "DispatchBuild's version-uniformity limitation") for the identical
// underlying reason. A divergent batch is rejected with a clear error
// rather than silently picking one ref and building the rest from the
// wrong commit -- accepted follow-up, same limitation class as
// uniformVersion, not fixed by this task.
func resolveDispatchRef(ctx context.Context, reg repository.Registry, versions map[string]string, fallbackRef string) (string, error) {
	var resolved string
	for key := range versions {
		kind, owner, ok := parseTargetKey(key)
		if !ok {
			return "", fmt.Errorf("resolve dispatch ref: malformed resolved-plan target key %q", key)
		}
		ref, err := resolveBuildRef(ctx, reg, owner, kind, fallbackRef)
		if err != nil {
			return "", err
		}
		if resolved == "" {
			resolved = ref
			continue
		}
		if ref != resolved {
			return "", fmt.Errorf("resolve dispatch ref: resolved plan has heterogeneous build refs (%q vs %q) across targets -- DispatchBuild requires a single uniform ref across the batch, mirroring uniformVersion's limitation (see github.go's package doc comment)", resolved, ref)
		}
	}
	if resolved == "" {
		return "", fmt.Errorf("resolve dispatch ref: resolved plan has no versions")
	}
	return resolved, nil
}

func resolveBuildLogOwnerID(ctx context.Context, reg repository.Registry, ownerFullName string, kind repository.ArtifactKind) (string, error) {
	switch kind {
	case repository.ArtifactKindImage:
		app, err := reg.Apps().GetAppByFullName(ctx, ownerFullName)
		if err != nil {
			return "", fmt.Errorf("resolve build ref: lookup app %s: %w", ownerFullName, err)
		}
		return app.AppID, nil
	case repository.ArtifactKindChart:
		chart, err := reg.Apps().GetChartByFullName(ctx, ownerFullName)
		if err != nil {
			return "", fmt.Errorf("resolve build ref: lookup chart %s: %w", ownerFullName, err)
		}
		return chart.ChartID, nil
	default:
		return "", fmt.Errorf("resolve build ref: unsupported kind %q for %s", kind, ownerFullName)
	}
}
