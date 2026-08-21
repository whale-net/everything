// buildref.go implements FR9/FR10's watermark-free release ref resolution
// read-side call site skeleton (issue #923, Scaffold phase): given a
// release target, resolve app_build_log's current-pointer row
// (repository.AppBuildLogRepository.GetCurrentBuildLog) to a commit SHA,
// falling back to a literal branch name when no current row exists yet.
//
// NOT YET WIRED into DispatchBuild/GitHubDispatcher.Dispatch's `ref`
// parameter (see github.go's GitHubDispatcherConfig.Ref, currently a
// static field defaulting to "main") -- that thread-through, including the
// per-Dispatch()-call-parameter-vs-static-field design decision FR11
// calls out, is Implementation-phase work for issue #923. This file is the
// read-side skeleton Scaffold adds; activities.go's DispatchBuild has a
// TODO marking where it will be called from.
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
