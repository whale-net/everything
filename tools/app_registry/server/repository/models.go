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
	// ArgoApplicationNameOverrides is an optional, admin-settable map of
	// environment_key -> ArgoCD Application name, overriding what
	// WritebackWorkflow's TriggerArgoRefresh/PollArgoSyncStatus activities
	// target for this chart's promotions in that one environment
	// (worker/writeback/argosync.go), for ad-hoc/legacy deployments whose
	// real Application name doesn't follow the
	// "<FullName>-<environment>" convention. Keyed per environment --
	// rather than one value for the whole chart -- because an ad-hoc
	// deployment's naming can differ unrelatedly between environments
	// (e.g. dev "foo-dev-app", prod "prod-svc-foo", sharing no pattern).
	// An environment absent from this map (nil/empty, the default,
	// migration 022) uses the convention. Set only via
	// AppRepository.SetChartArgoApplicationNameOverride, one environment
	// at a time -- ReconcileApps never writes it. See
	// ResolveArgoApplicationName.
	ArgoApplicationNameOverrides map[string]string
}

func (c Chart) FullName() string { return c.Domain + "-" + c.Name }

// ResolveArgoApplicationName returns the ArgoCD Application name for this
// chart in environmentKey: the explicit override in
// ArgoApplicationNameOverrides[environmentKey], if one is set for this
// exact environment (an ad-hoc/legacy deployment can have a completely
// different name in each environment, not just a mechanical variation --
// dev and prod need not share any naming pattern); otherwise the
// convention every standard chart uses: "<FullName>-<environmentKey>".
func (c Chart) ResolveArgoApplicationName(environmentKey string) string {
	if name, ok := c.ArgoApplicationNameOverrides[environmentKey]; ok && name != "" {
		return name
	}
	return c.FullName() + "-" + environmentKey
}

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

