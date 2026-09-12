//go:build integration

// Real-Postgres coverage for MediatedWriteStore (mediated.go, issue #2546,
// FR9/FR10/NFR2) -- the mediated-intake write path: a producer-role Agent
// turns a Requirement Contributor's plain-language submission into
// Feature/Requirement rows, with the entity create(s) and the describing
// revision_event sharing exactly one transaction. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" build tag.
//
// This is its own go_test target (not folded into design_session_
// integration_test) because it is the one test target that needs the full
// Product -> FeatureSet -> Feature chain (feature.go/featureset.go)
// alongside DesignSession/RevisionEvent, plus //krill/slice for the FR9
// whole-product-slice-visibility proof -- a combination no other target
// carries. Helpers below are a deliberate small duplicate of design_session_
// integration_test.go's (each go_test target in this package compiles only
// its own listed srcs, so helpers are not shared across targets -- see that
// file's dsTestSubject comment).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:mediated_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newMediatedTestStore provisions an isolated, migrated Postgres database
// (krill's own real embedded schema, every migration) and returns a ready
// *store.Store plus the underlying dbtest.Postgres.
func newMediatedTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(db.Pool), db
}

func newMediatedTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres, repoFullName string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, repoFullName).Scan(&id))
	return id
}

func mediatedTestSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

// mintMediatedKrillSession opens a real krill_session row via
// SessionStore.InitSession -- design_session.opened_by_krill_session_id
// requires a genuine krill_session id, not a fabricated uuid.
func mintMediatedKrillSession(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID uuid.UUID, subject store.Subject) store.SessionID {
	t.Helper()
	id, err := store.NewSessionStore(db.Pool).InitSession(ctx, scopeID, subject, subject, nil)
	require.NoError(t, err)
	return id
}

// medFixture bundles what every MediatedWriteStore test needs: a store, its
// scope, a real Product -> FeatureSet -> Feature chain, and a real
// design_session id opened against that Product -- so proposals can attach
// either to the existing Feature (ParentID) or to a Feature proposed in the
// same call (ParentProposalIndex).
type medFixture struct {
	store        *store.Store
	db           *dbtest.Postgres
	scopeID      uuid.UUID
	productID    uuid.UUID
	featureSetID uuid.UUID
	featureID    uuid.UUID
	sessionID    uuid.UUID
}

func newMedFixture(t *testing.T, repoFullName string) medFixture {
	t.Helper()
	ctx := context.Background()

	s, db := newMediatedTestStore(t)
	scopeID := newMediatedTestScope(t, ctx, db, repoFullName)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)

	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "M2", nil)
	require.NoError(t, err)

	feature, err := s.Features().Create(ctx, scopeID, fs.ID, "Mediated intake", nil)
	require.NoError(t, err)

	krillSessionID := mintMediatedKrillSession(t, ctx, db, scopeID, mediatedTestSubject("agent-1"))
	ds, err := s.DesignSessions().Open(ctx, scopeID, product.ID, "As a Requirement Contributor, I want mediated intake.", krillSessionID)
	require.NoError(t, err)

	return medFixture{
		store:        s,
		db:           db,
		scopeID:      scopeID,
		productID:    product.ID,
		featureSetID: fs.ID,
		featureID:    feature.ID,
		sessionID:    ds.ID,
	}
}

// baseMediatedProposal returns a MediatedProposal with SessionID/ScopeID
// from f, event_type draft (required verified_against attached), and no
// proposals -- tests append what they need to Proposals.
func baseMediatedProposal(f medFixture, acting, onBehalfOf store.Subject) store.MediatedProposal {
	verified := "abc123def"
	return store.MediatedProposal{
		SessionID:       f.sessionID,
		ScopeID:         f.scopeID,
		Acting:          acting,
		OnBehalfOf:      onBehalfOf,
		EventType:       store.EventTypeDraft,
		VerifiedAgainst: &verified,
	}
}

func countRows(t *testing.T, f medFixture, table string) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

