// Package repository defines the storage interfaces the handlers depend on.
// AR-2 fills in the AppRegistry/ArtifactRegistry subset; AR-3/AR-4 add
// EnvironmentRepository/PromotionRepository following the same shape.
//
// postgres/ is the pgx-backed implementation; fake/ is an in-memory
// implementation used by handler-level logic tests (see fake/fake.go for why
// there is no Postgres-backed test in this package under Bazel).
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// Domain errors. Handlers map these to gRPC codes via errors.Is; repository
// implementations must wrap one of these with %w so the mapping works
// regardless of which implementation (postgres or fake) is behind the
// interface.
var (
	// ErrNotFound: the requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists: a unique constraint would be violated.
	ErrAlreadyExists = errors.New("already exists")
	// ErrInvalidArgument: caller input is structurally invalid (e.g. a
	// chart pinning an unrecorded image digest, an unknown chart-app name).
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrFailedPrecondition: the request is well-formed but illegal given
	// current state (e.g. SetAppStatus(ACTIVE) on an app absent from the
	// latest reconcile).
	ErrFailedPrecondition = errors.New("failed precondition")

	// ErrOwnerNotReconciled: RecordArtifact's owner_full_name resolved to no
	// known app/chart row (see handlers.resolveOwner). It wraps
	// ErrInvalidArgument (not a sibling of it) so mapRepoErr's existing
	// errors.Is(err, ErrInvalidArgument) case keeps matching unchanged --
	// mapRepoErr additionally checks for this more specific sentinel first
	// to attach a structured apierrors.ReasonOwnerNotReconciled detail,
	// which the CLI (and, through it, CI) uses to distinguish "app isn't
	// registered yet" from any other registry failure without parsing the
	// message text. See issue #547 and ARCHITECTURE.md "Reconcile
	// watermark" for why this case is common and expected, not a bug.
	ErrOwnerNotReconciled = fmt.Errorf("%w: owner not reconciled", ErrInvalidArgument)

	// ErrOwnerArchived: a write RPC (RecordArtifact, BeginPublish,
	// AllocateVersion) targeted an app/chart whose Status is ARCHIVED.
	// A human archived it deliberately via SetAppStatus (see ARCHITECTURE.md
	// "Triage: the MISSING/ARCHIVED lifecycle") -- silently resurrecting it
	// by recording a new build against it would undo that decision without
	// telling anyone. Wraps ErrFailedPrecondition (well-formed request,
	// illegal given current state), matching the AssertApps per-item reject
	// this same rule enforces for identity assertion -- see
	// AppRepository.AssertApps's doc comment. Distinct from
	// ErrOwnerNotReconciled (which wraps ErrInvalidArgument -- an unknown
	// owner is a caller mistake, not a state conflict).
	ErrOwnerArchived = fmt.Errorf("%w: owner is archived", ErrFailedPrecondition)
)

