// Package fake is an in-memory implementation of repository.Registry, used
// by handler-level logic tests (see server/handlers/*_test.go).
//
// There is no Postgres-backed test in this repo under Bazel: the repo has no
// existing precedent for running a real Postgres instance inside a `bazel
// test` sandbox (manmanv2's `*_test.go` files under repository/postgres use
// an in-memory mock for the same reason — see server_port_test.go). Rather
// than add a new, unproven pattern, AR-2a follows the same approach: this
// fake exercises the business logic (reconcile diffing, promotability
// derivation, idempotency, chart/image validation) that handlers.go and
// postgres/*.go must agree on, while postgres/*.go itself is exercised only
// by `bazel build` (compiles against the same interfaces) — its SQL is not
// covered by an automated test in this change. See the AR-2a report for the
// explicit CI-vs-manual breakdown.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/semver"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

type idemEntry struct {
	Method   string
	Response []byte
}

// state is everything WithTx snapshots/restores. Kept as a separate struct
// (rather than fields directly on Registry) so it round-trips through JSON
// cleanly for a cheap deep copy.
type state struct {
	Apps   map[string]repository.App
	Charts map[string]repository.Chart
	Builds map[string]repository.Build
	// Artifacts holds every state an artifact row can be in
	// (allocated/publishing/published/failed -- see
	// repository.ArtifactState's doc comment). As of AR-7b (migration 007)
	// this is also where AllocateVersion writes its reservation -- the
	// separate version_allocation table/map this fake used to mirror is
	// gone, folded in here exactly as the real schema folded it into
	// `artifact`.
	Artifacts map[string]repository.Artifact
	// Idempotency is keyed by idempotency key, then by method -- mirroring
	// postgres's composite (idempotency_key, method) primary key (migration
	// 009, issue #575): a key reused across two different RPCs must resolve
	// each method independently, never let one replay the other's response.
	Idempotency     map[string]map[string]idemEntry
	Environments    map[string]repository.Environment    // keyed by environment_id
	Promotions      map[string]repository.Promotion      // keyed by promotion_id
	PromotionEvents map[string]repository.PromotionEvent // keyed by event_id
	// PromotionSyncEvents mirrors `promotion_sync_event` (migration 020,
	// issue #1028), keyed by sync_event_id -- the same flat, append-only
	// shape as PromotionEvents.
	PromotionSyncEvents map[string]repository.PromotionSyncEvent
	WritebackOutbox     map[string]repository.WritebackOutbox // keyed by outbox_id
	// DomainAdoption mirrors the `domain_adoption` table, keyed by domain.
	// Absent means DomainAdoptionStageObserve -- see
	// DomainAdoptionRepository.GetStage's doc comment.
	DomainAdoption map[string]repository.DomainAdoptionStage
	// Watermark mirrors the singleton reconcile_watermark row (migration
	// 006). nil means "no watermark yet" -- unlike postgres, the fake has
	// no seeded-sentinel-row concurrency concern to work around (WithTx
	// already serializes every call via the outer mutex, see WithTx's doc
	// comment), so this is a plain nilable pointer rather than a
	// GitSHA=="" sentinel value. reconcile.go's use of it must still agree
	// with repository.ShouldApplyReconcile's semantics for nil.
	Watermark *repository.ReconcileWatermark
	// ReconcileRuns mirrors the `reconcile_run` table (migration 010,
	// AR-8), keyed by reconcile_run_id. reconcile.go's reconcile() appends
	// one row here on every call that actually applies (never dry-run,
	// never SkippedStale) -- see postgres/app.go's Reconcile for the same
	// bookkeeping expressed in SQL.
	ReconcileRuns map[string]repository.ReconcileRun
	// ReleaseRuns mirrors `release_run` (migration 016, NFR4, issue #887),
	// keyed by release_run_id.
	ReleaseRuns map[string]repository.ReleaseRun
	// ReleaseRunTargets mirrors `release_run_target`, keyed by
	// release_run_target_id -- a flat map (not nested under ReleaseRuns) so
	// UpdateTargetState can look a target up directly by its own id, the
	// same shape Promotions/PromotionEvents use.
	ReleaseRunTargets map[string]repository.ReleaseRunTarget
	// AppBuildLogs mirrors `app_build_log` (migration 019, issue #923,
	// FR8-FR12/FR14), keyed by app_build_log_id. Written UNCONDITIONALLY on
	// every RecordBuildLog call (see AppBuildLogRepository's doc comment)
	// -- unlike every other SCD2-shaped map here, there is no
	// same-content-skip branch in the write path this mirrors.
	AppBuildLogs map[string]repository.AppBuildLog
}

func newState() *state {
	return &state{
		Apps:                map[string]repository.App{},
		Charts:              map[string]repository.Chart{},
		Builds:              map[string]repository.Build{},
		Artifacts:           map[string]repository.Artifact{},
		Idempotency:         map[string]map[string]idemEntry{},
		Environments:        map[string]repository.Environment{},
		Promotions:          map[string]repository.Promotion{},
		PromotionEvents:     map[string]repository.PromotionEvent{},
		PromotionSyncEvents: map[string]repository.PromotionSyncEvent{},
		WritebackOutbox:     map[string]repository.WritebackOutbox{},
		DomainAdoption:      map[string]repository.DomainAdoptionStage{},
		ReconcileRuns:       map[string]repository.ReconcileRun{},
		ReleaseRuns:         map[string]repository.ReleaseRun{},
		ReleaseRunTargets:   map[string]repository.ReleaseRunTarget{},
		AppBuildLogs:        map[string]repository.AppBuildLog{},
	}
}

func (s *state) clone() *state {
	// Deep copy via JSON round-trip: simple and correct for a test fake
	// whose fields are all JSON-marshalable (structs, maps, slices, time.Time).
	raw, err := json.Marshal(s)
	if err != nil {
		panic("fake: state failed to marshal: " + err.Error())
	}
	out := newState()
	if err := json.Unmarshal(raw, out); err != nil {
		panic("fake: state failed to unmarshal: " + err.Error())
	}
	return out
}

// Registry is the in-memory repository.Registry implementation.
type Registry struct {
	mu    *sync.Mutex
	state *state
}

// New constructs an empty fake Registry.
func New() *Registry {
	return &Registry{mu: &sync.Mutex{}, state: newState()}
}

// SetDomainAdoptionStage is a test-only fixture setter: there is no RPC to
// cut a domain over to a new adoption stage in this change (see PLAN.md's
// AR-5 status — AR-5a ships the gate but no cutover mechanism), so handler
// tests that need to exercise the "allocate" path reach in here directly,
// the same way postgres integration tests do via a raw
// `INSERT INTO domain_adoption` (see postgres_integration_artifact_test.go).
func (r *Registry) SetDomainAdoptionStage(domain string, stage repository.DomainAdoptionStage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.DomainAdoption[domain] = stage
}

// SeedReconcileRun is a test-only fixture setter, the same shape as
// SetDomainAdoptionStage above: ListReconcileRuns's duplicate-applied_at
// tie-break case needs two rows with the exact same AppliedAt, which the
// real write path (reconcile.go's reconcile(), via uuid.NewString() +
// time.Now()) cannot reliably construct without racing a clock -- so tests
// that need that exact shape reach in here directly instead.
func (r *Registry) SeedReconcileRun(run repository.ReconcileRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.ReconcileRuns[run.ReconcileRunID] = run
}

// SeedBuild is the ListBuilds analogue of SeedReconcileRun above (#611):
// ListBuilds's duplicate-recorded_at tie-break and NULL-started_at ordering
// cases both need an exact, caller-chosen RecordedAt (and, for the second
// case, a nil StartedAt) that the real write path (RecordBuild, whose
// postgres counterpart stamps RecordedAt = time.Now().UTC() itself -- see
// postgres/artifact.go's buildRepo.RecordBuild) cannot reliably construct
// without racing the clock. If BuildID is unset, one is generated so callers
// that don't care about the exact ID (most of them) can omit it.
func (r *Registry) SeedBuild(b repository.Build) repository.Build {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b.BuildID == "" {
		b.BuildID = uuid.NewString()
	}
	r.state.Builds[b.BuildID] = b
	return b
}

// SeedArtifact is the ListArtifacts analogue of SeedBuild above (issue
// #603): pagination tie-break/ordering cases need an exact, caller-chosen
// StateChangedAt that the real write paths (RecordArtifact et al., which
// stamp it from the clock) cannot reliably construct without racing it. If
// ArtifactID is unset, one is generated so callers that don't care about the
// exact ID can omit it.
func (r *Registry) SeedArtifact(a repository.Artifact) repository.Artifact {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.ArtifactID == "" {
		a.ArtifactID = uuid.NewString()
	}
	r.state.Artifacts[a.ArtifactID] = a
	return a
}

