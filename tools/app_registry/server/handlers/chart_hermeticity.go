package handlers

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckChartHermeticity implements the AR-7f (issue #558) compose-time gate:
// "a chart may only pin images the registry has published," checked before
// release_helper_go's build-helm-chart packages the chart -- see
// ARCHITECTURE.md "Compose-time chart hermeticity" and PLAN.md's AR-7f
// subsection for the full contract.
//
// Deliberately kept in its own file rather than artifact.go, despite living
// on ArtifactRegistry and despite server/README.md's "one file per service"
// convention: AR-7e (AdoptArtifact) is being implemented in parallel on a
// sibling branch that also touches artifact.go, and this file's only shared
// surface with that work is the repository.Registry interface, not any line
// of artifact.go itself -- see PLAN.md AR-7f's "Constraints" note.
func (s *ArtifactServer) CheckChartHermeticity(ctx context.Context, req *pb.CheckChartHermeticityRequest) (*pb.CheckChartHermeticityResponse, error) {
	if req.ChartDomain == "" {
		return nil, status.Error(codes.InvalidArgument, "chart_domain is required")
	}

	// Enforced unconditionally for every domain — there is no per-domain
	// gate (see ARCHITECTURE.md "Rejected alternatives (issue #558)" for
	// the historical rationale for why this was ever per-domain). Enforced
	// is kept on the response (always true) rather than removed, so
	// callers don't need a lockstep
	// proto change.
	var violations []*pb.ChartPinViolation
	for _, pin := range req.Pins {
		artifact, err := s.repo.Artifacts().GetArtifact(ctx, repository.ArtifactLookup{
			OwnerFullName: pin.AppFullName,
			Kind:          repository.ArtifactKindImage,
			Version:       pin.Version,
		})
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				violations = append(violations, &pb.ChartPinViolation{
					AppFullName: pin.AppFullName,
					Version:     pin.Version,
					Reason:      "not recorded in App Registry",
				})
				continue
			}
			return nil, mapRepoErr(err)
		}
		if artifact.State != repository.ArtifactStatePublished {
			violations = append(violations, &pb.ChartPinViolation{
				AppFullName: pin.AppFullName,
				Version:     pin.Version,
				Reason:      fmt.Sprintf("state=%s, not published", artifact.State),
			})
		}
	}

	return &pb.CheckChartHermeticityResponse{Enforced: true, Violations: violations}, nil
}