// TestMediatedWriteStore_ProposeEntities_CreatesAllAndOneRevisionEvent is
// this issue's Testing case 1: a proposal of one Feature plus two
// Requirements (the requirements attached via ParentProposalIndex to the
// feature proposed in the same call) creates all three and exactly one
// revision_event, whose entity_deltas names all three ids with
// change:"created".
func TestMediatedWriteStore_ProposeEntities_CreatesAllAndOneRevisionEvent(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-create-all-test")
	agent := mediatedTestSubject("producer-agent")
	contributor := mediatedTestSubject("requirement-contributor")

	p := baseMediatedProposal(f, agent, contributor)
	p.Proposals = []store.MediatedEntityProposal{
		{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "Mediated Feature", Position: 1, SummaryLine: "proposed a feature"},
		{Kind: store.MediatedEntityKindRequirement, ParentProposalIndex: intPtr(0), Name: "FR: does the thing", Position: 1, RequirementKind: store.RequirementKindFR, SummaryLine: "proposed FR"},
		{Kind: store.MediatedEntityKindRequirement, ParentProposalIndex: intPtr(0), Name: "NFR: does it fast", Position: 2, RequirementKind: store.RequirementKindNFR, SummaryLine: "proposed NFR"},
	}

	ev, entities, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
	require.NoError(t, err)
	require.Len(t, entities, 3)

	gotIDs := make(map[uuid.UUID]bool, 3)
	for _, e := range entities {
		gotIDs[e.ID] = true
	}
	require.Len(t, ev.EntityDeltas, 3, "exactly one revision_event must describe all three creates")
	for _, d := range ev.EntityDeltas {
		assert.Equal(t, store.EntityDeltaChangeCreated, d.Change)
		assert.True(t, gotIDs[d.EntityID], "every entity_delta must name one of the created entities")
	}

	list, err := f.store.RevisionEvents().ListBySession(ctx, f.sessionID)
	require.NoError(t, err)
	assert.Len(t, list, 1, "exactly one revision_event must have been appended")
}

// TestMediatedWriteStore_ProposeEntities_InvalidLastParent_LeavesZeroRows is
// this issue's Testing case 2 (atomicity, NFR2): a proposal whose last item
// has an invalid parent leaves zero entities and zero revision events
// behind. Verified by direct row counts, not by the returned error alone.
func TestMediatedWriteStore_ProposeEntities_InvalidLastParent_LeavesZeroRows(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-atomicity-test")
	agent := mediatedTestSubject("producer-agent")
	contributor := mediatedTestSubject("requirement-contributor")

	beforeFeatures := countRows(t, f, "feature")
	beforeRequirements := countRows(t, f, "requirement")
	beforeEvents := countRows(t, f, "revision_event")

	unknownFeature := uuid.New()
	p := baseMediatedProposal(f, agent, contributor)
	p.Proposals = []store.MediatedEntityProposal{
		{Kind: store.MediatedEntityKindRequirement, ParentID: &f.featureID, Name: "FR: valid parent", Position: 1, RequirementKind: store.RequirementKindFR, SummaryLine: "ok"},
		{Kind: store.MediatedEntityKindRequirement, ParentID: &unknownFeature, Name: "FR: invalid parent", Position: 2, RequirementKind: store.RequirementKindFR, SummaryLine: "bad"},
	}

	_, _, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
	assert.ErrorIs(t, err, store.ErrNotFound)

	assert.Equal(t, beforeFeatures, countRows(t, f, "feature"), "no feature row may survive a rolled-back proposal")
	assert.Equal(t, beforeRequirements, countRows(t, f, "requirement"), "the earlier, individually-valid requirement in this batch must not survive either")
	assert.Equal(t, beforeEvents, countRows(t, f, "revision_event"), "no revision_event may survive a rolled-back proposal")
}

