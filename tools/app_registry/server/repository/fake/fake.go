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

	"github.com/google/uuid"
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
	Apps        map[string]repository.App
	Charts      map[string]repository.Chart
	Builds      map[string]repository.Build
	Artifacts   map[string]repository.Artifact
	Idempotency map[string]idemEntry
}

func newState() *state {
	return &state{
		Apps:        map[string]repository.App{},
		Charts:      map[string]repository.Chart{},
		Builds:      map[string]repository.Build{},
		Artifacts:   map[string]repository.Artifact{},
		Idempotency: map[string]idemEntry{},
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

func (r *Registry) Apps() repository.AppRepository                { return r }
func (r *Registry) Builds() repository.BuildRepository            { return r }
func (r *Registry) Artifacts() repository.ArtifactRepository      { return r }
func (r *Registry) Idempotency() repository.IdempotencyRepository { return r }

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

func (r *Registry) Reconcile(ctx context.Context, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest, dryRun bool) (*repository.ReconcileResult, error) {
	return reconcile(r.state, apps, charts, dryRun)
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

func (r *Registry) SetAppStatus(ctx context.Context, appID string, status repository.Status, reason string) (*repository.App, error) {
	a, ok := r.state.Apps[appID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if status == repository.StatusActive && !r.presentInLatestReconcile(a) {
		return nil, repository.ErrFailedPrecondition
	}
	a.Status = status
	r.state.Apps[appID] = a
	return &a, nil
}

// presentInLatestReconcile mirrors the postgres implementation's rule:
// an app was present in the most recent Reconcile call iff its LastSeenAt
// equals the newest LastSeenAt across the whole app table.
func (r *Registry) presentInLatestReconcile(a repository.App) bool {
	var max = a.LastSeenAt
	for _, other := range r.state.Apps {
		if other.LastSeenAt.After(max) {
			max = other.LastSeenAt
		}
	}
	return !a.LastSeenAt.Before(max)
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

func (r *Registry) RecordArtifact(ctx context.Context, a repository.Artifact, contains []repository.ContainedImageInput) (*repository.Artifact, bool, error) {
	for _, existing := range r.state.Artifacts {
		if existing.Digest == a.Digest {
			out := existing
			out.Promotability = r.derivePromotability(existing)
			return &out, true, nil
		}
	}

	// Mirrors postgres's UNIQUE INDEX artifact_version_idx (owner_id, kind,
	// version): a fresh digest for an (owner, kind, version) already claimed
	// by a different artifact is a conflict, not a new row. This is the
	// AR-5 "someone else took this version" path — see
	// repository/postgres/errors.go's doc comment.
	if existing, found := r.findByOwnerKindVersion(a); found {
		return nil, false, fmt.Errorf("%w: artifact %s %s already recorded", repository.ErrAlreadyExists, r.ownerFullName(existing), existing.Version)
	}

	a.ArtifactID = uuid.NewString()

	if a.Kind == repository.ArtifactKindChart {
		links := make([]repository.ArtifactLink, 0, len(contains))
		for _, ci := range contains {
			img, err := r.findImageByDigest(ci.Digest)
			if err != nil {
				return nil, false, err
			}
			links = append(links, repository.ArtifactLink{
				ImageArtifactID: img.ArtifactID,
				AppID:           img.AppID,
				Repository:      ci.Repository,
				Version:         ci.Version,
				Digest:          ci.Digest,
			})
		}
		a.Contains = links
	}

	r.state.Artifacts[a.ArtifactID] = a
	out := a
	out.Promotability = r.derivePromotability(a)
	return &out, false, nil
}

// findByOwnerKindVersion looks up an existing artifact sharing a's owner
// (AppID for images, ChartID for charts), kind, and version — the collision
// the real artifact_version_idx unique index enforces.
func (r *Registry) findByOwnerKindVersion(a repository.Artifact) (repository.Artifact, bool) {
	owner := ownerID(a)
	for _, existing := range r.state.Artifacts {
		if existing.Kind == a.Kind && existing.Version == a.Version && ownerID(existing) == owner {
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

func (r *Registry) findImageByDigest(digest string) (*repository.Artifact, error) {
	for _, a := range r.state.Artifacts {
		if a.Digest == digest && a.Kind == repository.ArtifactKindImage {
			return &a, nil
		}
	}
	return nil, repository.ErrInvalidArgument
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
		return nil, nil, nil, repository.ErrInvalidArgument
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
