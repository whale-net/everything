package handlers

import (
	"context"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// releaseTriggerEnv is the environment key server/auth.RequirePromoter is
// checked against for ReleaseRegistry writes. TriggerRelease has no
// environment field of its own -- releasing builds/publishes artifacts, it
// does not deploy to an environment -- so FR4/NFR5's "reuse the exact
// permission Promote already requires, no new role" is applied against the
// lowest-rank promoter role every promoter principal (dev/stage/prod, human
// or service) is expected to hold. See ARCHITECTURE.md "Authorization" and
// issue #888's Auth scope note.
const releaseTriggerEnv = "dev"

// ReleaseServer implements pb.ReleaseRegistryServer, backed by
// repository.Registry's ReleaseRunRepository (release_run/
// release_run_target, migration 016, issue #887). See
// ARCHITECTURE.md "The API is the write path" -- this is the seam both the
// admin UI and the Temporal ReleaseWorkflow call through; neither talks to
// Postgres directly.
//
// Scaffold (issue #888): every RPC enforces auth.RequirePromoter identically
// to PromotionRegistry.Promote, then returns codes.Unimplemented. Business
// logic (dedup-check, CreateReleaseRun, starting the Temporal workflow, and
// the GetRelease/ListReleases read paths) lands in the Implementation phase.
type ReleaseServer struct {
	pb.UnimplementedReleaseRegistryServer
	repo repository.Registry
}

// NewReleaseServer constructs a ReleaseServer over repo.
func NewReleaseServer(repo repository.Registry) *ReleaseServer {
	return &ReleaseServer{repo: repo}
}

// TriggerRelease will dedup-check the batch against any already-non-terminal
// release covering the same targets (FR5), persist the release_run/
// release_run_target rows via repo.ReleaseRuns().CreateReleaseRun, and start
// the Temporal ReleaseWorkflow. Not yet implemented -- see this type's doc
// comment.
func (s *ReleaseServer) TriggerRelease(ctx context.Context, req *pb.TriggerReleaseRequest) (*pb.TriggerReleaseResponse, error) {
	if err := auth.RequirePromoter(ctx, releaseTriggerEnv); err != nil {
		return nil, err
	}
	if len(req.GetTargets()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "targets is required")
	}
	return nil, status.Error(codes.Unimplemented, "TriggerRelease is not yet implemented")
}

// GetRelease will read straight from repo.ReleaseRuns().GetReleaseRun. Not
// yet implemented -- see this type's doc comment. Unauthenticated, matching
// every other read RPC in ARCHITECTURE.md's Authorization table (issue
// #853) -- no auth.Require* check here is deliberate, not an oversight.
func (s *ReleaseServer) GetRelease(ctx context.Context, req *pb.GetReleaseRequest) (*pb.GetReleaseResponse, error) {
	if req.GetReleaseRunId() == "" {
		return nil, status.Error(codes.InvalidArgument, "release_run_id is required")
	}
	return nil, status.Error(codes.Unimplemented, "GetRelease is not yet implemented")
}

// ListReleases will read straight from
// repo.ReleaseRuns().ListReleaseRunsByTarget -- NFR4's full history,
// including prior (not just current) attempts per target. Not yet
// implemented -- see this type's doc comment. Unauthenticated, same
// reasoning as GetRelease above.
func (s *ReleaseServer) ListReleases(ctx context.Context, req *pb.ListReleasesRequest) (*pb.ListReleasesResponse, error) {
	if req.GetOwnerFullName() == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}
	return nil, status.Error(codes.Unimplemented, "ListReleases is not yet implemented")
}