// TestMediatedWriteStore_ProposeEntities_EveryReturnedIDAppearsInADelta is
// this issue's Testing case 3, the NFR2 invariant test written as a
// property-style assertion over a multi-proposal fixture: after every
// successful ProposeEntities call, every entity id it returned appears in
// some revision_event.entity_deltas for that session.
func TestMediatedWriteStore_ProposeEntities_EveryReturnedIDAppearsInADelta(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-nfr2-invariant-test")
	agent := mediatedTestSubject("producer-agent")
	contributor := mediatedTestSubject("requirement-contributor")

	batches := [][]store.MediatedEntityProposal{
		{
			{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "Batch 1 Feature", Position: 1, SummaryLine: "f1"},
		},
		{
			{Kind: store.MediatedEntityKindRequirement, ParentID: &f.featureID, Name: "Batch 2 FR A", Position: 1, RequirementKind: store.RequirementKindFR, SummaryLine: "fr-a"},
			{Kind: store.MediatedEntityKindRequirement, ParentID: &f.featureID, Name: "Batch 2 FR B", Position: 2, RequirementKind: store.RequirementKindNFR, SummaryLine: "nfr-b"},
		},
		{
			{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "Batch 3 Feature", Position: 2, SummaryLine: "f3"},
			{Kind: store.MediatedEntityKindRequirement, ParentProposalIndex: intPtr(0), Name: "Batch 3 FR", Position: 1, RequirementKind: store.RequirementKindFR, SummaryLine: "fr3"},
		},
	}

	var allReturnedIDs []uuid.UUID
	for _, proposals := range batches {
		p := baseMediatedProposal(f, agent, contributor)
		p.Proposals = proposals
		_, entities, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
		require.NoError(t, err)
		for _, e := range entities {
			allReturnedIDs = append(allReturnedIDs, e.ID)
		}
	}
	require.Len(t, allReturnedIDs, 5, "sanity: three batches totalling five proposals")

	events, err := f.store.RevisionEvents().ListBySession(ctx, f.sessionID)
	require.NoError(t, err)
	require.Len(t, events, len(batches), "one revision_event per ProposeEntities call")

	deltaIDs := make(map[uuid.UUID]bool)
	for _, ev := range events {
		for _, d := range ev.EntityDeltas {
			deltaIDs[d.EntityID] = true
		}
	}
	for _, id := range allReturnedIDs {
		assert.True(t, deltaIDs[id], "entity id %s returned by ProposeEntities must appear in some revision_event.entity_deltas for its session", id)
	}
}

// TestMediatedWriteStore_ProposeEntities_ActingEqualsOnBehalfOf_Rejected is
// this issue's Testing case 4 (FR10): acting == on-behalf-of is rejected
// with a named error, and writes no rows.
func TestMediatedWriteStore_ProposeEntities_ActingEqualsOnBehalfOf_Rejected(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-identity-same-test")
	self := mediatedTestSubject("agent-1")

	beforeFeatures := countRows(t, f, "feature")
	beforeEvents := countRows(t, f, "revision_event")

	p := baseMediatedProposal(f, self, self)
	p.Proposals = []store.MediatedEntityProposal{
		{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "Should never exist", Position: 1, SummaryLine: "nope"},
	}

	_, _, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
	assert.ErrorIs(t, err, store.ErrMediatedIdentitySame)

	assert.Equal(t, beforeFeatures, countRows(t, f, "feature"))
	assert.Equal(t, beforeEvents, countRows(t, f, "revision_event"))
}

// TestMediatedWriteStore_ProposeEntities_ActingDiffersFromOnBehalfOf_
// BothStoredDistinctly is this issue's Testing case 5: acting != on-behalf-
// of are both stored distinctly on the appended revision_event and readable
// back through RevisionEvents().ListBySession.
func TestMediatedWriteStore_ProposeEntities_ActingDiffersFromOnBehalfOf_BothStoredDistinctly(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-identity-distinct-test")
	agent := mediatedTestSubject("producer-agent")
	contributor := mediatedTestSubject("requirement-contributor")

	p := baseMediatedProposal(f, agent, contributor)
	p.Proposals = []store.MediatedEntityProposal{
		{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "Distinctly attributed feature", Position: 1, SummaryLine: "f"},
	}

	ev, _, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
	require.NoError(t, err)
	assert.Equal(t, agent, ev.Acting)
	assert.Equal(t, contributor, ev.OnBehalfOf)
	assert.NotEqual(t, ev.Acting, ev.OnBehalfOf)

	list, err := f.store.RevisionEvents().ListBySession(ctx, f.sessionID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, agent, list[0].Acting)
	assert.Equal(t, contributor, list[0].OnBehalfOf)
}