// AppRepository covers App and Chart identity — the two tables ReconcileApps
// writes together, since a chart's chart_app join depends on app rows
// existing first.
type AppRepository interface {
	// Reconcile performs the FULL replace described in ARCHITECTURE.md: every
	// app/chart in the manifest set is created or updated and set ACTIVE;
	// anything known but absent is marked MISSING (ARCHIVED rows are left
	// alone); anything MISSING that reappears is reported as recovered.
	// chart.apps/app_refs entries are resolved to app_ids.
	//
	// AR-7a: this is PARTIALLY applying with respect to chart resolution. A
	// chart whose apps references don't all resolve (unknown app, or an
	// ambiguous bare name across domains) is reported in
	// ReconcileResult.UnresolvedCharts and SKIPPED -- it is not created or
	// updated, its chart_app membership is not touched, and critically it is
	// NOT marked MISSING by the absence sweep either: a chart present in the
	// manifest set but unresolvable is *present*, not absent (see
	// ARCHITECTURE.md "AssertApps (additive) vs. ReconcileApps"). Every
	// other app and chart in the same call still applies, and the watermark
	// still advances. This is a deliberate change from pre-AR-7a behavior,
	// where any resolution failure aborted the whole transaction and left
	// the watermark unmoved. dryRun computes the result without writing.
	//
	// source carries the ordering metadata (git_sha / source_committed_at /
	// discovered_at) this call is checked against the reconcile watermark
	// with -- see ARCHITECTURE.md "Reconcile watermark", watermark.go's
	// ShouldApplyReconcile, and issue #545. When dryRun is true, source is
	// ignored entirely: a dry run must never consult or advance the
	// watermark. When dryRun is false and the watermark rejects source as
	// stale, Reconcile returns a result with SkippedStale set and writes
	// nothing -- a no-op success, not an error (a re-run of an older CI
	// workflow correctly declining to revert newer state must not fail).
	// Implementations MUST perform the watermark read (locking, e.g.
	// `SELECT ... FOR UPDATE`) and the apply-or-skip decision inside the
	// SAME transaction as the app/chart writes, so two concurrent Reconcile
	// calls serialize rather than both reading a stale watermark.
	Reconcile(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, source ReconcileSource, dryRun bool) (*ReconcileResult, error)

	// AssertApps is the additive-only counterpart to Reconcile (AR-7c, issue
	// #558): identity assert + manifest snapshot write, safe to call from
	// ANY ref -- a divergent branch, an old tag, an unmerged PR -- because it
	// NEVER marks anything MISSING and NEVER touches chart_app membership
	// (that stays Reconcile/main-sweep-only: resolving composition correctly
	// needs the canonical complete tree, which a release ref is not
	// guaranteed to be). This is what closes ordering 1 (issue #547) without
	// the provisional-upsert trade #547's own "Rejected alternatives"
	// rejected: there is no more mutable metadata for a divergent ref to
	// clobber, because App/Chart's mutable fields all live in the per-commit
	// manifest snapshot now (see ARCHITECTURE.md "App identity vs. per-build
	// manifest snapshot").
	//
	// Every app/chart in apps/charts is either created (∅ -> ACTIVE),
	// recovered (MISSING -> ACTIVE, same automatic recovery Reconcile's
	// absence sweep triggers), or -- if already ACTIVE -- left with its
	// identity untouched and just gets a fresh manifest snapshot recorded
	// (idempotent on (owner_id, source_git_sha); a repeat call for the same
	// commit writes nothing new). An app/chart that is ARCHIVED is REJECTED
	// per item, not for the whole call -- see AssertResult.RejectedApps/
	// RejectedCharts, modeled on ReconcileResult.UnresolvedCharts's
	// per-item-skip shape: a human archived it on purpose (SetAppStatus,
	// see ARCHITECTURE.md "Triage"), and a release silently resurrecting it
	// is worse than a red step. Every other app/chart in the same call still
	// applies.
	//
	// source carries the same ordering/provenance metadata Reconcile's
	// source does, but AssertApps NEVER consults or advances the reconcile
	// watermark -- that guard exists to keep an OLDER main sweep from
	// reverting a NEWER one's absence-sweep result, a concern that does not
	// apply here (AssertApps never marks anything MISSING, so there is
	// nothing for a stale call to wrongly revert). Must be called inside
	// Registry.WithTx.
	AssertApps(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, source ManifestSource) (*AssertResult, error)

	ListApps(ctx context.Context, filter AppListFilter) ([]App, error)
	GetAppByID(ctx context.Context, appID string) (*App, error)
	GetAppByFullName(ctx context.Context, fullName string) (*App, error)
	// ChartsForApp returns the charts composing appID, for GetAppResponse.
	ChartsForApp(ctx context.Context, appID string) ([]Chart, error)

	ListCharts(ctx context.Context, filter ChartListFilter) ([]Chart, error)
	GetChartByID(ctx context.Context, chartID string) (*Chart, error)
	GetChartByFullName(ctx context.Context, fullName string) (*Chart, error)

	// SetAppStatus is the human-triage path and supports exactly one
	// transition: StatusMissing -> StatusArchived. missing -> active happens
	// automatically via Reconcile's "recovered" path, so it is never legal
	// here. archived -> archived is a no-op success (idempotent retry); any
	// other starting status, or a target other than StatusArchived, fails
	// with ErrFailedPrecondition.
	SetAppStatus(ctx context.Context, appID string, target Status, reason string) (*App, error)
}

// BuildRepository covers the `build` table.
type BuildRepository interface {
	// RecordBuild upserts on the (workflow_run_id, workflow_attempt) unique
	// constraint and reports whether the row already existed.
	RecordBuild(ctx context.Context, b Build) (*Build, bool, error)
	GetBuild(ctx context.Context, buildID string) (*Build, error)

	// GetBuildByWorkflowRun looks up the build for (workflowRunID, attempt).
	// attempt == 0 means "the latest attempt recorded for this run id" --
	// GetReleaseRun's (AR-7d, issue #558) common case, since a caller
	// checking on a run's status usually doesn't know in advance whether it
	// has been re-run. Returns ErrNotFound if no build was ever recorded
	// for this run id (attempt == 0) or this exact (run id, attempt) pair.
	GetBuildByWorkflowRun(ctx context.Context, workflowRunID string, attempt int32) (*Build, error)
}