// SeedPromotion is the ListPromotions analogue of SeedBuild above (issue
// #603): pagination tie-break/ordering cases need an exact, caller-chosen
// ValidFrom that the real write path (Promote) stamps from the clock. If
// PromotionID is unset, one is generated.
func (r *Registry) SeedPromotion(p repository.Promotion) repository.Promotion {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.PromotionID == "" {
		p.PromotionID = uuid.NewString()
	}
	r.state.Promotions[p.PromotionID] = p
	return p
}

// SeedPromotionEvent is the ListPromotionEvents analogue of SeedBuild above
// (issue #603): pagination tie-break/ordering cases need an exact,
// caller-chosen OccurredAt that the real write path (RecordEvent) stamps
// from the clock. If EventID is unset, one is generated.
func (r *Registry) SeedPromotionEvent(e repository.PromotionEvent) repository.PromotionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.EventID == "" {
		e.EventID = uuid.NewString()
	}
	r.state.PromotionEvents[e.EventID] = e
	return e
}

func (r *Registry) Apps() repository.AppRepository                { return r }
func (r *Registry) Builds() repository.BuildRepository            { return r }
func (r *Registry) Artifacts() repository.ArtifactRepository      { return r }
func (r *Registry) Idempotency() repository.IdempotencyRepository { return r }

// Environments returns a distinct type, not Registry itself, because
// EnvironmentRepository.Get/List collide by name with IdempotencyRepository's
// Get and AppRepository's List-shaped methods if implemented directly on
// Registry -- Go has no method overloading. Every method still reads/writes
// r.state, the same map WithTx snapshots.
func (r *Registry) Environments() repository.EnvironmentRepository { return environmentFake{r} }

// Promotions returns a distinct type for the same reason Environments does.
func (r *Registry) Promotions() repository.PromotionRepository { return promotionFake{r} }

// Writeback returns a distinct type for the same reason Environments does.
func (r *Registry) Writeback() repository.WritebackRepository { return writebackFake{r} }

// DomainAdoption returns a distinct type for the same reason Environments
// does.
func (r *Registry) DomainAdoption() repository.DomainAdoptionRepository { return domainAdoptionFake{r} }

// ReleaseRuns returns a distinct type for the same reason Environments
// does.
func (r *Registry) ReleaseRuns() repository.ReleaseRunRepository { return releaseRunFake{r} }

// AppBuildLogs returns a distinct type for the same reason Environments
// does.
func (r *Registry) AppBuildLogs() repository.AppBuildLogRepository { return appBuildLogFake{r} }

// WithTx snapshots state, runs fn against a Registry sharing that snapshot,
// and commits the snapshot back only if fn succeeds — giving the fake the
// same all-or-nothing semantics a Postgres transaction provides.
func (r *Registry) WithTx(ctx context.Context, fn func(ctx context.Context, reg repository.Registry) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	txState := r.state.clone()
	tx := &Registry{mu: r.mu, state: txState}
	// fn must not re-lock r.mu; tx shares the outer lock so this is safe as
	// long as fn only calls methods on the Registry it was given.
	if err := fn(ctx, txHandle{tx}); err != nil {
		return err
	}
	r.state = txState
	return nil
}

// txHandle exists only so WithTx doesn't recursively re-enter r.mu.Lock()
// via a nested Registry.WithTx call from inside fn — none of the current
// handlers nest transactions, but this keeps the invariant explicit rather
// than relying on callers never doing so.
type txHandle struct{ *Registry }

func (h txHandle) WithTx(ctx context.Context, fn func(ctx context.Context, reg repository.Registry) error) error {
	return fn(ctx, h)
}

// ============================================================================
// AppRepository
// ============================================================================

func (r *Registry) Reconcile(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, source repository.ReconcileSource, dryRun bool) (*repository.ReconcileResult, error) {
	return reconcile(r.state, apps, charts, source, dryRun)
}

// AssertApps mirrors postgres's appRepo.AssertApps -- see
// repository.AppRepository.AssertApps's doc comment. source is accepted for
// interface-shape parity with Reconcile but unused: AssertApps never
// consults or advances the reconcile watermark.
func (r *Registry) AssertApps(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, source repository.ManifestSource) (*repository.AssertResult, error) {
	return assertApps(r.state, apps, charts), nil
}

func (r *Registry) ListApps(ctx context.Context, filter repository.AppListFilter) ([]repository.App, error) {
	statuses := defaultAppStatuses(filter.Statuses)
	var out []repository.App
	for _, a := range r.state.Apps {
		if filter.Domain != "" && a.Domain != filter.Domain {
			continue
		}
		if filter.DeployUnit != appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED && a.DeployUnit != filter.DeployUnit {
			continue
		}
		if !containsStatus(statuses, a.Status) {
			continue
		}
		out = append(out, a)
	}
	sortApps(out)
	return out, nil
}

