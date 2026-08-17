package repository

import appmetapb "github.com/whale-net/everything/tools/appmeta/proto"

// DerivePromotability implements ARCHITECTURE.md "Promotability". ownerDeployUnit
// is the DeployUnit of the App (for an image, binary, or firmware artifact) or
// Chart (for a chart artifact) that owns the artifact — never a value stored on
// the artifact itself. Shared by every repository implementation so the rule has
// exactly one definition.
//
//	App/Chart deploy_unit | Image artifacts | Chart artifacts | Binary artifacts | Firmware
//	chart                 | VIA_CHART       | PROMOTABLE      | NOT_PROMOTABLE   | NOT_PROMOTABLE
//	image                 | PROMOTABLE      | n/a             | PROMOTABLE       | NOT_PROMOTABLE
//	none                  | NOT_PROMOTABLE  | n/a             | PROMOTABLE       | NOT_PROMOTABLE
//
// Binary artifacts are PROMOTABLE regardless of ownerDeployUnit: tool binaries
// (release_helper_go, the app-registry CLI) are deliberately packaged with
// DEPLOY_UNIT_NONE to keep them out of Helm/K8s chart composition (#534/NFR-4),
// but that isolation is a composer.go concern, not a promotion-registry one --
// see #780. composer.go must keep ignoring binaries independently of this.
func DerivePromotability(kind ArtifactKind, ownerDeployUnit appmetapb.DeployUnit) Promotability {
	if kind == ArtifactKindFirmware {
		return PromotabilityNotPromotable
	}
	if kind == ArtifactKindBinary {
		return PromotabilityPromotable
	}
	switch ownerDeployUnit {
	case appmetapb.DeployUnit_DEPLOY_UNIT_CHART:
		if kind == ArtifactKindImage {
			return PromotabilityViaChart
		}
		if kind == ArtifactKindChart {
			return PromotabilityPromotable
		}
		return PromotabilityNotPromotable
	case appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE:
		return PromotabilityPromotable
	default: // DEPLOY_UNIT_NONE, DEPLOY_UNIT_UNSPECIFIED
		return PromotabilityNotPromotable
	}
}