// ArtifactRepository covers `artifact` and `artifact_link`. As of AR-7b
// (migration 007), `artifact` rows carry the allocated -> publishing ->
// published -> failed lifecycle described in ArtifactState's doc comment —
// every method below that writes a row must enforce the legal-transition
// table there, translating any other starting state into
// ErrFailedPrecondition.
type ArtifactRepository interface {
	// RecordArtifact is the publishing -> published transition: it looks
	// for an existing row by digest first (the natural key — a repeat with
	// the SAME digest is an idempotent success, alreadyRecorded=true,
	// mirroring BuildRepository.RecordBuild), then by (owner, kind,
	// version). A "publishing" row there is completed (digest/published_at
	// set, state -> published). A "published" row there with a DIFFERENT
	// digest is a real conflict (ErrAlreadyExists) — a different digest for
	// an already-published version is rejected, not merged. A "publishing"
	// row is not found and there is no existing row at all: creating
	// directly in "published" is allowed only when domainStage ==
	// DomainAdoptionStageObserve (ARCHITECTURE.md "Backward compatibility
	// during rollout" — this is what lets CI adopt BeginPublish per domain
	// instead of in one cutover); any other stage rejects with
	// ErrFailedPrecondition, since BeginPublish must have run first. An
	// "allocated" or "failed" row found by (owner, kind, version) is also
	// ErrFailedPrecondition — those need BeginPublish (allocated/failed ->
	// publishing) before RecordArtifact can complete them. For kind ==
	// chart, every entry in contains must already be a PUBLISHED artifact
	// (matched by digest) or the call fails with ErrInvalidArgument — a
	// chart may not pin an unknown or not-yet-published artifact.
	RecordArtifact(ctx context.Context, a Artifact, contains []ContainedImageInput, domainStage DomainAdoptionStage) (artifact *Artifact, alreadyRecorded bool, err error)

	ListArtifacts(ctx context.Context, filter ArtifactListFilter) ([]Artifact, error)
	GetArtifact(ctx context.Context, lookup ArtifactLookup) (*Artifact, error)

	// ResolveArtifact walks a chart artifact to its pinned image artifacts
	// and their originating builds. lookup identifies the chart artifact by
	// artifact_id or digest.
	ResolveArtifact(ctx context.Context, lookup ArtifactLookup) (artifact *Artifact, images []Artifact, builds []Build, err error)

	// AllocateVersion reserves the next version for (kind, ownerID) per
	// ARCHITECTURE.md's version model (AR-5) and PLAN.md's AR-5 addendum:
	// increment is "major"|"minor"|"patch" and is ignored when
	// explicitVersion is set. As of AR-7b (migration 007) this writes an
	// `artifact` row in state "allocated" directly — the ∅ -> allocated
	// transition — rather than to the now-retired version_allocation
	// table; repository is the owning app's/chart's stored
	// image_repository/chart_repository (required because artifact.repository
	// is NOT NULL even for a digest-less allocated row). Implementations
	// must perform this as a transactional INSERT against a unique (owner,
	// kind, version) constraint so concurrent callers cannot be handed the
	// same version — see ARCHITECTURE.md and postgres/errors.go's doc
	// comment on ErrAlreadyExists. Must be called inside Registry.WithTx: a
	// unique-constraint collision aborts the whole transaction (the
	// "transaction abort" hazard PLAN.md flags), so a caller retrying on
	// ErrAlreadyExists MUST do so in a FRESH WithTx call, not the same one —
	// see server/handlers/artifact.go's AllocateVersion for the retry loop.
	AllocateVersion(ctx context.Context, kind ArtifactKind, ownerID, repository, increment, explicitVersion string) (*VersionAllocation, error)

	// BeginPublish is the ∅|allocated|failed|publishing -> publishing
	// transition (see ArtifactState's doc comment), identified by (kind,
	// ownerID, version). buildID is stamped on the row (refreshed on every
	// call, including the publishing -> publishing branch — see below).
	// repositoryHint is used only for the ∅ -> publishing branch (no prior
	// row exists — the pre-cutover path, or a domain that never called
	// AllocateVersion for this version): artifact.repository is NOT NULL,
	// so a fresh row needs one from somewhere, and an existing
	// allocated/failed/publishing row already carries its own from when it
	// was created. versionSource is likewise only used on the fresh-create
	// branch; an existing row keeps whatever AllocateVersion (or a prior
	// BeginPublish) already gave it. published is ErrFailedPrecondition.
	//
	// publishing -> publishing (AR-7d, issue #558) is a legal, idempotent
	// heartbeat/re-arm, not a rejection: it refreshes state_changed_at (and
	// build_id) without changing state. This exists because
	// BeginPublishBatch (AR-7d) stamps state_changed_at for every run
	// target at plan time, before the release matrix fans out — a target
	// whose matrix leg hasn't started yet is "publishing" from the moment
	// the batch call ran, not from when its own push begins. Without this
	// heartbeat, the stale-row reaper's timeout would have to cover the
	// whole run rather than one leg (see ENV.md's ARTIFACT_REAPER_TIMEOUT).
	// release.yml's per-leg BeginPublish call re-arms the clock immediately
	// before that leg's own push, restoring a one-leg budget for any leg
	// that actually runs; it also revives (failed -> publishing) a row the
	// reaper already expired, so a push that lost the race against the
	// reaper still gets recorded instead of failing at RecordArtifact after
	// an already-successful GHCR push.
	//
	// Must be called inside Registry.WithTx — same transactional-write
	// shape as every other write method here.
	BeginPublish(ctx context.Context, kind ArtifactKind, ownerID, version, buildID, repositoryHint string, versionSource VersionSource) (*Artifact, error)

	// FailPublish is the publishing -> failed transition, identified by
	// (kind, ownerID, version). reason is stored on FailReason. Any other
	// starting state is ErrFailedPrecondition. Must be called inside
	// Registry.WithTx.
	FailPublish(ctx context.Context, kind ArtifactKind, ownerID, version, reason string) (*Artifact, error)

	// ExpireStale moves every row in state "allocated" or "publishing"
	// whose state_changed_at is older than olderThan to "failed" with
	// FailReason "stale" — see ARCHITECTURE.md "The reaper is not
	// optional": a cancelled release run would otherwise hold a version
	// number in the (owner, kind, version) unique index forever. Returns
	// the number of rows moved. Called by app-registry-worker's periodic
	// reaper loop against the bare pool, NOT inside Registry.WithTx — same
	// pattern as WritebackRepository.ClaimBatch, a standalone maintenance
	// sweep with no other write to be atomic with.
	ExpireStale(ctx context.Context, olderThan time.Duration) (int, error)

	// AdoptArtifact is AR-7e's (issue #558) admin-only adoption /
	// disaster-recovery path: records a pre-existing GHCR image or chart as
	// "published" with Provenance ArtifactProvenanceAdopted, for when there
	// is no run to resume — the artifact was published before the registry
	// existed, or while the opt-in was off. See ARCHITECTURE.md "Adoption
	// and disaster recovery". a.Digest identifies the artifact; reason is
	// required at the handler layer (validated there, not persisted here —
	// same shape as AppRepository.SetAppStatus's reason parameter) and
	// actor authors any synthetic `build` row this call creates (see below).
	//
	// State-collision semantics, decided for this phase (there is no prior
	// art to extend — AdoptArtifact is the only writer of Provenance
	// ArtifactProvenanceAdopted):
	//
	//   - Idempotent replay: an existing PUBLISHED row (observed OR
	//     previously adopted) with this EXACT digest is "already recorded" —
	//     returned unchanged, alreadyRecorded=true. Adoption must NEVER
	//     rewrite an existing row's Provenance/State: downgrading an
	//     "observed" row to "adopted" after the fact would corrupt the exact
	//     audit trail this RPC exists to provide.
	//   - ∅ -> published (adopted): the primary case — no row of any kind
	//     exists yet for (owner, kind, version). A synthetic `build` row is
	//     created to satisfy artifact.build_id's NOT NULL-once-published
	//     constraint (migration 007's artifact_state_shape) and its foreign
	//     key, since by definition there is no real CI run behind a
	//     pre-registry artifact.
	//   - failed -> published (adopted): the disaster-recovery case — a run
	//     already tried (BeginPublish ran, then FailPublish or the reaper
	//     marked it "failed"), but the artifact demonstrably exists (an
	//     operator confirms it against GHCR/the chart repo by hand — this
	//     method never checks). If the failed row already carries a
	//     build_id (it does whenever the reaper reaped a "publishing" row;
	//     it does NOT when the reaper reaped an "allocated" row, which never
	//     had one), that REAL build_id is reused rather than minting a
	//     synthetic one — the CI run that actually attempted the push is the
	//     more honest provenance than a synthetic placeholder.
	//   - published (same digest): folded into the idempotent-replay case
	//     above.
	//   - published (different digest): ErrAlreadyExists — a real conflict.
	//     Adoption must not silently overwrite a different already-recorded
	//     digest, whether that row is observed or itself previously adopted;
	//     an operator must investigate by hand.
	//   - allocated or publishing: ErrFailedPrecondition. A live
	//     reservation or in-flight publish is not what adoption is for —
	//     let it complete, fail, or be reaped first (or FailPublish it
	//     explicitly), then retry. Adopting over live state would silently
	//     discard whatever process holds it.
	//
	// Must be called inside Registry.WithTx — same transactional-write shape
	// as every other write method here.
	AdoptArtifact(ctx context.Context, a Artifact, contains []ContainedImageInput, reason, actor string) (artifact *Artifact, alreadyRecorded bool, err error)
}