func (r *Registry) GetAppByID(ctx context.Context, appID string) (*repository.App, error) {
	a, ok := r.state.Apps[appID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &a, nil
}

func (r *Registry) GetAppByFullName(ctx context.Context, fullName string) (*repository.App, error) {
	for _, a := range r.state.Apps {
		if a.FullName() == fullName {
			return &a, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *Registry) ChartsForApp(ctx context.Context, appID string) ([]repository.Chart, error) {
	var out []repository.Chart
	for _, c := range r.state.Charts {
		for _, id := range c.AppIDs {
			if id == appID {
				out = append(out, c)
				break
			}
		}
	}
	sortCharts(out)
	return out, nil
}

func (r *Registry) ListCharts(ctx context.Context, filter repository.ChartListFilter) ([]repository.Chart, error) {
	statuses := defaultAppStatuses(filter.Statuses)
	var out []repository.Chart
	for _, c := range r.state.Charts {
		if filter.Domain != "" && c.Domain != filter.Domain {
			continue
		}
		if !containsStatus(statuses, c.Status) {
			continue
		}
		out = append(out, c)
	}
	sortCharts(out)
	return out, nil
}

func (r *Registry) GetChartByID(ctx context.Context, chartID string) (*repository.Chart, error) {
	c, ok := r.state.Charts[chartID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &c, nil
}

func (r *Registry) GetChartByFullName(ctx context.Context, fullName string) (*repository.Chart, error) {
	for _, c := range r.state.Charts {
		if c.FullName() == fullName {
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

// GetAppManifestAppType is a stub for the fake repository. In a real
// implementation, this would look up the app_type from an app_manifest row.
// For testing, this just returns an empty string as a placeholder.
func (r *Registry) GetAppManifestAppType(ctx context.Context, manifestID string) (string, error) {
	// Stub implementation: just return empty string
	// Tests that need this will mock it or test with the postgres backend
	return "", repository.ErrNotFound
}

// SetAppStatus mirrors the postgres implementation: exactly one human-triage
// transition (missing -> archived) is legal. missing -> active happens
// automatically via Reconcile's "recovered" path, never through here.
// archived -> archived is a no-op success so a retried archive call is safe.
func (r *Registry) SetAppStatus(ctx context.Context, appID string, target repository.Status, reason string) (*repository.App, error) {
	a, ok := r.state.Apps[appID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if a.Status == repository.StatusArchived && target == repository.StatusArchived {
		return &a, nil
	}
	if a.Status != repository.StatusMissing || target != repository.StatusArchived {
		return nil, fmt.Errorf("%w: app %s is %s; only a missing app can be archived", repository.ErrFailedPrecondition, a.FullName(), a.Status)
	}
	a.Status = target
	r.state.Apps[appID] = a
	return &a, nil
}

// SetChartArgoApplicationNameOverride mirrors the postgres implementation:
// the only write path to Chart.ArgoApplicationNameOverrides, one
// environment at a time -- Reconcile never touches it here either. Builds a
// fresh map rather than mutating the stored one in place, so a Chart value
// handed out by an earlier Get/List call never sees this write (matching
// postgres's copy-on-read semantics via a fresh row scan). See
// repository.Chart.ResolveArgoApplicationName.
func (r *Registry) SetChartArgoApplicationNameOverride(ctx context.Context, chartID, environmentKey, argoApplicationName string) (*repository.Chart, error) {
	c, ok := r.state.Charts[chartID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	overrides := make(map[string]string, len(c.ArgoApplicationNameOverrides))
	for k, v := range c.ArgoApplicationNameOverrides {
		overrides[k] = v
	}
	if argoApplicationName == "" {
		delete(overrides, environmentKey)
	} else {
		overrides[environmentKey] = argoApplicationName
	}
	c.ArgoApplicationNameOverrides = overrides
	r.state.Charts[chartID] = c
	return &c, nil
}

// defaultReconcileRunPageSize matches postgres's appRepo default -- see that
// package's doc comment on the same constant.
const defaultReconcileRunPageSize = 50

// ListReconcileRuns mirrors postgres's appRepo.ListReconcileRuns: same
// ordering (applied_at DESC, reconcile_run_id DESC), same since filter, same
// real LIMIT + keyset cursor pagination contract -- see
// repository.AppRepository.ListReconcileRuns's doc comment. The fake's
// cursor format is its own implementation detail (see keyset_cursor.go in
// this package): only this fake's own round-trip needs to hold, not
// cross-compatibility with postgres's token format, since fake and postgres
// are never both queried with the same token.
func (r *Registry) ListReconcileRuns(ctx context.Context, since time.Time, pageSize int32, pageToken string) ([]repository.ReconcileRun, string, error) {
	if pageSize <= 0 {
		pageSize = defaultReconcileRunPageSize
	}

	var all []repository.ReconcileRun
	for _, rr := range r.state.ReconcileRuns {
		if !since.IsZero() && rr.AppliedAt.Before(since) {
			continue
		}
		all = append(all, rr)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].AppliedAt.Equal(all[j].AppliedAt) {
			return all[i].AppliedAt.After(all[j].AppliedAt)
		}
		return all[i].ReconcileRunID > all[j].ReconcileRunID
	})

	if pageToken != "" {
		cursorTS, cursorID, err := decodeFakeCursor(pageToken)
		if err != nil {
			return nil, "", err
		}
		var resumed []repository.ReconcileRun
		for _, rr := range all {
			if rr.AppliedAt.Before(cursorTS) || (rr.AppliedAt.Equal(cursorTS) && rr.ReconcileRunID < cursorID) {
				resumed = append(resumed, rr)
			}
		}
		all = resumed
	}

	var nextPageToken string
	if int32(len(all)) > pageSize {
		last := all[pageSize-1]
		nextPageToken = encodeFakeCursor(last.AppliedAt, last.ReconcileRunID)
		all = all[:pageSize]
	}
	return all, nextPageToken, nil
}

// ============================================================================
// BuildRepository
// ============================================================================

func (r *Registry) RecordBuild(ctx context.Context, b repository.Build) (*repository.Build, bool, error) {
	for _, existing := range r.state.Builds {
		if existing.WorkflowRunID == b.WorkflowRunID && existing.WorkflowAttempt == b.WorkflowAttempt {
			return &existing, true, nil
		}
	}
	b.BuildID = uuid.NewString()
	r.state.Builds[b.BuildID] = b
	return &b, false, nil
}

func (r *Registry) GetBuild(ctx context.Context, buildID string) (*repository.Build, error) {
	b, ok := r.state.Builds[buildID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &b, nil
}

// GetBuildByWorkflowRun mirrors postgres's buildRepo.GetBuildByWorkflowRun
// -- AR-7d (issue #558). attempt == 0 selects the highest workflow_attempt
// recorded for workflowRunID.
func (r *Registry) GetBuildByWorkflowRun(ctx context.Context, workflowRunID string, attempt int32) (*repository.Build, error) {
	var best *repository.Build
	for _, b := range r.state.Builds {
		b := b
		if b.WorkflowRunID != workflowRunID {
			continue
		}
		if attempt > 0 {
			if b.WorkflowAttempt == attempt {
				return &b, nil
			}
			continue
		}
		if best == nil || b.WorkflowAttempt > best.WorkflowAttempt {
			best = &b
		}
	}
	if best == nil {
		return nil, repository.ErrNotFound
	}
	return best, nil
}

// defaultBuildPageSize matches postgres's buildRepo default -- see that
// package's doc comment on the same constant.
const defaultBuildPageSize = 50

// ListBuilds mirrors postgres's buildRepo.ListBuilds: same ordering
// (recorded_at DESC, build_id DESC), same since filter, same real LIMIT +
// keyset cursor pagination contract -- see
// repository.BuildRepository.ListBuilds's doc comment. r.state.Builds is
// already fully populated by RecordBuild, so no new bookkeeping is needed
// here (unlike ListReconcileRuns, which needed a dedicated ReconcileRuns
// slice on state). Cursor format reuses this package's own
// encodeFakeCursor/decodeFakeCursor (keyset_cursor.go) -- only this fake's
// own round-trip needs to hold, not cross-compatibility with postgres's
// token format.
func (r *Registry) ListBuilds(ctx context.Context, since time.Time, pageSize int32, pageToken string) ([]repository.Build, string, error) {
	if pageSize <= 0 {
		pageSize = defaultBuildPageSize
	}

	var all []repository.Build
	for _, b := range r.state.Builds {
		if !since.IsZero() && b.RecordedAt.Before(since) {
			continue
		}
		all = append(all, b)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].RecordedAt.Equal(all[j].RecordedAt) {
			return all[i].RecordedAt.After(all[j].RecordedAt)
		}
		return all[i].BuildID > all[j].BuildID
	})

	if pageToken != "" {
		cursorTS, cursorID, err := decodeFakeCursor(pageToken)
		if err != nil {
			return nil, "", err
		}
		var resumed []repository.Build
		for _, b := range all {
			if b.RecordedAt.Before(cursorTS) || (b.RecordedAt.Equal(cursorTS) && b.BuildID < cursorID) {
				resumed = append(resumed, b)
			}
		}
		all = resumed
	}

	var nextPageToken string
	if int32(len(all)) > pageSize {
		last := all[pageSize-1]
		nextPageToken = encodeFakeCursor(last.RecordedAt, last.BuildID)
		all = all[:pageSize]
	}
	return all, nextPageToken, nil
}

// ============================================================================
// ArtifactRepository
// ============================================================================

// RecordArtifact mirrors postgres's artifactRepo.RecordArtifact -- the
// publishing -> published transition, plus (at adoption stage "observe"
// only) the backward-compatible direct-create path. See
// repository.ArtifactRepository.RecordArtifact's doc comment for the full
// state-machine contract.
func (r *Registry) RecordArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput, domainStage repository.DomainAdoptionStage) (*repository.Artifact, bool, error) {
	for _, existing := range r.state.Artifacts {
		// digest is empty on every non-published row (mirrors migration
		// 007's artifact_state_shape CHECK) -- guard explicitly rather than
		// relying on a.Digest never being "" too, which happens to be true
		// today but shouldn't be load-bearing.
		if existing.Digest != "" && existing.Digest == a.Digest && existing.Version == a.Version && existing.Kind == a.Kind && ownerID(existing) == ownerID(a) {
			// Promotability is derived LIVE (issue #833) -- re-derived here
			// from the owner's CURRENT deploy_unit rather than replayed
			// verbatim from the stored row, so a replay reflects any
			// deploy_unit edit (or DerivePromotability rule change) made
			// since the original publish, same as every other read path.
			out := r.livePromotability(existing)
			return &out, true, nil
		}
	}

	existing, found := r.findByOwnerKindVersion(a.Kind, ownerID(a), a.Version)
	if !found {
		// No row at all for this (owner, kind, version). Creating directly
		// as "published" is the pre-AR-7b behaviour, kept only for domains
		// still at adoption stage "observe" -- see ARCHITECTURE.md
		// "Backward compatibility during rollout".
		if domainStage != repository.DomainAdoptionStageObserve {
			return nil, false, fmt.Errorf("%w: no publishing artifact found for %s %s -- BeginPublish must run before RecordArtifact once a domain has left adoption stage \"observe\"",
				repository.ErrFailedPrecondition, r.ownerFullName(a), a.Version)
		}
		out, err := r.insertArtifact(a, contains, repository.ArtifactStatePublished, repository.VersionSourceTag, repository.ArtifactProvenanceObserved)
		return out, false, err
	}

	switch existing.State {
	case repository.ArtifactStatePublishing:
		out, err := r.completePublish(existing, a, contains)
		return out, false, err
	case repository.ArtifactStatePublished:
		// Reached only when the digest lookup above missed -- i.e. a
		// DIFFERENT digest than what is already published for this
		// (owner, kind, version). A real conflict, not a retry.
		return nil, false, fmt.Errorf("%w: artifact %s %s already published with digest %s",
			repository.ErrAlreadyExists, r.ownerFullName(existing), a.Version, existing.Digest)
	default: // allocated, failed
		return nil, false, fmt.Errorf("%w: artifact %s %s is %q -- BeginPublish must transition it to \"publishing\" before RecordArtifact can complete it",
			repository.ErrFailedPrecondition, r.ownerFullName(existing), a.Version, existing.State)
	}
}

// findByOwnerKindVersion looks up an existing artifact by owner (AppID for
// images, ChartID for charts), kind, and version — the identity
// BeginPublish/FailPublish/RecordArtifact all key off of, and the collision
// the real artifact_version_idx unique index enforces regardless of state.
func (r *Registry) findByOwnerKindVersion(kind repository.ArtifactKind, owner, version string) (repository.Artifact, bool) {
	for _, existing := range r.state.Artifacts {
		if existing.Kind == kind && existing.Version == version && ownerID(existing) == owner {
			return existing, true
		}
	}
	return repository.Artifact{}, false
}

func ownerID(a repository.Artifact) string {
	if a.Kind != repository.ArtifactKindChart {
		return a.AppID
	}
	return a.ChartID
}

// insertArtifact mirrors postgres's artifactRepo.insertArtifact: writes a
// brand-new artifact row directly in state, with the given versionSource/
// provenance -- shared by RecordArtifact's direct-create path (always
// ArtifactProvenanceObserved), BeginPublish's ∅ -> publishing branch,
// AllocateVersion's ∅ -> allocated write, and AdoptArtifact's (AR-7e, issue
// #558) ∅ -> published branch (ArtifactProvenanceAdopted). Promotability is
// derived live (issue #833) via livePromotability below, not stored --
// every read path re-derives it again anyway, so what is set here only
// matters for THIS call's own return value.
func (r *Registry) checkMajorMinorDigestCollision(a repository.Artifact) error {
	if a.Digest == "" {
		return nil
	}
	v, err := semver.Parse(a.Version)
	if err != nil {
		return nil
	}
	owner := ownerID(a)
	for _, existing := range r.state.Artifacts {
		if existing.ArtifactID == a.ArtifactID || existing.Digest != a.Digest || existing.Kind != a.Kind || ownerID(existing) != owner {
			continue
		}
		ev, err := semver.Parse(existing.Version)
		if err == nil && ev.Major == v.Major && ev.Minor == v.Minor {
			return fmt.Errorf("%w: artifact %s %s already published with digest %s in minor release v%d.%d",
				repository.ErrAlreadyExists, r.ownerFullName(existing), existing.Version, existing.Digest, ev.Major, ev.Minor)
		}
	}
	return nil
}

func (r *Registry) insertArtifact(a repository.Artifact, contains []repository.ContainedImageInput, state repository.ArtifactState, versionSource repository.VersionSource, provenance repository.ArtifactProvenance) (*repository.Artifact, error) {
	if state == repository.ArtifactStatePublished {
		if err := r.checkMajorMinorDigestCollision(a); err != nil {
			return nil, err
		}
	}
	a.ArtifactID = uuid.NewString()
	a.State = state
	a.Provenance = provenance
	a.VersionSource = versionSource
	a.StateChangedAt = timeNow()
	if state == repository.ArtifactStatePublished && a.PublishedAt.IsZero() {
		a.PublishedAt = timeNow()
	}
	a = r.livePromotability(a)

	if a.Kind == repository.ArtifactKindChart && state == repository.ArtifactStatePublished {
		links, err := r.linkContains(contains)
		if err != nil {
			return nil, err
		}
		a.Contains = links
	}

	r.state.Artifacts[a.ArtifactID] = a
	out := a
	return &out, nil
}

// completePublish mirrors postgres's artifactRepo.completePublish: the
// publishing -> published transition against an EXISTING row. Promotability
// is derived live (issue #833) -- see insertArtifact's doc comment above.
func (r *Registry) completePublish(existing, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, error) {
	updated := existing
	updated.Digest = a.Digest
	if err := r.checkMajorMinorDigestCollision(updated); err != nil {
		return nil, err
	}
	if a.BuildID != "" {
		updated.BuildID = a.BuildID
	}
	updated.PublishedAt = a.PublishedAt
	if updated.PublishedAt.IsZero() {
		updated.PublishedAt = timeNow()
	}
	updated.State = repository.ArtifactStatePublished
	updated.StateChangedAt = timeNow()
	updated = r.livePromotability(updated)

	if updated.Kind == repository.ArtifactKindChart {
		links, err := r.linkContains(contains)
		if err != nil {
			return nil, err
		}
		updated.Contains = links
	}

	r.state.Artifacts[updated.ArtifactID] = updated
	out := updated
	return &out, nil
}

// ============================================================================
// AdoptArtifact (AR-7e, issue #558)
// ============================================================================

// AdoptArtifact mirrors postgres's artifactRepo.AdoptArtifact -- the
// admin-only adoption / disaster-recovery path. See
// repository.ArtifactRepository.AdoptArtifact's doc comment for the full
// state-collision contract.
func (r *Registry) AdoptArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput, reason, actor string) (*repository.Artifact, bool, error) {
	// 1. Idempotent replay by (digest, owner, kind, version) -- see postgres's
	// AdoptArtifact step 1 for why Provenance/State are never rewritten here.
	// Promotability is re-derived live (issue #833), same as RecordArtifact's
	// matching replay branch above.
	for _, existing := range r.state.Artifacts {
		if existing.Digest != "" && existing.Digest == a.Digest && existing.Version == a.Version && existing.Kind == a.Kind && ownerID(existing) == ownerID(a) {
			out := r.livePromotability(existing)
			return &out, true, nil
		}
	}

	existing, found := r.findByOwnerKindVersion(a.Kind, ownerID(a), a.Version)
	if !found {
		out, err := r.adoptNew(a, contains, actor)
		return out, false, err
	}

	switch existing.State {
	case repository.ArtifactStateFailed, repository.ArtifactStatePublishing:
		out, err := r.completeAdoption(existing, a, contains, actor)
		return out, false, err
	case repository.ArtifactStatePublished:
		return nil, false, fmt.Errorf("%w: artifact %s %s already published with digest %s",
			repository.ErrAlreadyExists, r.ownerFullName(existing), a.Version, existing.Digest)
	default: // allocated
		return nil, false, fmt.Errorf("%w: artifact %s %s is %q -- AdoptArtifact only applies when there is no row, or the row is \"failed\" or \"publishing\"; a live allocation must be let run its course (or explicitly failed via FailPublish) before it can be adopted",
			repository.ErrFailedPrecondition, r.ownerFullName(existing), a.Version, existing.State)
	}
}

// createAdoptionBuild mirrors postgres's artifactRepo.createAdoptionBuild:
// a synthetic `build` row backing an adopted artifact, since by definition
// there is no real CI run behind a pre-registry artifact.
func (r *Registry) createAdoptionBuild(actor string) string {
	buildID := uuid.NewString()
	r.state.Builds[buildID] = repository.Build{
		BuildID:         buildID,
		GitRef:          "adopted",
		WorkflowRunID:   "adopted:" + uuid.NewString(),
		WorkflowAttempt: 1,
		Actor:           actor,
		RecordedAt:      timeNow(),
	}
	return buildID
}

// adoptNew mirrors postgres's artifactRepo.adoptNew: AdoptArtifact's ∅ ->
// published(adopted) branch.
func (r *Registry) adoptNew(a repository.Artifact, contains []repository.ContainedImageInput, actor string) (*repository.Artifact, error) {
	a.BuildID = r.createAdoptionBuild(actor)
	return r.insertArtifact(a, contains, repository.ArtifactStatePublished, repository.VersionSourceTag, repository.ArtifactProvenanceAdopted)
}

// completeAdoption mirrors postgres's artifactRepo.completeAdoption:
// AdoptArtifact's failed -> published(adopted) branch, reusing the existing
// row's build_id when it already has one (a "failed" row reaped from
// "publishing" does; one reaped from "allocated" does not).
func (r *Registry) completeAdoption(existing, a repository.Artifact, contains []repository.ContainedImageInput, actor string) (*repository.Artifact, error) {
	updated := existing
	updated.Digest = a.Digest
	if existing.BuildID != "" {
		updated.BuildID = existing.BuildID
	} else {
		updated.BuildID = r.createAdoptionBuild(actor)
	}
	updated.PublishedAt = a.PublishedAt
	if updated.PublishedAt.IsZero() {
		updated.PublishedAt = timeNow()
	}
	updated.State = repository.ArtifactStatePublished
	updated.Provenance = repository.ArtifactProvenanceAdopted
	updated.StateChangedAt = timeNow()
	updated.FailReason = ""
	updated = r.livePromotability(updated)

	if updated.Kind == repository.ArtifactKindChart {
		links, err := r.linkContains(contains)
		if err != nil {
			return nil, err
		}
		updated.Contains = links
	}

	r.state.Artifacts[updated.ArtifactID] = updated
	out := updated
	return &out, nil
}

// linkContains mirrors postgres's artifactRepo.linkContains: resolves each
// contains entry to an already-PUBLISHED image artifact, building the
// ArtifactLink slice a chart artifact carries. Unlike postgres this fake
// has no separate artifact_link table to write to -- Contains is stored
// directly on the chart's Artifact struct (see models.go).
func (r *Registry) linkContains(contains []repository.ContainedImageInput) ([]repository.ArtifactLink, error) {
	links := make([]repository.ArtifactLink, 0, len(contains))
	for _, ci := range contains {
		img, err := r.findImageByDigest(ci.Digest)
		if err != nil {
			return nil, err
		}
		links = append(links, repository.ArtifactLink{
			ImageArtifactID: img.ArtifactID,
			AppID:           img.AppID,
			Repository:      ci.Repository,
			Version:         ci.Version,
			Digest:          ci.Digest,
		})
	}
	return links, nil
}

// AllocateVersion mirrors postgres's artifactRepo.AllocateVersion. As of
// AR-7b this writes the ∅ -> allocated `artifact` row directly (via
// insertArtifact) instead of to a separate version_allocation map -- "next"
// is computed from the highest version already claimed for (ownerID, kind)
// against r.state.Artifacts alone, which now covers both already-published
// versions and reserved-but-not-yet-published ones in one place, so an
// allocated-but-not-yet-recorded version is never handed out twice.
func (r *Registry) AllocateVersion(ctx context.Context, kind repository.ArtifactKind, owner, repo, increment, explicitVersion string) (*repository.VersionAllocation, error) {
	var hasPrevious bool
	var previous semver.Version
	var latest repository.Artifact
	consider := func(a repository.Artifact) {
		v, err := semver.Parse(a.Version)
		if err != nil {
			return
		}
		if !hasPrevious || semver.Compare(v, previous) > 0 {
			hasPrevious = true
			previous = v
			latest = a
		}
	}
	for _, a := range r.state.Artifacts {
		if a.Kind == kind && owner == ownerID(a) {
			consider(a)
		}
	}

	// See postgres artifactRepo.AllocateVersion's identical comment: reuse
	// the highest version if (and only if) it already failed, instead of
	// minting a new one past it, so a retried release keeps retrying the
	// SAME version rather than permanently burning a version slot per
	// attempt. Deliberately NOT allocated/publishing -- those may still be
	// legitimately in flight from a concurrent caller.
	if explicitVersion == "" && hasPrevious && latest.State == repository.ArtifactStateFailed {
		return &repository.VersionAllocation{Version: latest.Version, PreviousVersion: r.latestPublishedVersion(kind, owner)}, nil
	}

	var next semver.Version
	if explicitVersion != "" {
		v, err := semver.ParseRelease(explicitVersion)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", repository.ErrInvalidArgument, err)
		}
		next = v
	} else {
		base := semver.Version{}
		if hasPrevious {
			base = previous
		}
		v, err := base.Increment(increment)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", repository.ErrInvalidArgument, err)
		}
		next = v
	}

	versionStr := next.String()
	if _, found := r.findByOwnerKindVersion(kind, owner, versionStr); found {
		return nil, fmt.Errorf("%w: version %s is already allocated or recorded for this owner", repository.ErrAlreadyExists, versionStr)
	}

	a := repository.Artifact{Kind: kind, Repository: repo, Version: versionStr}
	if kind != repository.ArtifactKindChart {
		a.AppID = owner
	} else {
		a.ChartID = owner
	}
	if _, err := r.insertArtifact(a, nil, repository.ArtifactStateAllocated, repository.VersionSourceRegistry, repository.ArtifactProvenanceObserved); err != nil {
		return nil, err
	}

	prevText := ""
	if hasPrevious {
		prevText = latest.Version
	}
	return &repository.VersionAllocation{Version: versionStr, PreviousVersion: prevText}, nil
}

// latestPublishedVersion mirrors postgres's artifactRepo.latestPublishedVersion.
func (r *Registry) latestPublishedVersion(kind repository.ArtifactKind, owner string) string {
	var hasPrev bool
	var prev semver.Version
	var prevText string
	for _, a := range r.state.Artifacts {
		if a.Kind != kind || owner != ownerID(a) || a.State != repository.ArtifactStatePublished {
			continue
		}
		v, err := semver.Parse(a.Version)
		if err != nil {
			continue
		}
		if !hasPrev || semver.Compare(v, prev) > 0 {
			hasPrev = true
			prev = v
			prevText = a.Version
		}
	}
	return prevText
}

// ============================================================================
// BeginPublish / FailPublish / ExpireStale (AR-7b)
// ============================================================================

// BeginPublish mirrors postgres's artifactRepo.BeginPublish -- the
// ∅|allocated|failed -> publishing transition.
func (r *Registry) BeginPublish(ctx context.Context, kind repository.ArtifactKind, owner, version, buildID, repositoryHint string, versionSource repository.VersionSource) (*repository.Artifact, error) {
	existing, found := r.findByOwnerKindVersion(kind, owner, version)
	if !found {
		if repositoryHint == "" {
			return nil, fmt.Errorf("%w: repository is required to begin publishing %s %s with no prior allocation", repository.ErrInvalidArgument, string(kind), version)
		}
		a := repository.Artifact{Kind: kind, Repository: repositoryHint, Version: version, BuildID: buildID}
		if kind != repository.ArtifactKindChart {
			a.AppID = owner
		} else {
			a.ChartID = owner
		}
		return r.insertArtifact(a, nil, repository.ArtifactStatePublishing, versionSource, repository.ArtifactProvenanceObserved)
	}

	// AR-7d (issue #558): publishing -> publishing is a legal, idempotent
	// heartbeat/re-arm, not a rejection -- see ArtifactState's doc comment.
	// Needed because AR-7d's plan-time BeginPublishBatch stamps
	// state_changed_at for every target at plan time, before the release
	// matrix fans out; without a per-leg heartbeat call re-arming it right
	// before that target's own push, the stale-row reaper could reap a row
	// whose leg simply hadn't started yet, using the WHOLE run's duration
	// as its budget instead of one leg's.
	if existing.State != repository.ArtifactStateAllocated &&
		existing.State != repository.ArtifactStateFailed &&
		existing.State != repository.ArtifactStatePublishing {
		return nil, fmt.Errorf("%w: artifact %s %s is %q, not \"allocated\", \"failed\", or \"publishing\" -- BeginPublish cannot start from here",
			repository.ErrFailedPrecondition, r.ownerFullName(existing), version, existing.State)
	}

	existing.BuildID = buildID
	existing.State = repository.ArtifactStatePublishing
	existing.StateChangedAt = timeNow()
	existing.FailReason = ""
	r.state.Artifacts[existing.ArtifactID] = existing
	out := existing
	return &out, nil
}

// FailPublish mirrors postgres's artifactRepo.FailPublish -- the
// publishing -> failed transition.
func (r *Registry) FailPublish(ctx context.Context, kind repository.ArtifactKind, owner, version, reason string) (*repository.Artifact, error) {
	existing, found := r.findByOwnerKindVersion(kind, owner, version)
	if !found {
		return nil, fmt.Errorf("%w: no artifact found for %s %s %s", repository.ErrFailedPrecondition, string(kind), owner, version)
	}
	if existing.State != repository.ArtifactStatePublishing {
		return nil, fmt.Errorf("%w: artifact %s %s is %q, not \"publishing\" -- FailPublish cannot start from here",
			repository.ErrFailedPrecondition, r.ownerFullName(existing), version, existing.State)
	}
	existing.State = repository.ArtifactStateFailed
	existing.StateChangedAt = timeNow()
	existing.FailReason = reason
	r.state.Artifacts[existing.ArtifactID] = existing
	out := existing
	return &out, nil
}

// ExpireStale mirrors postgres's artifactRepo.ExpireStale -- the reaper's
// sweep.
func (r *Registry) ExpireStale(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := timeNow().Add(-olderThan)
	n := 0
	for id, a := range r.state.Artifacts {
		if (a.State == repository.ArtifactStateAllocated || a.State == repository.ArtifactStatePublishing) && a.StateChangedAt.Before(cutoff) {
			a.State = repository.ArtifactStateFailed
			a.StateChangedAt = timeNow()
			a.FailReason = "stale"
			r.state.Artifacts[id] = a
			n++
		}
	}
	return n, nil
}

func (r *Registry) findImageByDigest(digest string) (*repository.Artifact, error) {
	for _, a := range r.state.Artifacts {
		if a.Digest == digest && a.Kind == repository.ArtifactKindImage && a.State == repository.ArtifactStatePublished {
			return &a, nil
		}
	}
	// Mirrors postgres/artifact.go's RecordArtifact chart-links error.
	return nil, fmt.Errorf("%w: chart pins unrecorded image digest %s", repository.ErrInvalidArgument, digest)
}

// defaultArtifactPageSize matches postgres's artifactRepo default -- see
// that package's doc comment on the same constant.
const defaultArtifactPageSize = 50

// ListArtifacts mirrors postgres's artifactRepo.ListArtifacts: same
// ordering (state_changed_at DESC, artifact_id DESC), same filters, same
// real LIMIT + keyset cursor pagination contract -- see
// repository.ArtifactRepository.ListArtifacts's doc comment. Cursor format
// reuses this package's own encodeFakeCursor/decodeFakeCursor
// (keyset_cursor.go) -- only this fake's own round-trip needs to hold, not
// cross-compatibility with postgres's token format.
func (r *Registry) ListArtifacts(ctx context.Context, filter repository.ArtifactListFilter, pageSize int32, pageToken string) ([]repository.Artifact, string, error) {
	if pageSize <= 0 {
		pageSize = defaultArtifactPageSize
	}

	var all []repository.Artifact
	for _, a := range r.state.Artifacts {
		if filter.Kind != "" && a.Kind != filter.Kind {
			continue
		}
		if filter.OwnerFullName != "" {
			owner := r.ownerFullName(a)
			if owner != filter.OwnerFullName {
				continue
			}
		}
		// GetReleaseRun's (AR-7d, issue #558) filter -- every artifact
		// hanging off one build, any state.
		if filter.BuildID != "" && a.BuildID != filter.BuildID {
			continue
		}
		// AR-7e (issue #558): "which rows did we take on faith?" as a query.
		if filter.Provenance != "" && a.Provenance != filter.Provenance {
			continue
		}
		// Promotability is derived LIVE (issue #833) from the owner's
		// CURRENT deploy_unit, not read off a stored value -- so an edit to
		// an app's deploy_unit (or a DerivePromotability rule change) is
		// reflected in this filter immediately, for every artifact
		// regardless of when it was published.
		a = r.livePromotability(a)
		if filter.PromotableOnly && a.Promotability != repository.PromotabilityPromotable {
			continue
		}
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].StateChangedAt.Equal(all[j].StateChangedAt) {
			return all[i].StateChangedAt.After(all[j].StateChangedAt)
		}
		return all[i].ArtifactID > all[j].ArtifactID
	})

	if pageToken != "" {
		cursorTS, cursorID, err := decodeFakeCursor(pageToken)
		if err != nil {
			return nil, "", err
		}
		var resumed []repository.Artifact
		for _, a := range all {
			if a.StateChangedAt.Before(cursorTS) || (a.StateChangedAt.Equal(cursorTS) && a.ArtifactID < cursorID) {
				resumed = append(resumed, a)
			}
		}
		all = resumed
	}

	var nextPageToken string
	if int32(len(all)) > pageSize {
		last := all[pageSize-1]
		nextPageToken = encodeFakeCursor(last.StateChangedAt, last.ArtifactID)
		all = all[:pageSize]
	}
	return all, nextPageToken, nil
}

