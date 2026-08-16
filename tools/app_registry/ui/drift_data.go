package main

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/matrix"
)

// DriftAuditData is everything screen 40 (drift & audit, #650) needs: the
// FR-32 drift list (a client-side rollup of the same Matrix the dashboard
// and deployments screens read, so the three screens can never disagree —
// FR-9 consistency) and, separately, the FR-32 adopted-artifacts list
// (ListArtifacts(provenance=ADOPTED), optionally scoped to one owner). The
// two are deliberately never merged into one table — "an override no
// longer matching its chart's pin" and "a version a human asserted" are
// different questions.
type DriftAuditData struct {
	Matrix    *matrix.Matrix
	DriftRows []matrix.DriftRow

	AdoptedArtifacts []*pb.Artifact
	AdoptedErr       error
}

// buildDriftAudit loads screen 40's data. ownerFullName, when non-empty,
// scopes the adopted-artifacts list to one app/chart (FR-32's "optionally
// scoped to one app"); it does not affect the drift list, which is always
// cross-environment/cross-app by definition (FR-32).
func (app *App) buildDriftAudit(ctx context.Context, ownerFullName string) (*DriftAuditData, error) {
	m, err := app.buildDeploymentMatrix(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("buildDeploymentMatrix: %w", err)
	}

	data := &DriftAuditData{
		Matrix:    m,
		DriftRows: m.DriftRows(),
	}

	adoptedResp, err := app.registry.Artifact.ListArtifacts(ctx, &pb.ListArtifactsRequest{
		OwnerFullName: ownerFullName,
		Provenance:    pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED,
	})
	if err != nil {
		// A failed adopted-artifacts read must not blank the whole screen
		// (NFR-6) — the drift list above is independently useful, and the
		// adopted section renders its own explicit error card.
		data.AdoptedErr = err
	} else {
		data.AdoptedArtifacts = adoptedResp.GetArtifacts()
	}

	return data, nil
}
