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
}

// ArtifactRepository covers `artifact` and `artifact_link`.
type ArtifactRepository interface {
	// RecordArtifact inserts an artifact, or — if one with the same digest
	// already exists — returns it unchanged with alreadyRecorded=true (the
	// digest is the natural key; this mirrors BuildRepository.RecordBuild).
	// For kind == chart, every entry in contains must already be recorded
	// (matched by digest) or the call fails with ErrInvalidArgument — a
	// chart may not pin an unknown artifact.
	RecordArtifact(ctx context.Context, a Artifact, contains []ContainedImageInput) (artifact *Artifact, alreadyRecorded bool, err error)

	ListArtifacts(ctx context.Context, filter ArtifactListFilter) ([]Artifact, error)
	GetArtifact(ctx context.Context, lookup ArtifactLookup) (*Artifact, error)

	// ResolveArtifact walks a chart artifact to its pinned image artifacts
	// and their originating builds. lookup identifies the chart artifact by
	// artifact_id or digest.
	ResolveArtifact(ctx context.Context, lookup ArtifactLookup) (artifact *Artifact, images []Artifact, builds []Build, err error)

	// AllocateVersion reserves the next version for (kind, ownerID) per
	// ARCHITECTURE.md's version model (AR-5) and PLAN.md's AR-5 addendum:
	// increment is "major"|"minor"|"patch" and is ignored when
	// explicitVersion is set. Implementations must perform this as a
	// transactional INSERT against a unique (owner, kind, version)
	// constraint so concurrent callers cannot be handed the same version —
	// see ARCHITECTURE.md and postgres/errors.go's doc comment on
	// ErrAlreadyExists. Must be called inside Registry.WithTx: a
	// unique-constraint collision aborts the whole transaction (the
	// "transaction abort" hazard PLAN.md flags), so a caller retrying on
	// ErrAlreadyExists MUST do so in a FRESH WithTx call, not the same one —
	// see server/handlers/artifact.go's AllocateVersion for the retry loop.
	AllocateVersion(ctx context.Context, kind ArtifactKind, ownerID, increment, explicitVersion string) (*VersionAllocation, error)
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