// DomainAdoptionRepository covers the `domain_adoption` table (migration
// 001), which gates the per-domain cutover described in ARCHITECTURE.md
// "Resolved questions" #3. Recording (AR-2) is never gated on this; only
// AllocateVersion (AR-5) is.
type DomainAdoptionRepository interface {
	// GetStage returns domain's current adoption stage, defaulting to
	// DomainAdoptionStageObserve when no row exists yet — every domain
	// starts at "observe" implicitly; a row is written only on explicit
	// cutover (there is no bulk-seed of one row per domain, unlike
	// `environment`'s dev/stage/prod seed).
	GetStage(ctx context.Context, domain string) (DomainAdoptionStage, error)
}

// IdempotencyRepository stores key -> serialized response for write RPCs.
// See ARCHITECTURE.md "Idempotency".
type IdempotencyRepository interface {
	// Get returns the stored response bytes for key, if present.
	Get(ctx context.Context, key string) (response []byte, found bool, err error)
	// Put stores key -> response. Called only after the write it guards has
	// succeeded, in the same transaction (see Registry.WithTx).
	Put(ctx context.Context, key, method string, response []byte) error
}

// EnvironmentRepository covers the `environment` table (AR-3b). Writes
// require auth.RoleAdmin at the handler layer -- ARCHITECTURE.md's
// Authorization table gives EnvironmentRegistry no builder/promoter
// carve-out. UpsertEnvironmentRequest and ArchiveEnvironmentRequest carry no
// idempotency_key (unlike ReconcileApps/RecordBuild/RecordArtifact), so
// these methods are not routed through runIdempotent -- same as
// AppRepository.SetAppStatus, the other admin-only write in this package.
type EnvironmentRepository interface {
	// Upsert creates an environment keyed by e.Key, or updates every field
	// but Key (the immutable identity) if one already exists. created
	// reports which happened. Does not change Archived -- only Archive does.
	Upsert(ctx context.Context, e Environment) (env *Environment, created bool, err error)

	Get(ctx context.Context, key string) (*Environment, error)

	// List returns environments ordered by rank ascending. includeArchived
	// controls whether archived rows are included, matching
	// ListEnvironmentsRequest.include_archived.
	List(ctx context.Context, includeArchived bool) ([]Environment, error)

	// Archive marks an environment archived. archived -> archived is a
	// no-op success, so a retried archive call is safe -- same idempotent
	// shape as AppRepository.SetAppStatus's archive path.
	Archive(ctx context.Context, key, reason string) (*Environment, error)
}