// AppBuildLog is one row from the `app_build_log` table (migration 019,
// issue #923, FR8-FR12/FR14) -- SCD2-shaped (ValidFrom/ValidTo) but,
// unlike app_manifest_history (migration 010), written UNCONDITIONALLY on
// every ci.yml reconcile-app-registry push, not gated on content change.
// Answers "what commit is OwnerID's latest logged build" for FR9's
// watermark-free release ref resolution. OwnerID is polymorphic --
// app.app_id when Kind is ArtifactKindImage, chart.chart_id when Kind is
// ArtifactKindChart -- mirroring Artifact.OwnerID's convention. See
// AppBuildLogRepository's doc comment for the write/read contract.
type AppBuildLog struct {
	AppBuildLogID string
	OwnerID       string
	Kind          ArtifactKind
	GitSHA        string
	BuildID       string
	ValidFrom     time.Time
	ValidTo       *time.Time
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
	IdentityDigest string // FR-13/FR-14: computed over uncompressed content, invariant to compression; used for no-op detection
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
	// this row was published against -- build-commit provenance, set once,
	// at the instant State reaches ArtifactStatePublished, never touched
	// again. Unlike Promotability (see below), this remains stored rather
	// than derived: it is a historical fact about which manifest content
	// existed at publish time, not a live-changing property of the owner's
	// current state -- issue #833 only reversed the "store once" tradeoff
	// for Promotability. Empty for
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

	// Promotability is derived LIVE, on every read (issue #833; previously
	// STORED once at publish time, migration 008/AR-7c) -- computed by
	// repository.DerivePromotability from the owner's CURRENT
	// v_current_app/v_current_chart deploy_unit, not from ManifestID's
	// content. This means editing an app's deploy_unit, or fixing a
	// DerivePromotability rule (e.g. #810), changes what is read back for
	// artifacts published before the edit -- AR-7c had deliberately frozen
	// this to avoid exactly that, but in production that traded one
	// correctness problem for another: a rule fix could never reach
	// artifacts published before the fix landed, permanently stranding them
	// on the old, wrong value with no way to self-correct short of a manual
	// DB edit. Staleness-under-rule-changes was judged worse than the extra
	// read-time join this reintroduces -- see ARCHITECTURE.md "Promotability"
	// and architecture/08-release-lifecycle/02-manifest-snapshot.md "As
	// built (issue #833, migration 014)". Empty/unset for allocated/
	// publishing/failed rows -- there is nothing to derive it from until
	// publish.
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
	ArtifactID      string
	Digest          string
	OwnerFullName   string
	Kind            ArtifactKind
	Version         string
	LatestPublished bool
	BeforeVersion   string
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

// PromotionSyncEvent is the append-only ArgoCD sync/health observation log
// -- NOT SCD2, see AGENTS.md and promotion_event's own doc comment in
// 003_promotion.up.sql.
type PromotionSyncEvent struct {
	SyncEventID  string
	PromotionID  string
	Source       string // one of the CHECK-constrained values below
	SyncStatus   string
	HealthStatus string
	OccurredAt   time.Time
}

// PromotionSyncEvent.Source values -- must match promotion_sync_event's
// `source` CHECK constraint (migration 020) exactly.
const (
	PromotionSyncEventSourceRefreshTriggered = "refresh_triggered"
	PromotionSyncEventSourcePollObserved     = "poll_observed"
	PromotionSyncEventSourceRetryTriggered   = "retry_triggered"
	PromotionSyncEventSourceRetryObserved    = "retry_observed"
)

// PromotionSyncOutcome is FR8's server-derived, visibly-distinct summary of
// a PromotionDetails' sync_events -- Go-native mirror of
// appregistrypb.PromotionSyncOutcome (same convention as every other
// repository/proto split in this service: this package never imports the
// proto package). Zero value (PromotionSyncOutcomeUnspecified) must never
// be returned by GetDetails -- every promotion has a real classification,
// even a promotion with zero sync_events (PromotionSyncOutcomePending).
type PromotionSyncOutcome string

const (
	PromotionSyncOutcomeUnspecified   PromotionSyncOutcome = ""
	PromotionSyncOutcomePending       PromotionSyncOutcome = "pending"
	PromotionSyncOutcomeSyncedHealthy PromotionSyncOutcome = "synced_healthy"
	PromotionSyncOutcomeSyncFailed    PromotionSyncOutcome = "sync_failed"
)

// PromotionDetails assembles one promotion's full lifecycle (FR7-FR9, issue
// #1031) for the Promotion Details page: GetDetails composes this by
// joining promotion -> promotion_event (the creating event) -> artifact
// (to-version; from-version via the superseded promotion) ->
// writeback_outbox (#1029's Location/CommitSHA) -> promotion_sync_event
// (#1028's ListSyncEvents). Go-native types throughout, not proto types --
// see handlers/convert.go for the promotionDetailsToPB translation this
// feeds.
type PromotionDetails struct {
	Promotion Promotion
	// RequestEvent is the promote/rollback event that created Promotion --
	// requester (RequestEvent.Actor) + request time (RequestEvent.OccurredAt).
	RequestEvent PromotionEvent
	// FromVersion is "" if this is the target's first-ever promotion (no
	// prior row to supersede).
	FromVersion string
	// ToVersion is Promotion's own artifact.version.
	ToVersion string

	// WritebackLocation is "" if not yet published.
	WritebackLocation string
	// WritebackCommitSHA is "" per FR7a when no real commit was made (the
	// stub path, or a Skipped no-op) -- never a stand-in/synthetic value.
	WritebackCommitSHA string

	// SyncEvents is the full chronological history (#1028), oldest first --
	// see PromotionRepository.ListSyncEvents.
	SyncEvents []PromotionSyncEvent
	// CurrentSyncStatus/CurrentHealthStatus mirror the most recent
	// SyncEvents entry, "" if SyncEvents is empty.
	CurrentSyncStatus   string
	CurrentHealthStatus string
	Outcome             PromotionSyncOutcome
}

// DerivePromotionSyncOutcome computes PromotionDetails' Outcome/
// CurrentSyncStatus/CurrentHealthStatus (FR8) from a promotion's full
// sync_events history, oldest-first (ListSyncEvents' own order) -- the
// single shared implementation every GetDetails (postgres, fake) calls, so
// the two backends never derive this classification differently. "Current"
// is always the LAST entry, terminal or not: if the most recent poll
// session exhausted its attempts without reaching Synced+Healthy/Degraded
// (FR5's "still pending" case), that non-terminal pair IS the current
// status, and the outcome is PENDING -- this method never falls back to
// scanning further back in history for an earlier terminal observation.
func DerivePromotionSyncOutcome(events []PromotionSyncEvent) (outcome PromotionSyncOutcome, currentSyncStatus, currentHealthStatus string) {
	if len(events) == 0 {
		return PromotionSyncOutcomePending, "", ""
	}
	last := events[len(events)-1]
	currentSyncStatus = last.SyncStatus
	currentHealthStatus = last.HealthStatus
	switch {
	case last.SyncStatus == "Synced" && last.HealthStatus == "Healthy":
		outcome = PromotionSyncOutcomeSyncedHealthy
	case last.HealthStatus == "Degraded":
		outcome = PromotionSyncOutcomeSyncFailed
	default:
		outcome = PromotionSyncOutcomePending
	}
	return outcome, currentSyncStatus, currentHealthStatus
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
	// EnvironmentKey above -- see 015_writeback_outbox_domain.up.sql's doc
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

	// Location/CommitSHA are what GitOpsActivities.Publish actually
	// produced (FR7a, issue #1029), set by RecordResult once Publish
	// succeeds -- distinct from WorkflowID/CompletedAt above, which
	// MarkDone sets when the workflow merely STARTS, not when Publish
	// COMPLETES. CommitSHA is '' on the no-op Skipped path and on
	// StubActivities' no-git dev/test path -- never a stand-in/synthetic
	// value. See worker/writeback.PublishResult.CommitSHA and migration
	// 021_writeback_outbox_result.
	Location  string
	CommitSHA string

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

// ReleaseRunTargetState is `release_run_target.state` (migration 016,
// NFR4). NOT part of the artifact lifecycle (ArtifactState) -- this is the
// UI-trigger layer's own view of one target's progress through a release,
// tracked independently of (and ahead of) whatever `artifact` row that
// target's build eventually produces. Legal transitions, enforced
// server-side in server/repository/postgres/release_run.go and mirrored in
// server/repository/fake: queued -> building -> publishing -> recording ->
// succeeded, with a transition to failed legal from any non-terminal state.
// succeeded/failed are terminal.
type ReleaseRunTargetState string

const (
	ReleaseRunTargetStateQueued     ReleaseRunTargetState = "queued"
	ReleaseRunTargetStateBuilding   ReleaseRunTargetState = "building"
	ReleaseRunTargetStatePublishing ReleaseRunTargetState = "publishing"
	ReleaseRunTargetStateRecording  ReleaseRunTargetState = "recording"
	ReleaseRunTargetStateSucceeded  ReleaseRunTargetState = "succeeded"
	ReleaseRunTargetStateFailed     ReleaseRunTargetState = "failed"
)

// ReleaseRun is one row per triggered release (a batch covering one or more
// targets) -- see migration 016's doc comment and
// architecture/08-release-lifecycle/04-run-log.md "The run log". NOT SCD2
// (AGENTS.md "SCD2") -- written once by CreateReleaseRun and never mutated
// after create, except TemporalRunID (filled in once the workflow actually
// starts running under TemporalWorkflowID) and ResolvedPlan (filled in
// once, by ReleaseRunRepository.SetResolvedPlan -- see that field's doc
// comment).
type ReleaseRun struct {
	ReleaseRunID string
	// TriggeredBy is the authenticated user who triggered this release
	// (FR1).
	TriggeredBy string
	// RequestedScope is the raw `all`/domain/comma-list input as given by
	// the trigger (FR1), unnormalized.
	RequestedScope string
	// DigestInput is FR2's per-target digest map, serialized as JSON; nil
	// means "build fresh" rather than "pinned to an empty set".
	DigestInput []byte
	// ResolvedPlan is the single plan resolved for this release (FR7/FR8),
	// serialized as JSON. nil/NULL until ReleaseRunRepository.SetResolvedPlan
	// stamps it -- CreateReleaseRun does NOT populate this itself (issue
	// #906, validation finding #903): FR7/FR8's plan resolution
	// (worker/release/plan.go's ResolvePlan) shells out to
	// `release_helper_go plan` against a full monorepo checkout and only
	// runs inside app-registry-worker's ReleaseWorkflow, after
	// CreateReleaseRun has already committed -- see
	// server/handlers/release.go's TriggerRelease doc comment. Written at
	// most once (the single SetResolvedPlan call from ReleaseWorkflow's
	// RecordResolvedPlan activity) and never rewritten again after that.
	ResolvedPlan []byte
	// TemporalWorkflowID is the workflow-id-based dedup key (FR5/NFR2),
	// release.WorkflowID(targets) -- deterministic per target batch, with
	// no time component (see that function's doc comment), so it is NOT
	// unique across every release_run row over time: two release_run rows
	// for the same batch, created weeks apart, legitimately share this
	// value once the earlier one's targets are all terminal -- see
	// ReleaseRunRepository.CreateReleaseRun's doc comment (migration 017)
	// and ListReleaseRunsByTarget's doc comment for the read-side
	// implication (most-recent-first, not "the" row for a workflow id).
	TemporalWorkflowID string
	// TemporalRunID is empty until the workflow named by TemporalWorkflowID
	// actually starts running.
	TemporalRunID string
	// BuildRefRunID/BuildRefRunURL identify the GitHub Actions run
	// DispatchBuild dispatched for this release (migration 023). Empty
	// (not NULL) until DispatchBuild's first successful GitHub dispatch,
	// mirroring TemporalRunID's "not yet known" convention above.
	// DispatchBuild (worker/release/activities.go) reads this back first,
	// before calling GitHub, and returns it unchanged instead of
	// dispatching a second `workflow_dispatch` for the same release run --
	// see ReleaseRunRepository.SetBuildRef's doc comment for why a second
	// dispatch is unsafe, not just wasteful.
	BuildRefRunID  string
	BuildRefRunURL string
	CreatedAt      time.Time
}

// ReleaseRunTarget is one row per target (app or chart) in a ReleaseRun's
// batch. A row IS the current state of one target -- transitioned in place
// via UPDATE, matching `artifact`'s existing mutation style (migration
// 007), not `promotion`'s SCD2 open/close style. See
// ReleaseRunTargetState's doc comment for the legal transition table.
type ReleaseRunTarget struct {
	ReleaseRunTargetID string
	ReleaseRunID       string
	OwnerFullName      string
	Kind               ArtifactKind // ArtifactKindImage or ArtifactKindChart only
	State              ReleaseRunTargetState
	StateChangedAt     time.Time
	// BuildID is empty until the 'building' step actually runs one.
	BuildID string
	// ErrorDetail is set when State == ReleaseRunTargetStateFailed; free
	// text for operator diagnosis, empty otherwise.
	ErrorDetail string
}

// UploadState mirrors upload_record.state (migration 023, FR-7, FR-52).
// Legal transitions (per schema constraints): allocated -> uploading/failed,
// uploading -> confirmed/failed, confirmed/failed are terminal.
type UploadState string

const (
	UploadStateAllocated UploadState = "allocated"
	UploadStateUploading UploadState = "uploading"
	UploadStateConfirmed UploadState = "confirmed"
	UploadStateFailed    UploadState = "failed"
)

// UploadRecord is one row per issued upload authorization (FR-7, FR-52).
// Deliberately NOT SCD2: an upload is an entity converging on a terminal
// state, not a slowly-changing attribute. Lifecycle state is a single field
// per row, not a history-tracking mechanism; append-only transition history
// (if needed) would be a separate log table. See migration 023's doc comment
// for the detailed rationale.
type UploadRecord struct {
	UploadID              string
	ObjectKey             string
	ArtifactKind          ArtifactKind
	ArtifactIdentity      string
	VersionReference      string
	RequestingPrincipal   string
	IssuedAt              time.Time
	State                 UploadState
	StateChangedAt        time.Time
	AttributionPrincipal  string
	WorkflowRunID         string
	AttributionTimestamp  time.Time
}

// BlobConfirmationState mirrors blob_record.confirmation_state (FR-46).
// Only a confirmed blob is ever a dedupe target; unconfirmed and
// failed-verification blobs are representable and queryable but not selectable
// as dedupe hits.
type BlobConfirmationState string

const (
	BlobConfirmationStateUnconfirmed         BlobConfirmationState = "unconfirmed"
	BlobConfirmationStateConfirmed           BlobConfirmationState = "confirmed"
	BlobConfirmationStateFailedVerification  BlobConfirmationState = "failed_verification"
)

// BlobRecord is one stored blob, identified by the three-tuple
// (uncompressed_content_digest, stored_encoding, content_type) per FR-61.
// Deliberately NOT SCD2: a blob is an immutable, content-addressed entity.
// Its existence is append-only; it never "becomes" something else. The
// confirmation_state is not a slowly-changing attribute of an environment or
// version; it is a property of the blob itself converging toward terminal
// confirmation. See migration 023's doc comment for the detailed rationale.
type BlobRecord struct {
	BlobID                      string
	UncompressedContentDigest   string
	StoredEncoding              string
	ContentType                 string
	ConfirmationState           BlobConfirmationState
	CreatedAt                   time.Time
	ConfirmationChangedAt       time.Time
}

// BlobVersion records the many-to-one relationship between stored blobs and
// published versions (FR-12). One stored blob may be referenced by multiple
// published versions across different minor series, different majors, and
// different owners/kinds. A published version's blob reference is immutable:
// never updated after publish. Changing which bytes a version resolves to
// requires a new version.
type BlobVersion struct {
	BlobID     string
	ArtifactID string
}

// StoredObjectKey records the actual object key of every object the registry
// publishes, per version and per declared variant (FR-25). The resolution
// task reads from here, and FR-40's recovery route can discover a key from
// the database with the API server down.
type StoredObjectKey struct {
	StoredObjectKeyID string
	ArtifactID        string
	VariantKey        string
	ObjectKey         string
	RecordedAt        time.Time
}