func (r *Registry) GetArtifact(ctx context.Context, lookup repository.ArtifactLookup) (*repository.Artifact, error) {
	a, err := r.findArtifact(lookup)
	if err != nil {
		return nil, err
	}
	cp := *a
	return &cp, nil
}

// findArtifact is the single choke point every read path (GetArtifact,
// ResolveArtifact's primary lookup, ListArtifactPins' primary lookup) goes
// through, so livePromotability only needs applying here to cover all of
// them -- issue #833: Promotability is derived live, never read off a
// stored value.
func (r *Registry) findArtifact(lookup repository.ArtifactLookup) (*repository.Artifact, error) {
	if lookup.ArtifactID != "" {
		a, ok := r.state.Artifacts[lookup.ArtifactID]
		if !ok {
			return nil, repository.ErrNotFound
		}
		a = r.livePromotability(a)
		return &a, nil
	}
	if lookup.Digest != "" {
		for _, a := range r.state.Artifacts {
			if a.Digest == lookup.Digest {
				a = r.livePromotability(a)
				return &a, nil
			}
		}
		return nil, repository.ErrNotFound
	}
	if lookup.OwnerFullName != "" && lookup.LatestPublished {
		var candidates []repository.Artifact
		var beforeVer semver.Version
		var hasBefore bool
		if lookup.BeforeVersion != "" {
			if v, err := semver.Parse(lookup.BeforeVersion); err == nil {
				beforeVer = v
				hasBefore = true
			}
		}
		for _, a := range r.state.Artifacts {
			if a.Kind == lookup.Kind && a.State == repository.ArtifactStatePublished && r.ownerFullName(a) == lookup.OwnerFullName {
				if hasBefore {
					v, err := semver.Parse(a.Version)
					if err != nil || semver.Compare(v, beforeVer) >= 0 {
						continue
					}
				}
				candidates = append(candidates, a)
			}
		}
		if len(candidates) == 0 {
			return nil, repository.ErrNotFound
		}
		sort.Slice(candidates, func(i, j int) bool {
			vi, erri := semver.Parse(candidates[i].Version)
			vj, errj := semver.Parse(candidates[j].Version)
			if erri != nil || errj != nil {
				return candidates[i].Version > candidates[j].Version
			}
			return semver.Compare(vi, vj) > 0
		})
		best := r.livePromotability(candidates[0])
		return &best, nil
	}
	if lookup.OwnerFullName != "" && lookup.Version != "" {
		for _, a := range r.state.Artifacts {
			if a.Kind == lookup.Kind && a.Version == lookup.Version && r.ownerFullName(a) == lookup.OwnerFullName {
				a = r.livePromotability(a)
				return &a, nil
			}
		}
		return nil, repository.ErrNotFound
	}
	return nil, repository.ErrInvalidArgument
}