// PromotionRepository covers `promotion` (SCD2) and `promotion_event`
// (append-only) — see ARCHITECTURE.md "SCD2 on promotion". Every method
// here must be called from inside Registry.WithTx when it participates in a
// write: none of them open their own transaction, matching every other
// repository in this package.
type PromotionRepository interface {
	// Promote performs the SCD2 close-and-open write described in
	// AGENTS.md: it closes any current row for (p.EnvironmentID,
	// p.TargetKey) by setting its valid_to, then inserts p as the new
	// current row. current is the freshly written row; superseded is the
	// row that was just closed, or nil if this is the first promotion ever
	// recorded for this target. The partial unique index
	// promotion_current_idx is what makes two concurrent callers racing
	// this method structurally unable to both end up current.
	Promote(ctx context.Context, p Promotion) (current *Promotion, superseded *Promotion, err error)

	// GetCurrent returns the current (valid_to IS NULL) promotion for
	// (environmentID, targetKey), or ErrNotFound.
	GetCurrent(ctx context.Context, environmentID, targetKey string) (*Promotion, error)

	// GetPrevious returns the most recently superseded row for
	// (environmentID, targetKey) — the state Rollback re-promotes.
	// ErrNotFound when there is no history to roll back to (the target has
	// never been promoted, or has been promoted exactly once).
	GetPrevious(ctx context.Context, environmentID, targetKey string) (*Promotion, error)

	// StateAt returns every target's promotion row valid in environmentID
	// at instant at. at == nil means "now": rows with valid_to IS NULL —
	// this is the SCD2 window query from ARCHITECTURE.md "SCD2 on
	// promotion", parameterized so the same method serves both
	// GetEnvironmentState's current and historical (`at`) reads.
	StateAt(ctx context.Context, environmentID string, at *time.Time) ([]Promotion, error)

	// ListPromotions supports ListPromotionsRequest's filters. Ordered by
	// valid_from descending.
	ListPromotions(ctx context.Context, filter PromotionListFilter) ([]Promotion, error)

	// RecordEvent appends one row to promotion_event. Not SCD2 — see
	// AGENTS.md, event logs get their own shape.
	RecordEvent(ctx context.Context, e PromotionEvent) (*PromotionEvent, error)

	// ListEvents supports ListPromotionEventsRequest's filters. Ordered by
	// occurred_at descending.
	ListEvents(ctx context.Context, filter PromotionEventListFilter) ([]PromotionEvent, error)
}

