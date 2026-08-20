package handlers

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
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
type ReleaseServer struct {
	pb.UnimplementedReleaseRegistryServer
	repo repository.Registry
}

// NewReleaseServer constructs a ReleaseServer over repo.
func NewReleaseServer(repo repository.Registry) *ReleaseServer {
	return &ReleaseServer{repo: repo}
}

// TriggerRelease dedup-checks the batch against any already-non-terminal
// release covering the same targets (FR5), persists the release_run/
// release_run_target rows via repo.ReleaseRuns().CreateReleaseRun, and
// returns the persisted ids. It deliberately does NOT resolve `all`/domain/
// comma-list scope syntax (that's the caller's job, done before this RPC is
// called -- see ReleaseTargetInput's doc comment) and does NOT itself
// resolve a version plan (FR7/FR8's "resolved once, reused everywhere"
// happens inside the Temporal ReleaseWorkflow -- issue #889 -- not here).
//
// TODO(#889): this does not yet start the Temporal ReleaseWorkflow. Once
// the workflow exists, TriggerRelease must: (1) derive the real
// deterministic workflow id coordinated with the workflow's own dedup key
// (replacing the release-<uuid> placeholder below, which is intentionally
// unique-per-call so CreateReleaseRun's TemporalWorkflowID uniqueness
// constraint never spuriously blocks a legitimate future release of the
// same targets), (2) start the workflow via the Temporal client (see
// worker/main.go's client construction pattern), and (3) translate a
// WorkflowExecutionAlreadyStarted error into the same FailedPrecondition
// "already releasing" response this handler's fast-fail check returns
// below -- that error is the authoritative NFR2 guarantee, this check is
// only a UX-quality fast-fail and can race.
func (s *ReleaseServer) TriggerRelease(ctx context.Context, req *pb.TriggerReleaseRequest) (*pb.TriggerReleaseResponse, error) {
	if err := auth.RequirePromoter(ctx, releaseTriggerEnv); err != nil {
		return nil, err
	}
	if len(req.GetTargets()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "targets is required")
	}

	targets := make([]repository.ReleaseRunTarget, 0, len(req.GetTargets()))
	digests := map[string]string{}
	// seen guards against the same owner+kind appearing twice within a
	// single TriggerRelease call. rejectIfAlreadyReleasing only checks
	// against already-*persisted* release_run_target rows, so two
	// occurrences of the same target within this one request would
	// otherwise both pass that check (neither is persisted yet) and
	// CreateReleaseRun would insert two 'queued' release_run_target rows
	// for the same owner+kind in the same release_run -- violating FR5's
	// "at most one non-terminal release per target" the instant the run is
	// created. Caught here as a request-shape error, not a repository
	// invariant, since no unique constraint enforces it at the DB layer
	// (see migration 016's release_run_target -- intentionally, per that
	// table's doc comment: CreateReleaseRun is trusted to insert at most
	// one row per target).
	seen := map[string]bool{}
	for _, t := range req.GetTargets() {
		if t.GetOwnerFullName() == "" {
			return nil, status.Error(codes.InvalidArgument, "targets[].owner_full_name is required")
		}
		kind := artifactKindFromPB(t.GetKind())
		if kind != repository.ArtifactKindImage && kind != repository.ArtifactKindChart {
			return nil, status.Errorf(codes.InvalidArgument, "targets[].kind must be IMAGE or CHART, got %v", t.GetKind())
		}
		key := t.GetOwnerFullName() + "\x00" + string(kind)
		if seen[key] {
			return nil, status.Errorf(codes.InvalidArgument, "targets[] contains %s (%s) more than once in the same request", t.GetOwnerFullName(), kind)
		}
		seen[key] = true

		if err := s.rejectIfAlreadyReleasing(ctx, t.GetOwnerFullName(), kind); err != nil {
			return nil, err
		}

		targets = append(targets, repository.ReleaseRunTarget{
			OwnerFullName: t.GetOwnerFullName(),
			Kind:          kind,
		})
		if t.GetDigest() != "" {
			digests[t.GetOwnerFullName()] = t.GetDigest()
		}
	}

	var digestInput []byte
	if len(digests) > 0 {
		raw, err := json.Marshal(digests)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "marshal digest input: %v", err)
		}
		digestInput = raw
	}

	run := repository.ReleaseRun{
		TriggeredBy:    actorFromCtx(ctx),
		RequestedScope: req.GetRequestedScope(),
		DigestInput:    digestInput,
		// TemporalWorkflowID: see TODO on this method's doc comment -- a
		// placeholder until #889 wires the real Temporal client and
		// coordinates a deterministic, batch-derived id.
		TemporalWorkflowID: "release-" + uuid.NewString(),
	}

	var created *repository.ReleaseRun
	if err := s.repo.WithTx(ctx, func(ctx context.Context, r repository.Registry) error {
		out, _, cerr := r.ReleaseRuns().CreateReleaseRun(ctx, run, targets)
		if cerr != nil {
			return cerr
		}
		created = out
		return nil
	}); err != nil {
		return nil, mapRepoErr(err)
	}

	return &pb.TriggerReleaseResponse{
		ReleaseRunId:       created.ReleaseRunID,
		TemporalWorkflowId: created.TemporalWorkflowID,
	}, nil
}