func (r *Registry) ResolveArtifact(ctx context.Context, lookup repository.ArtifactLookup) (*repository.Artifact, []repository.Artifact, []repository.Build, error) {
	a, err := r.findArtifact(lookup)
	if err != nil {
		return nil, nil, nil, err
	}
	if a.Kind != repository.ArtifactKindChart {
		// Mirrors postgres/artifact.go's ResolveArtifact.
		return nil, nil, nil, fmt.Errorf("%w: artifact %s is not a chart", repository.ErrInvalidArgument, a.ArtifactID)
	}
	cp := *a

	var images []repository.Artifact
	var builds []repository.Build
	for _, link := range a.Contains {
		img, ok := r.state.Artifacts[link.ImageArtifactID]
		if !ok {
			continue
		}
		images = append(images, r.livePromotability(img))
		if b, ok := r.state.Builds[img.BuildID]; ok {
			builds = append(builds, b)
		}
	}
	return &cp, images, builds, nil
}

// ListArtifactPins is ResolveArtifact's mirror image: given an image
// artifact lookup, returns the chart artifacts whose Contains links back to
// it. The fake has no separate artifact_link table to walk (see
// linkContains above), so this scans chart artifacts' Contains slices
// instead of joining.
func (r *Registry) ListArtifactPins(ctx context.Context, lookup repository.ArtifactLookup) ([]repository.Artifact, error) {
	a, err := r.findArtifact(lookup)
	if err != nil {
		return nil, err
	}
	if a.Kind != repository.ArtifactKindImage {
		// Mirrors postgres/artifact.go's ListArtifactPins.
		return nil, fmt.Errorf("%w: artifact %s is not an image", repository.ErrInvalidArgument, a.ArtifactID)
	}

	var chartArtifacts []repository.Artifact
	for _, candidate := range r.state.Artifacts {
		if candidate.Kind != repository.ArtifactKindChart {
			continue
		}
		for _, link := range candidate.Contains {
			if link.ImageArtifactID == a.ArtifactID {
				chartArtifacts = append(chartArtifacts, r.livePromotability(candidate))
				break
			}
		}
	}
	return chartArtifacts, nil
}

