package repository

import (
	"strings"
	"time"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// Status is the shared reconciliation lifecycle for App and Chart rows. See
// ARCHITECTURE.md "Data model" and AppStatus in protos/messages.proto.
type Status string

const (
	StatusActive   Status = "active"
	StatusMissing  Status = "missing"
	StatusArchived Status = "archived"
)

// ArtifactKind mirrors ArtifactKind in protos/messages.proto.
type ArtifactKind string

const (
	ArtifactKindImage    ArtifactKind = "image"
	ArtifactKindChart    ArtifactKind = "chart"
	ArtifactKindBinary   ArtifactKind = "binary"
	ArtifactKindFirmware ArtifactKind = "firmware"
)

// Promotability mirrors Promotability in protos/messages.proto. Always
// derived server-side from the owning app's/chart's DeployUnit — never
// stored. See ARCHITECTURE.md "Promotability".
type Promotability string

const (
	PromotabilityPromotable    Promotability = "promotable"
	PromotabilityViaChart      Promotability = "via_chart"
	PromotabilityNotPromotable Promotability = "not_promotable"
)

// ManifestSource is the ordering/provenance metadata one Reconcile or
// AssertApps call carries -- the same three AppManifestSet fields
// ReconcileSource already wraps (git_sha / source_committed_at /
// discovered_at), reused here rather than duplicated because AssertApps
// needs exactly the same triple, just without ever consulting the reconcile
// watermark. See AppRepository.AssertApps's doc comment.
type ManifestSource = ReconcileSource

// RejectedOwner is one app or chart AssertApps declined to touch because it
// is ARCHIVED -- a human said this app/chart is gone for good, and a
// release silently resurrecting it is worse than a red step (AR-7c, issue
// #558). Modeled on UnresolvedChart's per-item skip-and-report shape: every
// other app/chart in the same AssertApps call still applies.
type RejectedOwner struct {
	Domain string
	Name   string
	Reason string
}

// AssertResult buckets every App/Chart row touched by one AssertApps call --
// the additive-only counterpart to ReconcileResult. See
// AppRepository.AssertApps's doc comment for exactly what each bucket means;
// unlike ReconcileResult there is no NewlyMissing bucket (AssertApps never
// marks anything MISSING) and no UnresolvedCharts (AssertApps never resolves
// chart_app membership -- that stays ReconcileApps-only).
type AssertResult struct {
	CreatedApps   []App
	UpdatedApps   []App // already ACTIVE; only the manifest snapshot changed
	RecoveredApps []App // MISSING -> ACTIVE
	RejectedApps  []RejectedOwner

	CreatedCharts   []Chart
	UpdatedCharts   []Chart
	RecoveredCharts []Chart
	RejectedCharts  []RejectedOwner
}

// App mirrors one release_app manifest. Never hard-deleted.
//
// As of migration 008 (AR-7c, issue #558) `app` itself is pure identity
// (domain, name, status, first/last-seen) -- every field below EXCEPT those
// is populated by a join to the owner's CURRENT `main`-sweep manifest
// content (v_current_app, in postgres/app.go), not stored directly.
// Migration 010 (AR-8, issue #587) changed how v_current_app finds "current"
// (a point lookup against app_manifest_history's open SCD2 interval, instead
// of a LEFT JOIN LATERAL over one row per commit) but not this struct's
// shape: the wire (App in protos/messages.proto) and every handler/CLI
// consumer read these fields exactly as before -- only where postgres/app.go
// sources them from moved, twice now.
type App struct {
	AppID           string
	Domain          string
	Name            string
	Description     string
	Language        string
	AppType         string
	DeployUnit      appmetapb.DeployUnit
	BazelLabel      string
	ImageRepository string
	Status          Status
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

func (a App) FullName() string { return a.Domain + "-" + a.Name }

// Chart is a Helm chart composing one or more apps. As of migration 008
// (AR-7c) `chart` itself is pure identity -- see App's doc comment for the
// same split; v_current_chart supplies Description/ChartRepository/
// DeployUnit, though for a chart those are (and always were) constants, not
// values sourced from a manifest -- see migration 008's "Why chart_manifest
// has no generated columns".
type Chart struct {
	ChartID         string
	Domain          string
	Name            string
	Description     string
	ChartRepository string
	DeployUnit      appmetapb.DeployUnit
	Status          Status
	AppIDs          []string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

func (c Chart) FullName() string { return c.Domain + "-" + c.Name }

// NormalizeChartName strips the "helm-{domain}-" prefix that
// release_helm_chart's Bazel macro bakes into ChartManifest.Name (needed
// there for git tag and tarball naming, e.g. "helm-manmanv2-control-services")
// so ReconcileApps/AssertApps store and look up charts the same way they do
// apps: a bare name, with domain held separately. Without this, FullName()
// would double up the domain (e.g. "app-registry-helm-app-registry-app-registry"
// for the chart whose domain and base name happen to coincide), which never
// matches the "{domain}-{name}" identifier the release pipeline uses for
// BeginPublish/RecordArtifact.
func NormalizeChartName(domain, name string) string {
	return strings.TrimPrefix(name, "helm-"+domain+"-")
}

// ReconcileResult buckets every App/Chart row touched by one ReconcileApps
// call, matching ReconcileAppsResponse in protos/api_messages_app.proto.
type ReconcileResult struct {
	CreatedApps        []App
	UpdatedApps        []App
	NewlyMissingApps   []App
	RecoveredApps      []App
	CreatedCharts      []Chart
	UpdatedCharts      []Chart
	NewlyMissingCharts []Chart
	RecoveredCharts    []Chart

	// UnresolvedCharts lists every chart in this call's manifest set whose
	// apps entries could not all be resolved to identity rows -- AR-7a. Each
	// such chart is SKIPPED, not fatal: every other app/chart in the same
	// call still applies, and the watermark still advances. See
	// ARCHITECTURE.md "AssertApps (additive) vs. ReconcileApps (absence
	// sweep)" and PLAN.md's AR-7a. Always empty for a dry run's diff in the
	// same shape as every other bucket here -- a dry run computes the same
	// resolution and reports it the same way, it just writes nothing.
	UnresolvedCharts []UnresolvedChart

	// SkippedStale is true when the reconcile watermark rejected this call
	// as older (in commit order) than the most recently applied one -- see
	// watermark.go's ShouldApplyReconcile. A no-op success, not an error:
	// every slice above is empty, and nothing was written. Always false
	// when the call was a dry run -- dry runs never consult the watermark.
	SkippedStale bool
	// CurrentWatermarkGitSHA is the git_sha this call lost to. Populated
	// only when SkippedStale is true.
	CurrentWatermarkGitSHA string
}

// UnresolvedChart is one chart manifest whose apps references could not all
// be resolved to app identity rows during Reconcile -- AR-7a. Modeled on the
// existing App/Chart result buckets: enough to act on without re-deriving it
// from server logs.
type UnresolvedChart struct {
	Domain string
	Name   string
	// AppRefs is every app reference from the chart manifest that failed to
	// resolve, exactly as it appeared in the manifest (a bare name via the
	// deprecated `ChartManifest.apps` path, or a "<domain>/<name>" string
	// via `app_refs`).
	AppRefs []string
	// Reason is a human-readable explanation (unknown app, ambiguous bare
	// name across domains, malformed app_ref) -- not machine-parsed.
	Reason string
}

// AppListFilter is ListAppsRequest's filter set, decoupled from the proto.
type AppListFilter struct {
	Domain     string
	Statuses   []Status // empty means [Active, Missing], the RPC default
	DeployUnit appmetapb.DeployUnit
}

// ChartListFilter is ListChartsRequest's filter set.
type ChartListFilter struct {
	Domain   string
	Statuses []Status
}

// ReconcileRun is one row from the `reconcile_run` table (migration 010,
// AR-8) -- one row per sweep that actually applied (never dry-run, never
// stale-rejected; see postgres/app.go's Reconcile). Mirrors the
// ReconcileRun proto message field-for-field. SourceCommittedAt stays an
// int64 Unix timestamp (matching the underlying BIGINT column and the wire
// type) rather than time.Time, unlike AppliedAt -- see migration 010's
// column comment.
type ReconcileRun struct {
	ReconcileRunID    string
	GitSHA            string
	SourceCommittedAt int64
	AppliedAt         time.Time
	AppsSeen          int32
	ChartsSeen        int32
}

// Build is one CI run. Every artifact hangs off a build.
type Build struct {
	BuildID         string
	GitSHA          string
	GitRef          string
	WorkflowRunID   string
	WorkflowAttempt int32
	Actor           string
	StartedAt       *time.Time
	RecordedAt      time.Time
}

// ArtifactLink records that a chart artifact pins a specific image digest.
type ArtifactLink struct {
	ImageArtifactID string
	AppID           string
	Repository      string
	Version         string
	Digest          string
}

// ArtifactState is the AR-7b artifact lifecycle -- see ARCHITECTURE.md
// "Artifact lifecycle: allocated -> publishing -> published" and migration
// 007. Legal transitions, enforced server-side in
// server/repository/postgres/artifact.go and mirrored in
// server/repository/fake/fake.go: ∅ -> allocated (AllocateVersion), ∅ ->
// publishing (BeginPublish with no prior allocation -- the pre-cutover
// path), allocated -> publishing (BeginPublish), publishing -> published
// (RecordArtifact), publishing -> failed (FailPublish, or the reaper),
// failed -> publishing (a later run retrying the same version), publishing
// -> publishing (BeginPublish again -- AR-7d, issue #558: an idempotent
// heartbeat/re-arm of state_changed_at, not a no-op; see BeginPublish's own
// doc comment for why this exists). published is terminal; anything else
// is FailedPrecondition.
//
// AR-7e (issue #558) adds two more transitions, reachable ONLY through
// ArtifactRepository.AdoptArtifact, never through BeginPublish/
// RecordArtifact: ∅ -> published and failed -> published, both stamping
// Provenance ArtifactProvenanceAdopted instead of ArtifactProvenanceObserved.
// See AdoptArtifact's doc comment for the full state-collision table
// (including why allocated/publishing reject, and why a same-digest
// published row is an idempotent no-op rather than a rewrite).
type ArtifactState string

const (
	ArtifactStateAllocated  ArtifactState = "allocated"
	ArtifactStatePublishing ArtifactState = "publishing"
	ArtifactStatePublished  ArtifactState = "published"
	ArtifactStateFailed     ArtifactState = "failed"
)

// ArtifactProvenance mirrors artifact.provenance (migration 007):
// "observed" (the normal case -- the registry watched this get published)
// vs "adopted" (AR-7e's AdoptArtifact, not implemented in this phase --
// see PLAN.md's AR-7e). Every row this phase writes is "observed"; the
// column exists now so migration 007 doesn't need a second pass later.
type ArtifactProvenance string

const (
	ArtifactProvenanceObserved ArtifactProvenance = "observed"
	ArtifactProvenanceAdopted  ArtifactProvenance = "adopted"
)

// VersionSource mirrors artifact.version_source (migration 007): which
// path authored this row's version. "registry" means AllocateVersion (AR-5)
// reserved it; "tag" means the pre-cutover git-tag path in
// tools/release_helper_go chose it and the registry is merely recording the
// intent -- see ARCHITECTURE.md "The run log" and PLAN.md's AR-5 parity
// exit criterion, which this column turns into a query.
type VersionSource string

const (
	VersionSourceRegistry VersionSource = "registry"
	VersionSourceTag      VersionSource = "tag"
)

// Artifact is a published, digest-addressable image or chart -- or, as of
// AR-7b, the record of an in-flight or failed attempt to publish one. See
// ArtifactState's doc comment for the lifecycle. Digest/BuildID/PublishedAt
// are empty/zero until State reaches the point migration 007's
// artifact_state_shape CHECK constraint requires them (BuildID from
// "publishing" onward, Digest/PublishedAt only once "published").
type Artifact struct {
	ArtifactID  string
	Kind        ArtifactKind
	AppID       string // set when Kind == ArtifactKindImage
	ChartID     string // set when Kind == ArtifactKindChart
	Repository  string
	Version     string
	Digest      string // empty until State == ArtifactStatePublished
	BuildID     string // empty while State == ArtifactStateAllocated
	PublishedAt time.Time

	State          ArtifactState
	Provenance     ArtifactProvenance
	VersionSource  VersionSource
	StateChangedAt time.Time
	// FailReason is set by FailPublish (caller-supplied) or the reaper
	// (hardcoded "stale" -- see ARCHITECTURE.md "The reaper is not
	// optional"). Free text for operator diagnosis; empty outside
	// State == ArtifactStateFailed.
	FailReason string

	// ManifestID is the app_manifest/chart_manifest CONTENT row (migration
	// 010, AR-8; originally a per-commit snapshot row, migration 008, AR-7c)
	// this row's Promotability was derived from -- set once, at the instant
	// State reaches ArtifactStatePublished, never touched again. Empty for
	// allocated/publishing/failed rows, and for any row published before
	// migration 008 (no content row exists that honestly corresponds to what
	// was live when those were originally published -- see migration 008's
	// backfill comments). No FK: it is a polymorphic reference (an image
	// artifact's ManifestID names an app_manifest row, a chart artifact's a
	// chart_manifest row), same reasoning as Artifact not carrying its own
	// FK-typed owner_id. Content-addressing (AR-8) means many artifacts built
	// from byte-identical manifests now legitimately share one ManifestID --
	// a storage side-effect, not something any reader should infer meaning
	// from.
	ManifestID string

	// Promotability is now STORED (migration 008, AR-7c) -- computed ONCE by
	// repository.DerivePromotability at the instant State reaches
	// ArtifactStatePublished, from ManifestID's content (or, absent one,
	// the owner's current v_current_app/v_current_chart deploy_unit -- see
	// postgres/artifact.go's resolveManifestForPublish). Never recomputed on
	// read. This is the fix for the retroactivity bug ARCHITECTURE.md
	// documents: editing an app's deploy_unit after a build was published no
	// longer changes that build's promotability. Empty/unset for
	// allocated/publishing/failed rows -- there is nothing to derive it from
	// until publish.
	Promotability Promotability

	// Contains is populated only for Kind == ArtifactKindChart.
	Contains []ArtifactLink
}

// ContainedImageInput is one image a chart pins, as resolved by tools/helm.
type ContainedImageInput struct {
	AppFullName string
	Repository  string
	Version     string
	Digest      string
}

// ArtifactListFilter is ListArtifactsRequest's filter set. BuildID has no
// corresponding request field on ListArtifactsRequest itself -- it exists
// so GetReleaseRun (AR-7d, issue #558) can reuse ListArtifacts rather than
// standing up a second query, filtering on the column every state from
// "publishing" onward carries (see ArtifactState's doc comment -- an
// "allocated" row never has one). BeginPublishBatch (AR-7d) writes straight
// to "publishing", carrying build_id, precisely so a target it covers is
// never left in "allocated" with nothing to filter on here.
type ArtifactListFilter struct {
	OwnerFullName  string
	Kind           ArtifactKind
	PromotableOnly bool
	BuildID        string
	// Provenance filters to exactly one of ArtifactProvenanceObserved /
	// ArtifactProvenanceAdopted when set; empty means every provenance —
	// AR-7e (issue #558), the query "which rows did we take on faith?" the
	// exit criterion asks for. See ListArtifactsRequest.provenance.
	Provenance ArtifactProvenance
}

// ArtifactLookup identifies an artifact by exactly one of the supported
// keys, matching GetArtifactRequest's oneof-by-convention shape.
type ArtifactLookup struct {
	ArtifactID    string
	Digest        string
	OwnerFullName string
	Kind          ArtifactKind
	Version       string
}

// Environment is a promotion target, e.g. "dev"/"stage"/"prod". A row, not
// an enum, so ephemeral/regional environments are an insert, not a release
// — see ARCHITECTURE.md "Data model". Key is the immutable identity;
// Upsert may change every other field but never Key itself.
type Environment struct {
	EnvironmentID     string
	Key               string
	DisplayName       string
	Rank              int32
	RequiresApproval  bool
	GitopsPath        string
	AllowedPrincipals []string
	Archived          bool
	CreatedAt         time.Time
}

// PromotionState mirrors PromotionState in protos/messages.proto. AR-3c
// only ever writes PromotionStateActive; the other values are reserved for
// the approval gate described in ARCHITECTURE.md "Future: approval gate".
type PromotionState string

const (
	PromotionStatePendingApproval PromotionState = "pending_approval"
	PromotionStateActive          PromotionState = "active"
	PromotionStateSuperseded      PromotionState = "superseded"
	PromotionStateFailed          PromotionState = "failed"
)

// PromotionAction mirrors PromotionAction in protos/messages.proto — the
// human-meaningful verb recorded on promotion_event.
type PromotionAction string

const (
	PromotionActionPromote  PromotionAction = "promote"
	PromotionActionRollback PromotionAction = "rollback"
	PromotionActionOverride PromotionAction = "override"
	PromotionActionRetire   PromotionAction = "retire"
	PromotionActionApprove  PromotionAction = "approve"
	PromotionActionReject   PromotionAction = "reject"
)

// TargetKey is the promoted thing's identity, denormalized onto the
// promotion table so its partial unique index can be expressed without a
// nullable two-column target (app_id is NULL for chart artifacts and vice
// versa) — see ARCHITECTURE.md "SCD2 on promotion".
func TargetKey(kind ArtifactKind, ownerFullName string) string {
	return string(kind) + ":" + ownerFullName
}

// Promotion is SCD2 state: what is deployed to an environment right now, or
// at any past instant. Follows the repo-wide valid_from/valid_to convention
// — see AGENTS.md "SCD2". AppID/ChartID/Repository/Version/Digest are
// populated by a join to the promoted artifact at read time — never stored
// redundantly on the promotion row itself, so there is exactly one place
// that data can drift from. ValidTo == nil means still current.
type Promotion struct {
	PromotionID    string
	EnvironmentID  string
	EnvironmentKey string // denormalized at read time, for readability
	TargetKey      string

	Kind       ArtifactKind
	AppID      string // set when Kind == ArtifactKindImage
	ChartID    string // set when Kind == ArtifactKindChart
	ArtifactID string
	Repository string
	Version    string
	Digest     string

	State      PromotionState
	IsOverride bool

	ValidFrom time.Time
	ValidTo   *time.Time
}

// PromotionEvent is the append-only audit log. NOT SCD2 — see AGENTS.md,
// event logs get their own shape.
type PromotionEvent struct {
	EventID            string
	PromotionID        string
	Action             PromotionAction
	Actor              string
	Reason             string
	TemporalWorkflowID string
	TemporalRunID      string
	OccurredAt         time.Time
}

// PromotionListFilter is ListPromotionsRequest's filter set.
type PromotionListFilter struct {
	EnvironmentKey string
	OwnerFullName  string
	IncludeHistory bool
}

// PromotionEventListFilter is ListPromotionEventsRequest's filter set.
type PromotionEventListFilter struct {
	PromotionID    string
	EnvironmentKey string
	OwnerFullName  string
	Actor          string
	Since          time.Time
}

// WritebackOutboxStatus is writeback_outbox.status -- see ARCHITECTURE.md
// "Writeback: outbox -> Temporal" and AR-4b's PLAN.md scope.
type WritebackOutboxStatus string

const (
	// WritebackOutboxStatusPending: written, not yet claimed by a worker.
	WritebackOutboxStatusPending WritebackOutboxStatus = "pending"
	// WritebackOutboxStatusClaimed: a worker has claimed this row and is
	// (or was) starting its WritebackWorkflow. A claim older than the
	// drain loop's staleness window is eligible to be reclaimed by
	// ClaimBatch -- see the worker being killed mid-run in AR-4b's exit
	// criteria.
	WritebackOutboxStatusClaimed WritebackOutboxStatus = "claimed"
	// WritebackOutboxStatusDone: the WritebackWorkflow was started (or was
	// already running/completed under the same workflow id -- Temporal's
	// dedup, see ARCHITECTURE.md). Terminal; ClaimBatch never selects a
	// done row again.
	WritebackOutboxStatusDone WritebackOutboxStatus = "done"
	// WritebackOutboxStatusFailed: MarkFailed recorded a non-retryable
	// error. Not currently reachable by drain logic (retryable failures
	// go back to pending), reserved for a future manual-intervention path.
	WritebackOutboxStatusFailed WritebackOutboxStatus = "failed"
)

// WritebackOutbox is one row of the transactional outbox described in
// ARCHITECTURE.md "Writeback: outbox -> Temporal". Written inside the same
// transaction as the promotion + promotion_event it carries (see
// server/handlers/promotion.go's enqueueWriteback) so a promotion and its
// writeback intent commit or roll back together -- the property this table
// exists to guarantee.
type WritebackOutbox struct {
	OutboxID       string
	PromotionID    string
	EnvironmentID  string
	EnvironmentKey string // denormalized at write time, for worker logging without a join
	// Domain is the owning app's/chart's domain (repository.App.Domain /
	// repository.Chart.Domain), denormalized at write time exactly like
	// EnvironmentKey above -- see 014_writeback_outbox_domain.up.sql's doc
	// comment. Lets the worker render/publish per (domain, environment)
	// without a join back through promotion -> artifact -> app/chart.
	Domain string
	// EventID is the promotion_event this row corresponds to, so the
	// worker can stamp temporal_workflow_id/temporal_run_id back onto the
	// audit row once the workflow starts.
	EventID string
	// StateHash is stateHash(...) computed inside the SAME transaction
	// that wrote the promotion -- see server/handlers/promotion.go. The
	// Writeback activity's Publish method compares this (transitively, via
	// the RenderedState it derives) against the last-published hash to
	// skip a no-op write.
	StateHash string
	Status    WritebackOutboxStatus

	ClaimedBy string
	ClaimedAt *time.Time

	// WorkflowID is set once ClaimBatch's caller has (re-)started the
	// WritebackWorkflow. Always equal to PromotionID in practice (that's
	// the workflow id convention -- see ARCHITECTURE.md), stored anyway so
	// a reader never has to assume it.
	WorkflowID  string
	CompletedAt *time.Time
	LastError   string
	Attempts    int32

	CreatedAt time.Time
}

// DomainAdoptionStage mirrors the `domain_adoption.stage` CHECK constraint
// (migration 001) and ARCHITECTURE.md "Resolved questions" #3's per-domain
// cutover table. AllocateVersion (AR-5) is the only capability gated on
// this today; recording (AR-2) is deliberately never gated.
type DomainAdoptionStage string

const (
	DomainAdoptionStageObserve  DomainAdoptionStage = "observe"
	DomainAdoptionStagePromote  DomainAdoptionStage = "promote"
	DomainAdoptionStageAllocate DomainAdoptionStage = "allocate"
)

// VersionAllocation is the result of a successful AllocateVersion call: the
// newly reserved version, and what it was incremented from (empty for a
// first release).
type VersionAllocation struct {
	Version         string
	PreviousVersion string
}
