// Package viewdata holds the data shapes screens 12 (environment diff), 13
// (app version history), 21 (artifact detail), and 22 (chart detail) need
// beyond the raw proto messages — the same split matrix package already
// established for screens 01/10/11/20 (see matrix.AppDetailData's doc
// comment): pure data shaping only, no gRPC calls of its own, so both main
// (which does the fetching) and pages (which renders) can import it without
// an import cycle between them.
package viewdata

import (
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// ============================================================================
// Screen 13: app version history (FR-21)
// ============================================================================

// LiveEnvironment names one environment an artifact digest is currently live
// in — the "Currently live in" column's anchor back to the promotion
// timeline (the wireframe note: this screen answers "what versions exist",
// never "what's promoted where"). Override mirrors Promotion.is_override so
// the row can carry the same "override" tag the deployments matrix and app
// detail already use.
type LiveEnvironment struct {
	EnvKey   string
	Override bool
}

// AppHistoryArtifact is one row: every artifact ever recorded for the app,
// independent of promotion state. Build is nil when the owning build
// couldn't be resolved (best-effort enrichment, not fatal to the row).
type AppHistoryArtifact struct {
	Artifact *pb.Artifact
	Build    *pb.Build
	LiveIn   []LiveEnvironment
}

// AppHistoryData is everything screen 13 needs. ProvenanceFilter/LiveOnly
// echo the request's own filters back so the template can pre-select them.
type AppHistoryData struct {
	App              *pb.App
	Artifacts        []AppHistoryArtifact
	ProvenanceFilter string // "" (all) | "observed" | "adopted"
	LiveOnly         bool
}

// ============================================================================
// Screen 12: environment diff (FR-20)
// ============================================================================

// DiffStatus is one diff row's visual classification. Deliberately gives
// "from ahead" and "to ahead" equal visual weight (see
// components/badges.templ's NFR-6 precedent) — the diff never assumes
// either side is the "healthy" direction, per the wireframe's own note.
type DiffStatus int

const (
	DiffInSync DiffStatus = iota
	DiffFromOnly
	DiffToOnly
	DiffFromAhead
	DiffToAhead
	// DiffMismatch is used when both sides have a version but it can't be
	// ordered by semver (e.g. either side failed to parse as v<maj>.<min>.<patch>)
	// — never guess a direction that isn't backed by an actual comparison.
	DiffMismatch
)

// DiffRow is one entity (app or chart) present in either environment.
type DiffRow struct {
	FullName string
	IsChart  bool

	FromVersion, FromDigest string
	ToVersion, ToDigest     string
	FromProvenance          pb.ArtifactProvenance
	ToProvenance            pb.ArtifactProvenance

	Status DiffStatus
}

// EnvironmentDiffData is everything screen 12 needs. FromErr/ToErr mirror
// matrix.ColumnResult.Err's NFR-6 contract: a failed side must render as an
// explicit error, never as "nothing there" (which would misleadingly read
// as an empty, in-sync environment).
type EnvironmentDiffData struct {
	From, To *pb.Environment
	AsOf     int64

	FromErr, ToErr error

	Rows []DiffRow
}

// ============================================================================
// Screen 22: chart detail (FR-24/FR-25)
// ============================================================================

// PinnedImage is one app image a chart version pins (an artifact_link row,
// resolved to a display-ready shape) — the build-time pin FR-24 requires be
// kept visually distinct from the chart's currently DECLARED composition
// (chart_app, see ChartDetailData.DeclaredApps).
type PinnedImage struct {
	AppFullName string
	Version     string
	Digest      string
	Provenance  pb.ArtifactProvenance
}

// ChartEnvPins is one environment's view of this chart: the version
// currently promoted there (if any) and the apps pinned by that exact
// artifact (Artifact.contains — resolved by tools/helm at publish time, not
// re-derived here). ColumnErr mirrors matrix.ColumnResult.Err's NFR-6
// contract.
type ChartEnvPins struct {
	Env       *pb.Environment
	ColumnErr error

	Promoted   bool
	Version    string
	PromotedAt int64 // Promotion.valid_from — 0 when !Promoted
	Images     []PinnedImage
	Drift      []*pb.DriftEntry
}

// ChartArgoOverrideRow is one environment's ArgoCD Application name row for
// screen 22's override editor. Override is the explicit admin-set value for
// this environment (empty when none is set); Default is the
// "<full_name>-<environment_key>" convention WritebackWorkflow falls back to
// otherwise (repository.Chart.ResolveArgoApplicationName, mirrored here
// read-only); Resolved is whichever of the two actually applies.
type ChartArgoOverrideRow struct {
	EnvKey   string
	Override string
	Default  string
	Resolved string
}

// ChartDetailData is everything screen 22 needs. DeclaredApps is the
// chart's currently DECLARED composition (chart.app_ids, resolved to full
// App records) — FR-24 requires this be labelled distinctly from, and never
// merged with, EnvPins[*].Images (the build-time artifact_link pins): the
// two can legitimately disagree. ArgoOverrides is aligned index-for-index
// with Environments, same convention as EnvPins. OverrideErr surfaces a
// failed SetChartArgoApplicationNameOverride submit inline (distinct from a
// load failure, which never reaches this struct at all — see
// buildChartDetail's nil,err contract).
type ChartDetailData struct {
	Chart        *pb.Chart
	Environments []*pb.Environment
	EnvPins      []ChartEnvPins // aligned index-for-index with Environments
	DeclaredApps []*pb.App

	ArgoOverrides []ChartArgoOverrideRow
	OverrideErr   string
}

// ============================================================================
// Screen 21: artifact detail (FR-23)
// ============================================================================

// PinnedByRow is one chart artifact that pins the viewed image artifact
// (ListArtifactPins is scoped to the exact digest requested, so every row
// here really does pin that digest — see ArtifactRegistry.ListArtifactPins's
// doc comment).
type PinnedByRow struct {
	ChartFullName string
	ChartVersion  string
}

// ArtifactDetailData is everything screen 21 needs. PinnedBy/PinnedByErr are
// only populated for kind == IMAGE; for kind == CHART, the template instead
// renders Artifact.contains directly (the "Pins" direction — see the
// wireframe's note that this same layout serves both directions).
// PinnedByErr must NOT be confused with an empty PinnedBy: an image nothing
// pins is a legitimate empty state (FR-23), never an error.
type ArtifactDetailData struct {
	Artifact *pb.Artifact
	Build    *pb.Build

	// OwnerFullName is the owning app's or chart's full_name, resolved
	// separately since Artifact itself only carries app_id/chart_id.
	OwnerFullName string

	// PinnedBy/PinnedByErr are only populated for kind == IMAGE.
	PinnedBy    []PinnedByRow
	PinnedByErr error

	// Contains is only populated for kind == CHART — Artifact.contains
	// (already present on data.Artifact) resolved to display-ready rows
	// with app full name and provenance, matching ChartEnvPins.Images's
	// shape (see chart_data.go).
	Contains []PinnedImage
}
