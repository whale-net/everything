package matrix

import (
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// AppCatalogRow is one row of the apps catalog (screen 20, FR-22): either a
// Chart or a bare App, each with its domain, deploy-unit and one Cell per
// environment. Unlike Row/Build, this is deliberately a FLAT list — the
// catalog names deploy unit per row instead of nesting VIA_CHART apps under
// their owning chart, and (unlike Build) it also includes DEPLOY_UNIT_NONE
// apps, which Build skips entirely since they're never PROMOTABLE. Built by
// main's buildAppsCatalog (which does the gRPC calls); this package only
// hosts the shape so pages can import it without importing main.
type AppCatalogRow struct {
	IsChart  bool
	ID       string // chart_id or app_id
	Domain   string
	FullName string
	// DeployUnit is only meaningful when !IsChart — chart rows are always
	// themselves the promotable unit ("chart" badge).
	DeployUnit appmetapb.DeployUnit

	Cells []*Cell // aligned index-for-index with environments
}

// AppEnvState is one environment's effective state for one app on the app
// detail screen (11): what's actually running there, and how it got there
// (direct promotion vs. riding its owning chart's pin) — the "Chart pin
// says … (mismatch)" drift signal the wireframe calls out inline rather
// than only on the environment list.
type AppEnvState struct {
	Env *pb.Environment

	ColumnErr error

	// Promoted is false when neither a direct promotion of this app nor (for
	// a VIA_CHART app) its owning chart's pin puts anything in this
	// environment — the FR-19/NFR-6 "not promoted" state, never rendered as
	// blank.
	Promoted bool

	// ViaChart is true when this environment's version comes from the
	// owning chart's pin rather than a direct promotion of this app.
	ViaChart bool
	// Override is true when this app was promoted directly, bypassing its
	// owning chart (Promotion.is_override) — only possible for a
	// DEPLOY_UNIT_CHART app.
	Override bool

	Artifact *pb.Artifact // set when Promoted && !ViaChart (a direct promotion carries a full Artifact record)
	Version  string
	Digest   string

	// Drifted/ChartPinnedDigest mirror the owning chart's DriftEntry for
	// this app_id, if any — set only when Override is true and the running
	// digest no longer matches the chart's pin (FR-9's per-app drift
	// signal, surfaced inline per the wireframe note).
	Drifted           bool
	ChartPinnedDigest string
}

// AppDetailData is everything screen 11 (app detail) needs beyond the App
// record itself.
type AppDetailData struct {
	App *pb.App

	// OwningChart is set only when App.deploy_unit == DEPLOY_UNIT_CHART —
	// the chart that publishes this app (FR-25), resolved from GetApp's own
	// charts field (the chart's DECLARED composition per ListCharts.app_ids
	// — a different question from a build-time artifact pin, per FR-25's
	// caveat). Nil when this app has no resolved owning chart (e.g. a
	// dangling composition reference).
	OwningChart *pb.Chart

	States []AppEnvState // aligned with Environments

	// LatestArtifact is the most recently published artifact recorded for
	// this app (any kind -- see releaseTargetKindFromApp), independent of
	// whether it is promoted anywhere (#774). Shown even for a
	// non-deployable (build-only) app -- see IsDeployable -- and, for a
	// deployable app, prepended ahead of the real per-environment States
	// as a synthetic "lower rank than dev" column/row so latest-vs-prod is
	// eyeballable at a glance (States/environments are already
	// rank-ascending, per ListEnvironments). Nil when ListArtifacts found
	// no published artifact yet.
	LatestArtifact *pb.Artifact

	Events    []*pb.PromotionEvent
	EventsErr error

	// SyncOutcomes is Story 3's inline sync/health badge data (issue
	// #1032), keyed by promotion_id -- one entry per Events row that
	// resolved successfully via fetchPromotionSyncOutcomes'
	// best-effort fanout. A missing entry (failed lookup, or the
	// worker hasn't observed any ArgoCD sync/health state for that
	// promotion yet) means promotionTimelineCard omits the badge for
	// that row entirely, never rendering a false "Unknown".
	SyncOutcomes map[string]*pb.PromotionDetails
}
