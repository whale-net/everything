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

	stage, err := s.repo.DomainAdoption().GetStage(ctx, req.ChartDomain)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	// The per-domain gate every other AR-7 tightening uses (see
	// ARCHITECTURE.md "Rejected alternatives (issue #558)"): enforced only
	// once the chart's own domain has cut over to "allocate". At every other
	// stage this is a no-op response, not merely a response the caller is
	// expected to ignore -- callers must not fail a build on an unenforced
	// response, so violations is left nil rather than populated-but-ignored.
	if stage != repository.DomainAdoptionStageAllocate {
		return &pb.CheckChartHermeticityResponse{Enforced: false}, nil
	}

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
