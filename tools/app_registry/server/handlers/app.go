// Package handlers implements the four App Registry gRPC services. Each
// file is a thin adapter — validation and delegation to repository/ — never
// business logic itself; see ../README.md "Conventions".
package handlers

import (
	"context"
	"log/slog"

	"github.com/whale-net/everything/libs/go/logging"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// AppServer implements pb.AppRegistryServer, backed by repository.Registry.
type AppServer struct {
	pb.UnimplementedAppRegistryServer
	repo repository.Registry
}

// NewAppServer constructs an AppServer over repo.
func NewAppServer(repo repository.Registry) *AppServer {
	return &AppServer{repo: repo}
}

// appLog is this file's logger name, deliberately its own name (not
// "handlers") so a skipped-stale reconcile is easy to filter for -- see
// ReconcileApps's use below.
var appLog = logging.Get("app-registry-handlers")

func (s *AppServer) ReconcileApps(ctx context.Context, req *pb.ReconcileAppsRequest) (*pb.ReconcileAppsResponse, error) {
	if err := auth.Require(ctx, auth.RoleBuilder); err != nil {
		return nil, err
	}
	if req.Manifests == nil {
		return nil, status.Error(codes.InvalidArgument, "manifests is required")
	}

	// source is the ordering metadata Reconcile checks against the
	// reconcile watermark -- see ARCHITECTURE.md "Reconcile watermark" and
	// issue #545. Passed through unconditionally, including on the dry_run
	// path below: Reconcile itself ignores it entirely when dryRun is true
	// (see repository.AppRepository.Reconcile's doc comment), so there is
	// no branch to get wrong here.
	source := repository.ReconcileSource{
		GitSHA:            req.Manifests.GitSha,
		SourceCommittedAt: req.Manifests.SourceCommittedAt,
		DiscoveredAt:      req.Manifests.DiscoveredAt,
	}

	// dry_run computes a diff and writes nothing: it never touches the
	// idempotency store either (there is nothing for a retry to replay
	// safely — every dry run recomputes against current state, which is the
	// whole point).
	if req.DryRun {
		result, err := s.repo.Apps().Reconcile(ctx, req.Manifests.Apps, req.Manifests.Charts, source, true)
		if err != nil {
			return nil, mapRepoErr(err)
		}
		return reconcileResultToPB(result, true), nil
	}

	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	resp, _, err := runIdempotent(ctx, s.repo, req.IdempotencyKey, "ReconcileApps",
		func() proto.Message { return &pb.ReconcileAppsResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			result, err := r.Apps().Reconcile(ctx, req.Manifests.Apps, req.Manifests.Charts, source, false)
			if err != nil {
				return nil, err
			}
			if result.SkippedStale {
				// Warn, not Debug/Info: a stale reconcile being skipped is
				// a no-op SUCCESS (see ReconcileAppsResponse.skipped_stale's
				// doc comment for why it must not be an error), but a
				// silent no-op is exactly the failure mode this whole
				// feature exists to make detectable after the fact. This
				// only fires when Reconcile actually ran, not on an
				// idempotency-key replay of a previously stored response.
				appLog.Warn("reconcile skipped: stale relative to current watermark",
					slog.String("incoming_git_sha", source.GitSHA),
					slog.Int64("incoming_source_committed_at", source.SourceCommittedAt),
					slog.Int64("incoming_discovered_at", source.DiscoveredAt),
					slog.String("current_watermark_git_sha", result.CurrentWatermarkGitSHA),
				)
			}
			return reconcileResultToPB(result, false), nil
		},
	)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return resp.(*pb.ReconcileAppsResponse), nil
}

func reconcileResultToPB(r *repository.ReconcileResult, dryRun bool) *pb.ReconcileAppsResponse {
	return &pb.ReconcileAppsResponse{
		CreatedApps:            appsToPB(r.CreatedApps),
		UpdatedApps:            appsToPB(r.UpdatedApps),
		NewlyMissingApps:       appsToPB(r.NewlyMissingApps),
		RecoveredApps:          appsToPB(r.RecoveredApps),
		CreatedCharts:          chartsToPB(r.CreatedCharts),
		UpdatedCharts:          chartsToPB(r.UpdatedCharts),
		NewlyMissingCharts:     chartsToPB(r.NewlyMissingCharts),
		RecoveredCharts:        chartsToPB(r.RecoveredCharts),
		DryRun:                 dryRun,
		SkippedStale:           r.SkippedStale,
		CurrentWatermarkGitSha: r.CurrentWatermarkGitSHA,
	}
}

func (s *AppServer) ListApps(ctx context.Context, req *pb.ListAppsRequest) (*pb.ListAppsResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	apps, err := s.repo.Apps().ListApps(ctx, repository.AppListFilter{
		Domain:     req.Domain,
		Statuses:   appStatusesFromPB(req.Statuses),
		DeployUnit: req.DeployUnit,
	})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListAppsResponse{
		Apps: appsToPB(apps),
		Page: &pb.PageResponse{TotalSize: int32(len(apps))},
	}, nil
}

func (s *AppServer) GetApp(ctx context.Context, req *pb.GetAppRequest) (*pb.GetAppResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	app, err := s.lookupApp(ctx, req.AppId, req.FullName)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	charts, err := s.repo.Apps().ChartsForApp(ctx, app.AppID)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.GetAppResponse{
		App:    appToPB(*app),
		Charts: chartsToPB(charts),
	}, nil
}

func (s *AppServer) lookupApp(ctx context.Context, appID, fullName string) (*repository.App, error) {
	switch {
	case appID != "":
		return s.repo.Apps().GetAppByID(ctx, appID)
	case fullName != "":
		return s.repo.Apps().GetAppByFullName(ctx, fullName)
	default:
		return nil, status.Error(codes.InvalidArgument, "app_id or full_name is required")
	}
}

func (s *AppServer) ListCharts(ctx context.Context, req *pb.ListChartsRequest) (*pb.ListChartsResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	charts, err := s.repo.Apps().ListCharts(ctx, repository.ChartListFilter{
		Domain:   req.Domain,
		Statuses: appStatusesFromPB(req.Statuses),
	})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListChartsResponse{
		Charts: chartsToPB(charts),
		Page:   &pb.PageResponse{TotalSize: int32(len(charts))},
	}, nil
}

// SetAppStatus is the human-triage path and supports exactly one transition:
// missing -> archived. Reconcile already handles missing -> active
// automatically via its "recovered" path, so ACTIVE is not a legal target
// here — see ARCHITECTURE.md "Triage".
func (s *AppServer) SetAppStatus(ctx context.Context, req *pb.SetAppStatusRequest) (*pb.SetAppStatusResponse, error) {
	if err := auth.Require(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	if req.AppId == "" {
		return nil, status.Error(codes.InvalidArgument, "app_id is required")
	}
	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	if appStatusFromPB(req.Status) != repository.StatusArchived {
		return nil, status.Error(codes.InvalidArgument, "status must be ARCHIVED; missing -> active happens automatically via reconcile")
	}

	app, err := s.repo.Apps().SetAppStatus(ctx, req.AppId, repository.StatusArchived, req.Reason)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.SetAppStatusResponse{App: appToPB(*app)}, nil
}