func (r *Registry) ownerFullName(a repository.Artifact) string {
	if a.Kind != repository.ArtifactKindChart {
		if app, ok := r.state.Apps[a.AppID]; ok {
			return app.FullName()
		}
	} else {
		if c, ok := r.state.Charts[a.ChartID]; ok {
			return c.FullName()
		}
	}
	return ""
}

// derivePromotability implements ARCHITECTURE.md "Promotability" via the
// shared repository.DerivePromotability rule.
func (r *Registry) derivePromotability(a repository.Artifact) repository.Promotability {
	var du appmetapb.DeployUnit
	if a.Kind != repository.ArtifactKindChart {
		app, ok := r.state.Apps[a.AppID]
		if !ok {
			return repository.PromotabilityNotPromotable
		}
		du = app.DeployUnit
	} else {
		c, ok := r.state.Charts[a.ChartID]
		if !ok {
			return repository.PromotabilityNotPromotable
		}
		du = c.DeployUnit
	}
	return repository.DerivePromotability(a.Kind, du)
}

// livePromotability derives a's Promotability live from the owner's CURRENT
// deploy_unit (issue #833) and returns the updated copy. Applied at every
// point an Artifact leaves the registry -- read paths (findArtifact,
// ListArtifacts, ResolveArtifact, ListArtifactPins) and write-path replay
// branches (RecordArtifact/AdoptArtifact's idempotent digest match) alike --
// so a deploy_unit edit (or a DerivePromotability rule change, e.g. #810) is
// reflected on the very next call, including for an artifact published
// before the edit. Mirrors postgres/artifact.go's scanArtifact: only
// published rows get a derived value; allocated/publishing/failed rows
// report "" (nothing to derive it from until publish).
func (r *Registry) livePromotability(a repository.Artifact) repository.Artifact {
	if a.State == repository.ArtifactStatePublished {
		a.Promotability = r.derivePromotability(a)
	} else {
		a.Promotability = ""
	}
	return a
}

