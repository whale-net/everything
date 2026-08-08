package repository

import appmetapb "github.com/whale-net/everything/tools/appmeta/proto"

// DerivePromotability implements ARCHITECTURE.md "Promotability". ownerDeployUnit
// is the DeployUnit of the App (for an image artifact) or Chart (for a chart
// artifact) that owns the artifact — never a value stored on the artifact
// itself. Shared by every repository implementation so the rule has exactly
// one definition.
//
//	App/Chart deploy_unit | Image artifacts | Chart artifacts
//	chart                 | VIA_CHART       | PROMOTABLE
//	image                 | PROMOTABLE      | n/a (charts don't have deploy_unit=image)
//	none                  | NOT_PROMOTABLE  | n/a
func DerivePromotability(kind ArtifactKind, ownerDeployUnit appmetapb.DeployUnit) Promotability {
	switch ownerDeployUnit {
	case appmetapb.DeployUnit_DEPLOY_UNIT_CHART:
		if kind == ArtifactKindImage {
			return PromotabilityViaChart
		}
		return PromotabilityPromotable
	case appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE:
		return PromotabilityPromotable
	default: // DEPLOY_UNIT_NONE, DEPLOY_UNIT_UNSPECIFIED
		return PromotabilityNotPromotable
	}
}