// rejectIfAlreadyReleasing implements FR5's fast-fail check: it returns
// codes.FailedPrecondition if ownerFullName+kind already has a
// release_run_target row in a non-terminal state on any release_run --
// ListReleaseRunsByTarget's doc comment is explicit that this method itself
// only narrows to "runs touching this owner"; filtering by target state is
// left to callers, which is what this does by re-reading each candidate
// run's targets via GetReleaseRun. This is a UX-quality check only (it can
// race under concurrent triggers) -- see this file's TODO(#889) for the
// authoritative guarantee.
func (s *ReleaseServer) rejectIfAlreadyReleasing(ctx context.Context, ownerFullName string, kind repository.ArtifactKind) error {
	runs, err := s.repo.ReleaseRuns().ListReleaseRunsByTarget(ctx, ownerFullName)
	if err != nil {
		return mapRepoErr(err)
	}
	for _, run := range runs {
		_, targets, gerr := s.repo.ReleaseRuns().GetReleaseRun(ctx, run.ReleaseRunID)
		if gerr != nil {
			return mapRepoErr(gerr)
		}
		for _, t := range targets {
			if t.OwnerFullName != ownerFullName || t.Kind != kind {
				continue
			}
			if !isTerminalReleaseRunTargetState(t.State) {
				return status.Errorf(codes.FailedPrecondition,
					"%s (%s) is already releasing under release_run %s (target state %q)",
					ownerFullName, kind, run.ReleaseRunID, t.State)
			}
		}
	}
	return nil
}

// isTerminalReleaseRunTargetState reports whether state is one of the two
// terminal states in ReleaseRunTargetState's legal-transition table
// (succeeded/failed) -- see repository/models.go's doc comment.
func isTerminalReleaseRunTargetState(state repository.ReleaseRunTargetState) bool {
	return state == repository.ReleaseRunTargetStateSucceeded || state == repository.ReleaseRunTargetStateFailed
}

// GetRelease reads straight from repo.ReleaseRuns().GetReleaseRun (FR10).
// Unauthenticated, matching every other read RPC in ARCHITECTURE.md's
// Authorization table (issue #853) -- no auth.Require* check here is
// deliberate, not an oversight.
func (s *ReleaseServer) GetRelease(ctx context.Context, req *pb.GetReleaseRequest) (*pb.GetReleaseResponse, error) {
	if req.GetReleaseRunId() == "" {
		return nil, status.Error(codes.InvalidArgument, "release_run_id is required")
	}
	run, targets, err := s.repo.ReleaseRuns().GetReleaseRun(ctx, req.GetReleaseRunId())
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return releaseRunToPB(*run, targets), nil
}

// ListReleases reads straight from
// repo.ReleaseRuns().ListReleaseRunsByTarget -- NFR4's full history,
// including prior (not just current) attempts per target. Unauthenticated,
// same reasoning as GetRelease above.
func (s *ReleaseServer) ListReleases(ctx context.Context, req *pb.ListReleasesRequest) (*pb.ListReleasesResponse, error) {
	if req.GetOwnerFullName() == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}
	runs, err := s.repo.ReleaseRuns().ListReleaseRunsByTarget(ctx, req.GetOwnerFullName())
	if err != nil {
		return nil, mapRepoErr(err)
	}
	out := make([]*pb.GetReleaseResponse, 0, len(runs))
	for _, run := range runs {
		_, targets, gerr := s.repo.ReleaseRuns().GetReleaseRun(ctx, run.ReleaseRunID)
		if gerr != nil {
			return nil, mapRepoErr(gerr)
		}
		out = append(out, releaseRunToPB(run, targets))
	}
	return &pb.ListReleasesResponse{Releases: out}, nil
}

// releaseRunToPB assembles a GetReleaseResponse from a repository.ReleaseRun
// and its release_run_target rows. Shared by GetRelease and ListReleases so
// both read the exact same shape (NFR4).
func releaseRunToPB(run repository.ReleaseRun, targets []repository.ReleaseRunTarget) *pb.GetReleaseResponse {
	pbTargets := make([]*pb.ReleaseRunTarget, 0, len(targets))
	for _, t := range targets {
		pbTargets = append(pbTargets, releaseRunTargetToPB(t))
	}
	return &pb.GetReleaseResponse{
		ReleaseRunId:       run.ReleaseRunID,
		TriggeredBy:        run.TriggeredBy,
		RequestedScope:     run.RequestedScope,
		ResolvedPlanJson:   string(run.ResolvedPlan),
		Targets:            pbTargets,
		TemporalWorkflowId: run.TemporalWorkflowID,
	}
}

func releaseRunTargetToPB(t repository.ReleaseRunTarget) *pb.ReleaseRunTarget {
	return &pb.ReleaseRunTarget{
		OwnerFullName:  t.OwnerFullName,
		Kind:           artifactKindToPB(t.Kind),
		State:          releaseRunTargetStateToPB(t.State),
		StateChangedAt: timeToUnix(t.StateChangedAt),
		BuildId:        t.BuildID,
		ErrorDetail:    t.ErrorDetail,
	}
}

func releaseRunTargetStateToPB(s repository.ReleaseRunTargetState) pb.ReleaseRunTargetState {
	switch s {
	case repository.ReleaseRunTargetStateQueued:
		return pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_QUEUED
	case repository.ReleaseRunTargetStateBuilding:
		return pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING
	case repository.ReleaseRunTargetStatePublishing:
		return pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_PUBLISHING
	case repository.ReleaseRunTargetStateRecording:
		return pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_RECORDING
	case repository.ReleaseRunTargetStateSucceeded:
		return pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED
	case repository.ReleaseRunTargetStateFailed:
		return pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED
	default:
		return pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_UNSPECIFIED
	}
}