// ============================================================================
// IdempotencyRepository
// ============================================================================

func (r *Registry) Get(ctx context.Context, key, method string) ([]byte, bool, error) {
	e, ok := r.state.Idempotency[key][method]
	if !ok {
		return nil, false, nil
	}
	return e.Response, true, nil
}

func (r *Registry) Put(ctx context.Context, key, method string, response []byte) error {
	if r.state.Idempotency[key] == nil {
		r.state.Idempotency[key] = map[string]idemEntry{}
	}
	r.state.Idempotency[key][method] = idemEntry{Method: method, Response: response}
	return nil
}

// ============================================================================
// EnvironmentRepository
// ============================================================================

// environmentFake implements repository.EnvironmentRepository over the same
// *Registry.state every other repository reads/writes -- see Environments's
// doc comment for why this can't be a set of methods directly on Registry.
type environmentFake struct{ r *Registry }

// Upsert mirrors postgres's environmentRepo.Upsert: creates by Key, or
// updates every field but Key (the immutable identity) and Archived (only
// Archive changes that) if a row already exists.
func (f environmentFake) Upsert(ctx context.Context, e repository.Environment) (*repository.Environment, bool, error) {
	if existing, ok := f.findByKey(e.Key); ok {
		updated := existing
		updated.DisplayName = e.DisplayName
		updated.Rank = e.Rank
		updated.RequiresApproval = e.RequiresApproval
		updated.GitopsPath = e.GitopsPath
		updated.AllowedPrincipals = e.AllowedPrincipals
		f.r.state.Environments[updated.EnvironmentID] = updated
		return &updated, false, nil
	}

	e.EnvironmentID = uuid.NewString()
	e.Archived = false
	f.r.state.Environments[e.EnvironmentID] = e
	return &e, true, nil
}

func (f environmentFake) Get(ctx context.Context, key string) (*repository.Environment, error) {
	if e, ok := f.findByKey(key); ok {
		return &e, nil
	}
	return nil, repository.ErrNotFound
}

func (f environmentFake) List(ctx context.Context, includeArchived bool) ([]repository.Environment, error) {
	var out []repository.Environment
	for _, e := range f.r.state.Environments {
		if !includeArchived && e.Archived {
			continue
		}
		out = append(out, e)
	}
	sortEnvironments(out)
	return out, nil
}

// Archive mirrors postgres's environmentRepo.Archive: archived -> archived
// is a no-op success so a retried archive call is safe. reason is accepted
// (and required at the handler layer) but not persisted, same as
// SetAppStatus's reason today.
func (f environmentFake) Archive(ctx context.Context, key, reason string) (*repository.Environment, error) {
	e, ok := f.findByKey(key)
	if !ok {
		return nil, repository.ErrNotFound
	}
	if e.Archived {
		return &e, nil
	}
	e.Archived = true
	f.r.state.Environments[e.EnvironmentID] = e
	return &e, nil
}

func (f environmentFake) findByKey(key string) (repository.Environment, bool) {
	for _, e := range f.r.state.Environments {
		if e.Key == key {
			return e, true
		}
	}
	return repository.Environment{}, false
}

func sortEnvironments(envs []repository.Environment) {
	sort.Slice(envs, func(i, j int) bool { return envs[i].Rank < envs[j].Rank })
}

// ============================================================================
// PromotionRepository
// ============================================================================

// promotionFake implements repository.PromotionRepository over the same
// *Registry.state every other repository reads/writes -- see Environments's
// doc comment for why this can't be methods directly on Registry.
type promotionFake struct{ r *Registry }

// Promote mirrors postgres's promotionRepo.Promote: close whatever is
// currently valid_to == nil for (p.EnvironmentID, p.TargetKey), then insert
// p as the new current row.
func (f promotionFake) Promote(ctx context.Context, p repository.Promotion) (*repository.Promotion, *repository.Promotion, error) {
	var superseded *repository.Promotion
	if existing, ok := f.findCurrent(p.EnvironmentID, p.TargetKey); ok {
		now := timeNow()
		existing.ValidTo = &now
		f.r.state.Promotions[existing.PromotionID] = existing
		superseded = &existing
	}

	p.PromotionID = uuid.NewString()
	p.ValidFrom = timeNow()
	p.ValidTo = nil
	if p.State == "" {
		p.State = repository.PromotionStateActive
	}
	f.r.state.Promotions[p.PromotionID] = p

	current := p
	return &current, superseded, nil
}

func (f promotionFake) GetCurrent(ctx context.Context, environmentID, targetKey string) (*repository.Promotion, error) {
	if p, ok := f.findCurrent(environmentID, targetKey); ok {
		return &p, nil
	}
	return nil, repository.ErrNotFound
}

func (f promotionFake) GetPrevious(ctx context.Context, environmentID, targetKey string) (*repository.Promotion, error) {
	var best *repository.Promotion
	for _, p := range f.r.state.Promotions {
		if p.EnvironmentID != environmentID || p.TargetKey != targetKey || p.ValidTo == nil {
			continue
		}
		if best == nil || p.ValidFrom.After(best.ValidFrom) {
			cp := p
			best = &cp
		}
	}
	if best == nil {
		return nil, repository.ErrNotFound
	}
	return best, nil
}

func (f promotionFake) StateAt(ctx context.Context, environmentID string, at *time.Time) ([]repository.Promotion, error) {
	var out []repository.Promotion
	for _, p := range f.r.state.Promotions {
		if p.EnvironmentID != environmentID {
			continue
		}
		if at == nil {
			if p.ValidTo == nil {
				out = append(out, p)
			}
			continue
		}
		if !p.ValidFrom.After(*at) && (p.ValidTo == nil || p.ValidTo.After(*at)) {
			out = append(out, p)
		}
	}
	sortPromotions(out)
	return out, nil
}

// defaultPromotionPageSize matches postgres's promotionRepo default -- see
// that package's doc comment on the same constant.
const defaultPromotionPageSize = 50

// ListPromotions mirrors postgres's promotionRepo.ListPromotions: same
// ordering (valid_from DESC, promotion_id DESC), same filters, same real
// LIMIT + keyset cursor pagination contract -- see
// repository.PromotionRepository.ListPromotions's doc comment. Cursor
// format reuses this package's own encodeFakeCursor/decodeFakeCursor
// (keyset_cursor.go) -- only this fake's own round-trip needs to hold, not
// cross-compatibility with postgres's token format.
func (f promotionFake) ListPromotions(ctx context.Context, filter repository.PromotionListFilter, pageSize int32, pageToken string) ([]repository.Promotion, string, error) {
	if pageSize <= 0 {
		pageSize = defaultPromotionPageSize
	}

	var all []repository.Promotion
	for _, p := range f.r.state.Promotions {
		if filter.EnvironmentKey != "" && p.EnvironmentKey != filter.EnvironmentKey {
			continue
		}
		if filter.OwnerFullName != "" && f.ownerFullName(p) != filter.OwnerFullName {
			continue
		}
		if !filter.IncludeHistory && p.ValidTo != nil {
			continue
		}
		all = append(all, p)
	}
	sortPromotions(all)

	if pageToken != "" {
		cursorTS, cursorID, err := decodeFakeCursor(pageToken)
		if err != nil {
			return nil, "", err
		}
		var resumed []repository.Promotion
		for _, p := range all {
			if p.ValidFrom.Before(cursorTS) || (p.ValidFrom.Equal(cursorTS) && p.PromotionID < cursorID) {
				resumed = append(resumed, p)
			}
		}
		all = resumed
	}

	var nextPageToken string
	if int32(len(all)) > pageSize {
		last := all[pageSize-1]
		nextPageToken = encodeFakeCursor(last.ValidFrom, last.PromotionID)
		all = all[:pageSize]
	}
	return all, nextPageToken, nil
}

func (f promotionFake) RecordEvent(ctx context.Context, e repository.PromotionEvent) (*repository.PromotionEvent, error) {
	e.EventID = uuid.NewString()
	if e.OccurredAt.IsZero() {
		e.OccurredAt = timeNow()
	}
	f.r.state.PromotionEvents[e.EventID] = e
	return &e, nil
}

// defaultPromotionEventPageSize matches postgres's promotionRepo default --
// see that package's doc comment on the same constant.
const defaultPromotionEventPageSize = 50

