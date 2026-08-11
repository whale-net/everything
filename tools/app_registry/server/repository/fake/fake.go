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
	Artifacts       map[string]repository.Artifact
	Idempotency     map[string]idemEntry
	Environments    map[string]repository.Environment     // keyed by environment_id
	Promotions      map[string]repository.Promotion       // keyed by promotion_id
	PromotionEvents map[string]repository.PromotionEvent  // keyed by event_id
	WritebackOutbox map[string]repository.WritebackOutbox // keyed by outbox_id
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
}

func newState() *state {
	return &state{
		Apps:            map[string]repository.App{},
		Charts:          map[string]repository.Chart{},
		Builds:          map[string]repository.Build{},
		Artifacts:       map[string]repository.Artifact{},
		Idempotency:     map[string]idemEntry{},
		Environments:    map[string]repository.Environment{},
		Promotions:      map[string]repository.Promotion{},
		PromotionEvents: map[string]repository.PromotionEvent{},
		WritebackOutbox: map[string]repository.WritebackOutbox{},
		DomainAdoption:  map[string]repository.DomainAdoptionStage{},
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
// `INSERT INTO domain_adoption` (see postgres_integration_test.go).
func (r *Registry) SetDomainAdoptionStage(domain string, stage repository.DomainAdoptionStage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.DomainAdoption[domain] = stage
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
		if existing.Digest != "" && existing.Digest == a.Digest {
			out := existing
			out.Promotability = r.derivePromotability(existing)
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
		out, err := r.insertArtifact(a, contains, repository.ArtifactStatePublished, repository.VersionSourceTag)
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
	if a.Kind == repository.ArtifactKindImage {
		return a.AppID
	}
	return a.ChartID
}

// insertArtifact mirrors postgres's artifactRepo.insertArtifact: writes a
// brand-new artifact row directly in state, with versionSource and
// Provenance "observed" -- shared by RecordArtifact's direct-create path,
// BeginPublish's ∅ -> publishing branch, and AllocateVersion's ∅ ->
// allocated write.
func (r *Registry) insertArtifact(a repository.Artifact, contains []repository.ContainedImageInput, state repository.ArtifactState, versionSource repository.VersionSource) (*repository.Artifact, error) {
	a.ArtifactID = uuid.NewString()
	a.State = state
	a.Provenance = repository.ArtifactProvenanceObserved
	a.VersionSource = versionSource
	a.StateChangedAt = timeNow()
	if state == repository.ArtifactStatePublished && a.PublishedAt.IsZero() {
		a.PublishedAt = timeNow()
	}

	if a.Kind == repository.ArtifactKindChart && state == repository.ArtifactStatePublished {
		links, err := r.linkContains(contains)
		if err != nil {
			return nil, err
		}
		a.Contains = links
	}

	r.state.Artifacts[a.ArtifactID] = a
	out := a
	out.Promotability = r.derivePromotability(a)
	return &out, nil
}

// completePublish mirrors postgres's artifactRepo.completePublish: the
// publishing -> published transition against an EXISTING row.
func (r *Registry) completePublish(existing, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, error) {
	updated := existing
	updated.Digest = a.Digest
	if a.BuildID != "" {
		updated.BuildID = a.BuildID
	}
	updated.PublishedAt = a.PublishedAt
	if updated.PublishedAt.IsZero() {
		updated.PublishedAt = timeNow()
	}
	updated.State = repository.ArtifactStatePublished
	updated.StateChangedAt = timeNow()

	if updated.Kind == repository.ArtifactKindChart {
		links, err := r.linkContains(contains)
		if err != nil {
			return nil, err
		}
		updated.Contains = links
	}

	r.state.Artifacts[updated.ArtifactID] = updated
	out := updated
	out.Promotability = r.derivePromotability(updated)
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
	var previousText string
	consider := func(text string) {
		v, err := semver.Parse(text)
		if err != nil {
			return
		}
		if !hasPrevious || semver.Compare(v, previous) > 0 {
			hasPrevious = true
			previous = v
			previousText = text
		}
	}
	for _, a := range r.state.Artifacts {
		if a.Kind == kind && owner == ownerID(a) {
			consider(a.Version)
		}
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
	if kind == repository.ArtifactKindImage {
		a.AppID = owner
	} else {
		a.ChartID = owner
	}
	if _, err := r.insertArtifact(a, nil, repository.ArtifactStateAllocated, repository.VersionSourceRegistry); err != nil {
		return nil, err
	}

	return &repository.VersionAllocation{Version: versionStr, PreviousVersion: previousText}, nil
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
		if kind == repository.ArtifactKindImage {
			a.AppID = owner
		} else {
			a.ChartID = owner
		}
		return r.insertArtifact(a, nil, repository.ArtifactStatePublishing, versionSource)
	}

	if existing.State != repository.ArtifactStateAllocated && existing.State != repository.ArtifactStateFailed {
		return nil, fmt.Errorf("%w: artifact %s %s is %q, not \"allocated\" or \"failed\" -- BeginPublish cannot start from here",
			repository.ErrFailedPrecondition, r.ownerFullName(existing), version, existing.State)
	}

	existing.BuildID = buildID
	existing.State = repository.ArtifactStatePublishing
	existing.StateChangedAt = timeNow()
	existing.FailReason = ""
	r.state.Artifacts[existing.ArtifactID] = existing
	out := existing
	out.Promotability = r.derivePromotability(existing)
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
	out.Promotability = r.derivePromotability(existing)
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

func (r *Registry) ListArtifacts(ctx context.Context, filter repository.ArtifactListFilter) ([]repository.Artifact, error) {
	var out []repository.Artifact
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
		a.Promotability = r.derivePromotability(a)
		if filter.PromotableOnly && a.Promotability != repository.PromotabilityPromotable {
			continue
		}
		out = append(out, a)
	}
	sortArtifacts(out)
	return out, nil
}

func (r *Registry) GetArtifact(ctx context.Context, lookup repository.ArtifactLookup) (*repository.Artifact, error) {
	a, err := r.findArtifact(lookup)
	if err != nil {
		return nil, err
	}
	cp := *a
	cp.Promotability = r.derivePromotability(cp)
	return &cp, nil
}

func (r *Registry) findArtifact(lookup repository.ArtifactLookup) (*repository.Artifact, error) {
	if lookup.ArtifactID != "" {
		a, ok := r.state.Artifacts[lookup.ArtifactID]
		if !ok {
			return nil, repository.ErrNotFound
		}
		return &a, nil
	}
	if lookup.Digest != "" {
		for _, a := range r.state.Artifacts {
			if a.Digest == lookup.Digest {
				return &a, nil
			}
		}
		return nil, repository.ErrNotFound
	}
	if lookup.OwnerFullName != "" {
		for _, a := range r.state.Artifacts {
			if a.Kind == lookup.Kind && a.Version == lookup.Version && r.ownerFullName(a) == lookup.OwnerFullName {
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
	cp.Promotability = r.derivePromotability(cp)

	var images []repository.Artifact
	var builds []repository.Build
	for _, link := range a.Contains {
		img, ok := r.state.Artifacts[link.ImageArtifactID]
		if !ok {
			continue
		}
		img.Promotability = r.derivePromotability(img)
		images = append(images, img)
		if b, ok := r.state.Builds[img.BuildID]; ok {
			builds = append(builds, b)
		}
	}
	return &cp, images, builds, nil
}

func (r *Registry) ownerFullName(a repository.Artifact) string {
	if a.Kind == repository.ArtifactKindImage {
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
	if a.Kind == repository.ArtifactKindImage {
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

// ============================================================================
// IdempotencyRepository
// ============================================================================

func (r *Registry) Get(ctx context.Context, key string) ([]byte, bool, error) {
	e, ok := r.state.Idempotency[key]
	if !ok {
		return nil, false, nil
	}
	return e.Response, true, nil
}

func (r *Registry) Put(ctx context.Context, key, method string, response []byte) error {
	r.state.Idempotency[key] = idemEntry{Method: method, Response: response}
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

func (f promotionFake) ListPromotions(ctx context.Context, filter repository.PromotionListFilter) ([]repository.Promotion, error) {
	var out []repository.Promotion
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
		out = append(out, p)
	}
	sortPromotions(out)
	return out, nil
}

func (f promotionFake) RecordEvent(ctx context.Context, e repository.PromotionEvent) (*repository.PromotionEvent, error) {
	e.EventID = uuid.NewString()
	if e.OccurredAt.IsZero() {
		e.OccurredAt = timeNow()
	}
	f.r.state.PromotionEvents[e.EventID] = e
	return &e, nil
}

func (f promotionFake) ListEvents(ctx context.Context, filter repository.PromotionEventListFilter) ([]repository.PromotionEvent, error) {
	var out []repository.PromotionEvent
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
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OccurredAt.After(out[j].OccurredAt) })
	return out, nil
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
	if p.Kind == repository.ArtifactKindImage {
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

func sortPromotions(promotions []repository.Promotion) {
	sort.Slice(promotions, func(i, j int) bool { return promotions[i].ValidFrom.After(promotions[j].ValidFrom) })
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

func sortArtifacts(artifacts []repository.Artifact) {
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].PublishedAt.Equal(artifacts[j].PublishedAt) {
			return artifacts[i].Digest < artifacts[j].Digest
		}
		return artifacts[i].PublishedAt.Before(artifacts[j].PublishedAt)
	})
}
