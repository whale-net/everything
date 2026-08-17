package repository

import (
	"testing"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

func TestDerivePromotability(t *testing.T) {
	tests := []struct {
		name            string
		kind            ArtifactKind
		ownerDeployUnit appmetapb.DeployUnit
		want            Promotability
	}{
		// Binary artifacts are always promotable, regardless of owner deploy
		// unit -- including DEPLOY_UNIT_NONE, which is how release tool
		// binaries (release_helper_go, app-registry CLI) are packaged to
		// keep them out of Helm/K8s composition (#534/NFR-4, #780).
		{"binary/deploy_unit_none", ArtifactKindBinary, appmetapb.DeployUnit_DEPLOY_UNIT_NONE, PromotabilityPromotable},
		{"binary/deploy_unit_unspecified", ArtifactKindBinary, appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED, PromotabilityPromotable},
		{"binary/deploy_unit_image", ArtifactKindBinary, appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE, PromotabilityPromotable},
		{"binary/deploy_unit_chart", ArtifactKindBinary, appmetapb.DeployUnit_DEPLOY_UNIT_CHART, PromotabilityPromotable},

		// Firmware is never promotable, regardless of owner deploy unit
		// (regression check -- must not be affected by the binary change).
		{"firmware/deploy_unit_none", ArtifactKindFirmware, appmetapb.DeployUnit_DEPLOY_UNIT_NONE, PromotabilityNotPromotable},
		{"firmware/deploy_unit_image", ArtifactKindFirmware, appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE, PromotabilityNotPromotable},
		{"firmware/deploy_unit_chart", ArtifactKindFirmware, appmetapb.DeployUnit_DEPLOY_UNIT_CHART, PromotabilityNotPromotable},

		// Image artifacts: PROMOTABLE under an image-deploy-unit owner,
		// VIA_CHART under a chart-deploy-unit owner, NOT_PROMOTABLE otherwise.
		{"image/deploy_unit_image", ArtifactKindImage, appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE, PromotabilityPromotable},
		{"image/deploy_unit_chart", ArtifactKindImage, appmetapb.DeployUnit_DEPLOY_UNIT_CHART, PromotabilityViaChart},
		{"image/deploy_unit_none", ArtifactKindImage, appmetapb.DeployUnit_DEPLOY_UNIT_NONE, PromotabilityNotPromotable},

		// Chart artifacts: PROMOTABLE only under a chart-deploy-unit owner.
		{"chart/deploy_unit_chart", ArtifactKindChart, appmetapb.DeployUnit_DEPLOY_UNIT_CHART, PromotabilityPromotable},
		{"chart/deploy_unit_image", ArtifactKindChart, appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE, PromotabilityPromotable},
		{"chart/deploy_unit_none", ArtifactKindChart, appmetapb.DeployUnit_DEPLOY_UNIT_NONE, PromotabilityNotPromotable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerivePromotability(tt.kind, tt.ownerDeployUnit)
			if got != tt.want {
				t.Errorf("DerivePromotability(%v, %v) = %v, want %v", tt.kind, tt.ownerDeployUnit, got, tt.want)
			}
		})
	}
}