// ListEvents mirrors postgres's promotionRepo.ListEvents: same ordering
// (occurred_at DESC, event_id DESC), same filters, same real LIMIT +
// keyset cursor pagination contract -- see
// repository.PromotionRepository.ListEvents's doc comment. Cursor format
// reuses this package's own encodeFakeCursor/decodeFakeCursor
// (keyset_cursor.go) -- only this fake's own round-trip needs to hold, not
// cross-compatibility with postgres's token format.
func (f promotionFake) ListEvents(ctx context.Context, filter repository.PromotionEventListFilter, pageSize int32, pageToken string) ([]repository.PromotionEvent, string, error) {
	if pageSize <= 0 {
		pageSize = defaultPromotionEventPageSize
	}

	var all []repository.PromotionEvent
	for _, e := range f.r.state.PromotionEvents {
		if filter.PromotionID != "" && e.PromotionID != filter.PromotionID {
			continue
		}
		if filter.Actor != "" && e.Actor != filter.Actor {
			continue
		}
		if !filter.Since.IsZero() && e.OccurredAt.Before(filter.Since) {
			continue
		}
		if filter.EnvironmentKey != "" || filter.OwnerFullName != "" {
			p, ok := f.r.state.Promotions[e.PromotionID]
			if !ok {
				continue
			}
			if filter.EnvironmentKey != "" && p.EnvironmentKey != filter.EnvironmentKey {
				continue
			}
			if filter.OwnerFullName != "" && f.ownerFullName(p) != filter.OwnerFullName {
				continue
			}
		}
		all = append(all, e)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].OccurredAt.Equal(all[j].OccurredAt) {
			return all[i].OccurredAt.After(all[j].OccurredAt)
		}
		return all[i].EventID > all[j].EventID
	})

	if pageToken != "" {
		cursorTS, cursorID, err := decodeFakeCursor(pageToken)
		if err != nil {
			return nil, "", err
		}
		var resumed []repository.PromotionEvent
		for _, e := range all {
			if e.OccurredAt.Before(cursorTS) || (e.OccurredAt.Equal(cursorTS) && e.EventID < cursorID) {
				resumed = append(resumed, e)
			}
		}
		all = resumed
	}

	var nextPageToken string
	if int32(len(all)) > pageSize {
		last := all[pageSize-1]
		nextPageToken = encodeFakeCursor(last.OccurredAt, last.EventID)
		all = all[:pageSize]
	}
	return all, nextPageToken, nil
}

func (f promotionFake) findCurrent(environmentID, targetKey string) (repository.Promotion, bool) {
	for _, p := range f.r.state.Promotions {
		if p.EnvironmentID == environmentID && p.TargetKey == targetKey && p.ValidTo == nil {
			return p, true
		}
	}
	return repository.Promotion{}, false
}

func (f promotionFake) ownerFullName(p repository.Promotion) string {
	if p.Kind != repository.ArtifactKindChart {
		if app, ok := f.r.state.Apps[p.AppID]; ok {
			return app.FullName()
		}
	} else {
		if c, ok := f.r.state.Charts[p.ChartID]; ok {
			return c.FullName()
		}
	}
	return ""
}

// ============================================================================
// WritebackRepository
// ============================================================================

// writebackFake implements repository.WritebackRepository over the same
// *Registry.state every other repository reads/writes -- see Environments's
// doc comment for why this can't be methods directly on Registry.
type writebackFake struct{ r *Registry }

func (f writebackFake) Enqueue(ctx context.Context, o repository.WritebackOutbox) (*repository.WritebackOutbox, error) {
	o.OutboxID = uuid.NewString()
	o.Status = repository.WritebackOutboxStatusPending
	o.CreatedAt = timeNow()
	f.r.state.WritebackOutbox[o.OutboxID] = o
	out := o
	return &out, nil
}

// ClaimBatch mirrors postgres's writebackRepo.ClaimBatch: pending rows, plus
// claimed rows whose claimed_at predates staleAfter, oldest created_at
// first, up to limit.
func (f writebackFake) ClaimBatch(ctx context.Context, workerID string, limit int, staleAfter time.Duration) ([]repository.WritebackOutbox, error) {
	staleBefore := timeNow().Add(-staleAfter)
	var eligible []repository.WritebackOutbox
	for _, o := range f.r.state.WritebackOutbox {
		if o.Status == repository.WritebackOutboxStatusPending {
			eligible = append(eligible, o)
			continue
		}
		if o.Status == repository.WritebackOutboxStatusClaimed && o.ClaimedAt != nil && o.ClaimedAt.Before(staleBefore) {
			eligible = append(eligible, o)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].CreatedAt.Before(eligible[j].CreatedAt) })
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}

	claimedAt := timeNow()
	out := make([]repository.WritebackOutbox, 0, len(eligible))
	for _, o := range eligible {
		o.Status = repository.WritebackOutboxStatusClaimed
		o.ClaimedBy = workerID
		o.ClaimedAt = &claimedAt
		o.Attempts++
		f.r.state.WritebackOutbox[o.OutboxID] = o
		out = append(out, o)
	}
	return out, nil
}

func (f writebackFake) MarkDone(ctx context.Context, outboxID, workflowID, runID string) error {
	o, ok := f.r.state.WritebackOutbox[outboxID]
	if !ok {
		return repository.ErrNotFound
	}
	o.Status = repository.WritebackOutboxStatusDone
	o.WorkflowID = workflowID
	completed := timeNow()
	o.CompletedAt = &completed
	f.r.state.WritebackOutbox[outboxID] = o
	return nil
}

func (f writebackFake) MarkFailed(ctx context.Context, outboxID, errMsg string) error {
	o, ok := f.r.state.WritebackOutbox[outboxID]
	if !ok {
		return repository.ErrNotFound
	}
	o.Status = repository.WritebackOutboxStatusPending
	o.LastError = errMsg
	o.ClaimedBy = ""
	o.ClaimedAt = nil
	f.r.state.WritebackOutbox[outboxID] = o
	return nil
}

func (f writebackFake) Get(ctx context.Context, outboxID string) (*repository.WritebackOutbox, error) {
	o, ok := f.r.state.WritebackOutbox[outboxID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &o, nil
}

// RecordResult implements repository.WritebackRepository.RecordResult
// (FR7a, issue #1029), mirroring postgres's writebackRepo.RecordResult:
// matches by promotionID (not outboxID -- see that method's doc comment),
// and leaves Status/CompletedAt untouched.
func (f writebackFake) RecordResult(ctx context.Context, outboxID, promotionID, location, commitSHA string) error {
	for id, o := range f.r.state.WritebackOutbox {
		if o.PromotionID != promotionID {
			continue
		}
		o.Location = location
		o.CommitSHA = commitSHA
		f.r.state.WritebackOutbox[id] = o
		return nil
	}
	return repository.ErrNotFound
}

func sortPromotions(promotions []repository.Promotion) {
	sort.Slice(promotions, func(i, j int) bool {
		if !promotions[i].ValidFrom.Equal(promotions[j].ValidFrom) {
			return promotions[i].ValidFrom.After(promotions[j].ValidFrom)
		}
		return promotions[i].PromotionID > promotions[j].PromotionID
	})
}

// timeNow is a thin indirection so a future test could freeze it; today it's
// just time.Now().UTC(), matching every other fake's timestamp handling.
func timeNow() time.Time { return time.Now().UTC() }

// ============================================================================
// DomainAdoptionRepository
// ============================================================================

// domainAdoptionFake implements repository.DomainAdoptionRepository over
// the same *Registry.state every other repository reads/writes -- see
// Environments's doc comment for why this can't be a method directly on
// Registry.
type domainAdoptionFake struct{ r *Registry }

// GetStage mirrors postgres's domainAdoptionRepo.GetStage: an absent row
// (the default for every domain in a fresh fake, matching production where
// domain_adoption seeds no rows automatically) means
// DomainAdoptionStageObserve.
func (f domainAdoptionFake) GetStage(ctx context.Context, domain string) (repository.DomainAdoptionStage, error) {
	if stage, ok := f.r.state.DomainAdoption[domain]; ok {
		return stage, nil
	}
	return repository.DomainAdoptionStageObserve, nil
}

// ============================================================================
// helpers shared with reconcile.go
// ============================================================================

func defaultAppStatuses(statuses []repository.Status) []repository.Status {
	if len(statuses) == 0 {
		return []repository.Status{repository.StatusActive, repository.StatusMissing}
	}
	return statuses
}

func containsStatus(statuses []repository.Status, s repository.Status) bool {
	for _, st := range statuses {
		if st == s {
			return true
		}
	}
	return false
}

func sortApps(apps []repository.App) {
	sort.Slice(apps, func(i, j int) bool { return apps[i].FullName() < apps[j].FullName() })
}

func sortCharts(charts []repository.Chart) {
	sort.Slice(charts, func(i, j int) bool { return charts[i].FullName() < charts[j].FullName() })
}