// WritebackRepository covers `writeback_outbox` (AR-4b) -- see
// ARCHITECTURE.md "Writeback: outbox -> Temporal" and
// tools/app_registry/worker/README.md. Enqueue participates in the
// promotion transaction like every other write method in this package;
// ClaimBatch/MarkDone/MarkFailed are called by the worker against the bare
// pool (no Registry.WithTx involved -- the worker is a separate process
// with no promotion write to be atomic with).
type WritebackRepository interface {
	// Enqueue writes one outbox row. Callers MUST invoke this inside
	// Registry.WithTx, in the same transaction as the Promote/RecordEvent
	// call it follows -- see server/handlers/promotion.go's
	// enqueueWriteback. o.OutboxID is ignored/generated.
	Enqueue(ctx context.Context, o WritebackOutbox) (*WritebackOutbox, error)

	// ClaimBatch atomically claims up to limit rows for workerID: every
	// row currently 'pending', plus every 'claimed' row whose claimed_at
	// is older than staleAfter (a worker killed mid-run, per AR-4b's exit
	// criteria, leaves its claims to be reclaimed here). Implementations
	// must use a single atomic statement (e.g. `UPDATE ... WHERE outbox_id
	// IN (SELECT ... FOR UPDATE SKIP LOCKED)`) so concurrent workers never
	// claim the same row twice.
	ClaimBatch(ctx context.Context, workerID string, limit int, staleAfter time.Duration) ([]WritebackOutbox, error)

	// MarkDone marks outboxID done and records the workflow/run id that
	// carried it -- called after ExecuteWorkflow has either started the
	// WritebackWorkflow or confirmed (via Temporal's own dedup) that one
	// is already running/has run under that workflow id.
	MarkDone(ctx context.Context, outboxID, workflowID, runID string) error

	// MarkFailed records an error on outboxID and returns it to pending,
	// so the next ClaimBatch reclaims it immediately rather than waiting
	// out the staleness window -- distinct from a crash (which leaves the
	// row 'claimed' until staleAfter elapses).
	MarkFailed(ctx context.Context, outboxID, errMsg string) error

	// Get returns a single row by id, for tests and observability.
	Get(ctx context.Context, outboxID string) (*WritebackOutbox, error)
}

// Registry aggregates the per-entity repositories and provides a
// unit-of-work boundary. Handlers call WithTx to make a business operation
// (reconcile, idempotency check-and-store, etc.) atomic: fn receives a
// Registry bound to a single database transaction.
type Registry interface {
	Apps() AppRepository
	Builds() BuildRepository
	Artifacts() ArtifactRepository
	Idempotency() IdempotencyRepository
	Environments() EnvironmentRepository
	Promotions() PromotionRepository
	Writeback() WritebackRepository
	DomainAdoption() DomainAdoptionRepository

	WithTx(ctx context.Context, fn func(ctx context.Context, r Registry) error) error
}