// TestMediatedWriteStore_ProposeEntities_AppearsInWholeProductSlice_
// NoSignoffNeeded is this issue's Testing case 6 (FR9): an entity created
// through this path is returned by GetProductSlice for its Product
// immediately, with no signoff event anywhere in the session -- pinning
// "proposals are visible product-wide as soon as created" as intended
// behavior.
func TestMediatedWriteStore_ProposeEntities_AppearsInWholeProductSlice_NoSignoffNeeded(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-fr9-visibility-test")
	agent := mediatedTestSubject("producer-agent")
	contributor := mediatedTestSubject("requirement-contributor")

	p := baseMediatedProposal(f, agent, contributor)
	p.Proposals = []store.MediatedEntityProposal{
		{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "Unsigned-off feature", Position: 1, SummaryLine: "f"},
	}
	_, entities, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
	require.NoError(t, err)
	require.Len(t, entities, 1)

	q := slice.NewQuerier(f.store)
	doc, err := q.GetProductSlice(ctx, f.productID)
	require.NoError(t, err)

	var found bool
	for _, feat := range doc.Features {
		if feat.ID == entities[0].ID {
			found = true
		}
	}
	assert.True(t, found, "an entity proposed through the mediated path must appear in GetProductSlice immediately, before any signoff")

	events, err := f.store.RevisionEvents().ListBySession(ctx, f.sessionID)
	require.NoError(t, err)
	for _, ev := range events {
		assert.NotEqual(t, store.EventTypeSignoff, ev.EventType, "no signoff event should exist anywhere in this session")
	}
}

// TestMediatedWriteStore_ProposeEntities_ScopeIDComesFromProposal_NeverElsewhere
// is this issue's Testing case 7: scope_id on both the entity rows and the
// event equals the ScopeID given to ProposeEntities -- the gating session's
// scope in the HTTP path (mediated.go never reads scope_id from a request
// body at all).
func TestMediatedWriteStore_ProposeEntities_ScopeIDComesFromProposal_NeverElsewhere(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-scope-test")
	agent := mediatedTestSubject("producer-agent")
	contributor := mediatedTestSubject("requirement-contributor")

	p := baseMediatedProposal(f, agent, contributor)
	p.Proposals = []store.MediatedEntityProposal{
		{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "Scope-qualified feature", Position: 1, SummaryLine: "f"},
	}
	ev, entities, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, f.scopeID, ev.ScopeID)

	var rowScope uuid.UUID
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT scope_id FROM feature WHERE id = $1 AND valid_to IS NULL`, entities[0].ID).Scan(&rowScope))
	assert.Equal(t, f.scopeID, rowScope, "the created feature row's scope_id must equal the proposal's scope_id")
}

// TestMediatedWriteStore_ProposeEntities_TiesOpeningSubmissionToCreatedEntities
// is this issue's Testing case 9 (the FR8->FR9 seam): a session opened with
// a plain-language opening_submission containing no entity reference, then
// proposed into, ties the contributor's original text to the agent-created
// entities via one session_id -- proven against the revision log directly
// (#2544's session slice is a separate task).
func TestMediatedWriteStore_ProposeEntities_TiesOpeningSubmissionToCreatedEntities(t *testing.T) {
	ctx := context.Background()
	f := newMedFixture(t, "whale-net/mediated-fr8-fr9-seam-test")
	agent := mediatedTestSubject("producer-agent")
	contributor := mediatedTestSubject("requirement-contributor")

	ds, err := f.store.DesignSessions().GetByID(ctx, f.sessionID)
	require.NoError(t, err)
	assert.NotContains(t, ds.OpeningSubmission, "uuid", "the opening submission is plain language, not an entity reference")

	p := baseMediatedProposal(f, agent, contributor)
	p.Proposals = []store.MediatedEntityProposal{
		{Kind: store.MediatedEntityKindFeature, ParentID: &f.featureSetID, Name: "From the contributor's idea", Position: 1, SummaryLine: "f"},
	}
	_, entities, err := f.store.MediatedWrites().ProposeEntities(ctx, p)
	require.NoError(t, err)
	require.Len(t, entities, 1)

	events, err := f.store.RevisionEvents().ListBySession(ctx, f.sessionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, f.sessionID, events[0].SessionID, "the revision_event ties back to the same design_session the opening_submission lives on")
	require.Len(t, events[0].EntityDeltas, 1)
	assert.Equal(t, entities[0].ID, events[0].EntityDeltas[0].EntityID)
}

func intPtr(i int) *int { return &i }
