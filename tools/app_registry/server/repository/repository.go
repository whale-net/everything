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
)

// AppRepository covers App and Chart identity — the two tables ReconcileApps
// writes together, since a chart's chart_app join depends on app rows
// existing first.
type AppRepository interface {
	// Reconcile performs the FULL replace described in ARCHITECTURE.md: every
	// app/chart in the manifest set is created or updated and set ACTIVE;
	// anything known but absent is marked MISSING (ARCHIVED rows are left
	// alone); anything MISSING that reappears is reported as recovered.
	// chart.apps entries are resolved to app_ids and the call fails with
	// ErrInvalidArgument if any name is unknown. dryRun computes the result
	// without writing.
	Reconcile(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, dryRun bool) (*ReconcileResult, error)

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

	WithTx(ctx context.Context, fn func(ctx context.Context, r Registry) error) error
}
