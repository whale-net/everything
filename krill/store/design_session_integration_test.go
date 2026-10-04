//go:build integration

// Real-Postgres coverage for DesignSessionStore (migration 008, issue
// #2542, FR1/FR8) -- the longer-lived container a session's
// revision_event rounds accumulate under. See store_integration_test.go's
// package doc for why this file only builds under the "integration" build
// tag.
//
// This target also carries revision_event_integration_test.go
// (RevisionEventStore, FR2-FR4/NFR1): a RevisionEvent's sole parent is a
// DesignSession, so the two share this file's fixture helpers rather than
// duplicating scope/product/krill_session/design_session setup across two
// go_test targets.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:design_session_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newDesignSessionTestStore provisions an isolated, migrated Postgres
// database (krill's own real embedded schema through 008_design_session)
// and returns a ready *store.Store plus the underlying dbtest.Postgres.
func newDesignSessionTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
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

// newDesignSessionTestScope seeds a single scope row directly -- LB1's FK
// target -- mirroring pointer_integration_test.go's newPointerTestScope.
func newDesignSessionTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres, repoFullName string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, repoFullName).Scan(&id))
	return id
}

// dsTestSubject builds a Subject for these tests -- kept local to this
// go_test target (each go_test target in this package compiles only its
// own srcs, so helpers are not shared across targets).
func dsTestSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

// mintKrillSession opens a real krill_session row via SessionStore.
// InitSession -- FR1's opened_by_krill_session_id provenance column
// requires a genuine krill_session id, not a fabricated uuid.
func mintKrillSession(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID uuid.UUID, subject store.Subject) store.SessionID {
	t.Helper()
	id, err := store.NewSessionStore(db.Pool).InitSession(ctx, scopeID, subject, subject, nil)
	require.NoError(t, err)
	return id
}

// TestDesignSessionStore_Open_WritesRowAndGetByIDReadsItBack is this
// issue's Testing case 1: Open writes a row and returns an id; GetByID
// reads it back with opening_submission and opened_by_krill_session_id
// intact.
func TestDesignSessionStore_Open_WritesRowAndGetByIDReadsItBack(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/design-session-open-test")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, dsTestSubject("human-1"))

	const submission = "As a Requirement Contributor, I want to open a design session with a plain-language idea."
	ds, err := s.DesignSessions().Open(ctx, scopeID, product.ID, submission, krillSessionID)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, ds.ID)
	assert.Equal(t, scopeID, ds.ScopeID)
	assert.Equal(t, product.ID, ds.ProductID)
	assert.Equal(t, submission, ds.OpeningSubmission)
	assert.Equal(t, krillSessionID, ds.OpenedByKrillSessionID)

	got, err := s.DesignSessions().GetByID(ctx, ds.ID)
	require.NoError(t, err)
	assert.Equal(t, ds, got, "GetByID must read back exactly what Open wrote")

	list, err := s.DesignSessions().ListByProduct(ctx, product.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, ds.ID, list[0].ID)
}

// TestDesignSessionStore_Open_UnknownProduct_ReturnsErrNotFoundAndWritesNoRow
// is this issue's Testing case 2: Open against a nonexistent product_id
// fails with a named parent error and writes no row (LB2 parentage).
func TestDesignSessionStore_Open_UnknownProduct_ReturnsErrNotFoundAndWritesNoRow(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/design-session-orphan-test")
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, dsTestSubject("human-1"))

	_, err := s.DesignSessions().Open(ctx, scopeID, uuid.New(), "an idea", krillSessionID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM design_session`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Open must insert no row")
}

// ---------------------------------------------------------------------------
// SummarizeByProduct (design_session_summary.go, FR
// d0a63ffb-8e80-47c1-8a64-2729d4950522): the product-wide aggregate read
// and the Stage derivation it owns.
// ---------------------------------------------------------------------------

// dssFixture is a product with three design sessions plus a second
// product in the same scope, so cases can prove both the per-session rows
// and the isolation between products. One fixture means one Postgres per
// test, as the rest of this file does -- cases that need a fourth session
// call openSession.
type dssFixture struct {
	store     *store.Store
	scopeID   uuid.UUID
	productID uuid.UUID
	// sessionIDs are productID's three sessions, oldest first.
	sessionIDs []uuid.UUID
	// otherProductID is a second product in scopeID whose sessions must
	// never appear in productID's aggregate.
	otherProductID uuid.UUID
	otherSessionID uuid.UUID
}

func newDSSFixture(t *testing.T, repoFullName string) dssFixture {
	t.Helper()
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, repoFullName)
	subject := dsTestSubject("agent-1")
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, subject)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)
	other, err := s.Products().Create(ctx, scopeID, "Other", "a different product")
	require.NoError(t, err)

	f := dssFixture{store: s, scopeID: scopeID, productID: product.ID, otherProductID: other.ID}
	for i, submission := range []string{"first idea", "second idea", "third idea"} {
		ds, err := s.DesignSessions().Open(ctx, scopeID, product.ID, submission, krillSessionID)
		require.NoError(t, err)
		// Now() is transaction-scoped, so pin each session's opened time
		// explicitly rather than sleeping -- newest-first ordering is one
		// of the aggregate's guarantees.
		_, err = db.Pool.Exec(ctx, `UPDATE design_session SET created_at = $2 WHERE id = $1`,
			ds.ID, time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		f.sessionIDs = append(f.sessionIDs, ds.ID)
	}
	otherSession, err := s.DesignSessions().Open(ctx, scopeID, other.ID, "another product's idea", krillSessionID)
	require.NoError(t, err)
	f.otherSessionID = otherSession.ID

	return f
}

// dssAppend appends one revision event of eventType to sessionID, with
// whatever conditional fields that type requires (FR3/FR4) and the
// question delta the case supplies.
func dssAppend(t *testing.T, f dssFixture, sessionID uuid.UUID, eventType store.EventType, delta store.OpenQuestionsDelta) store.RevisionEvent {
	t.Helper()
	ctx := context.Background()
	subject := dsTestSubject("agent-1")
	e := store.NewRevisionEvent{
		ScopeID:            f.scopeID,
		SessionID:          sessionID,
		Acting:             subject,
		OnBehalfOf:         subject,
		EventType:          eventType,
		OpenQuestionsDelta: delta,
	}
	if eventType == store.EventTypeDraft || eventType == store.EventTypeReconciliation {
		verified := "requirement-1"
		e.VerifiedAgainst = &verified
	}
	if eventType == store.EventTypeSignoff {
		changes := store.SignoffStatusChangesRequested
		e.SignoffStatus = &changes
	}
	ev, err := f.store.RevisionEvents().Append(ctx, e)
	require.NoError(t, err)
	return ev
}

// dssAppendSignoff appends a signoff event carrying the given status.
func dssAppendSignoff(t *testing.T, f dssFixture, sessionID uuid.UUID, status store.SignoffStatus) store.RevisionEvent {
	t.Helper()
	ctx := context.Background()
	subject := dsTestSubject("agent-1")
	ev, err := f.store.RevisionEvents().Append(ctx, store.NewRevisionEvent{
		ScopeID:       f.scopeID,
		SessionID:     sessionID,
		Acting:        subject,
		OnBehalfOf:    subject,
		EventType:     store.EventTypeSignoff,
		SignoffStatus: &status,
	})
	require.NoError(t, err)
	return ev
}

// dssStageByID indexes an aggregate's rows by session id.
func dssStageByID(t *testing.T, summary store.ProductDesignSessionsSummary) map[uuid.UUID]store.DesignSessionSummary {
	t.Helper()
	byID := make(map[uuid.UUID]store.DesignSessionSummary, len(summary.Sessions))
	for _, row := range summary.Sessions {
		byID[row.ID] = row
	}
	return byID
}

// TestSummarizeByProduct_StageFollowsLatestRevisionEventType covers every
// Stage the latest revision event's type derives. Each case gets its own
// fixture so no case's events can be another's latest.
func TestSummarizeByProduct_StageFollowsLatestRevisionEventType(t *testing.T) {
	cases := []struct {
		name      string
		eventType store.EventType
		want      store.Stage
	}{
		{"draft is in draft", store.EventTypeDraft, store.StageInDraft},
		{"reconciliation is architect review", store.EventTypeReconciliation, store.StageArchitectReview},
		{"answer is answered", store.EventTypeAnswer, store.StageAnswered},
		{"ruling is ruled", store.EventTypeRuling, store.StageRuled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newDSSFixture(t, "whale-net/design-session-stage-"+string(tc.eventType))
			dssAppend(t, f, f.sessionIDs[0], tc.eventType, store.OpenQuestionsDelta{})

			summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, dssStageByID(t, summary)[f.sessionIDs[0]].Stage)
		})
	}
}

// TestSummarizeByProduct_LatestEventNotFirstWins proves the stage follows
// the latest event rather than whichever type appeared first: a draft
// then a ruling leaves the session ruled.
func TestSummarizeByProduct_LatestEventNotFirstWins(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-latest-event-test")
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeDraft, store.OpenQuestionsDelta{})
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{})

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	assert.Equal(t, store.StageRuled, dssStageByID(t, summary)[f.sessionIDs[0]].Stage)
}

// TestSummarizeByProduct_SessionWithNoEventsIsOpened is the opened case
// on its own, so the "no revision events" branch is proved without a
// sibling event on the same session.
func TestSummarizeByProduct_SessionWithNoEventsIsOpened(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-opened-stage-test")

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	byID := dssStageByID(t, summary)
	for _, id := range f.sessionIDs {
		assert.Equal(t, store.StageOpened, byID[id].Stage, "a session with no revision events is opened")
	}
}

// TestSummarizeByProduct_ChangesRequestedSignoffIsChangesRequested proves
// the one Stage derived from a signoff that is not an approval.
func TestSummarizeByProduct_ChangesRequestedSignoffIsChangesRequested(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-changes-requested-test")
	dssAppendSignoff(t, f, f.sessionIDs[0], store.SignoffStatusChangesRequested)

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	assert.Equal(t, store.StageChangesRequested, dssStageByID(t, summary)[f.sessionIDs[0]].Stage)
}

// TestSummarizeByProduct_ApprovedSignoffWinsOverLaterNonSignoffEvent is
// the terminal-stage rule: an approval is not undone by a later draft,
// ruling or answer, so the follow-up form stays hidden.
func TestSummarizeByProduct_ApprovedSignoffWinsOverLaterNonSignoffEvent(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-approved-terminal-test")
	dssAppendSignoff(t, f, f.sessionIDs[0], store.SignoffStatusApproved)
	// A later non-signoff event would otherwise derive its own stage.
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeDraft, store.OpenQuestionsDelta{})
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{})

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	assert.Equal(t, store.StageApproved, dssStageByID(t, summary)[f.sessionIDs[0]].Stage,
		"an approved signoff is terminal and outranks a later non-signoff event")
}

// TestSummarizeByProduct_ChangesRequestedThenApprovedIsApproved proves
// the *latest* signoff decides, not the first one.
func TestSummarizeByProduct_ChangesRequestedThenApprovedIsApproved(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-signoff-latest-test")
	dssAppendSignoff(t, f, f.sessionIDs[0], store.SignoffStatusChangesRequested)
	dssAppendSignoff(t, f, f.sessionIDs[0], store.SignoffStatusApproved)

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	assert.Equal(t, store.StageApproved, dssStageByID(t, summary)[f.sessionIDs[0]].Stage)
}

// TestSummarizeByProduct_OpenQuestionCountsMatchListOpenQuestions is the
// "never a second notion of open" rule: per session, the aggregate's
// blocking and non-blocking counts partition exactly the set
// ListOpenQuestions returns, including a re-opened question whose latest
// entry flipped its blocking flag, and a resolved question counting for
// neither.
func TestSummarizeByProduct_OpenQuestionCountsMatchListOpenQuestions(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-open-counts-test")

	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "storage backend?"},
		{QuestionID: "q2", Blocking: false, Text: "naming bikeshed"},
		{QuestionID: "q3", Blocking: true, Text: "scope creep?"},
		{QuestionID: "q4", Blocking: true, Text: "unchanged blocking question"},
	}})
	// Re-open q1 as non-blocking: the latest opened entry's flag wins.
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{Resolved: []string{"q1"}})
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: false, Text: "reopened, lower priority"},
	}})
	// q3 resolved outright: it counts for neither split.
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{Resolved: []string{"q3"}})

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	row := dssStageByID(t, summary)[f.sessionIDs[0]]
	assert.Equal(t, 1, row.OpenBlockingQuestions, "q4 only, once q1 was re-opened non-blocking and q3 resolved")
	assert.Equal(t, 2, row.OpenNonBlockingQuestions, "q2 plus the re-opened q1")

	open, err := f.store.RevisionEvents().ListOpenQuestions(ctx, f.sessionIDs[0])
	require.NoError(t, err)
	var blocking, nonBlocking int
	for _, q := range open {
		if q.Blocking {
			blocking++
		} else {
			nonBlocking++
		}
	}
	assert.Equal(t, blocking, row.OpenBlockingQuestions, "the blocking count must partition ListOpenQuestions")
	assert.Equal(t, nonBlocking, row.OpenNonBlockingQuestions, "the non-blocking count must partition ListOpenQuestions")
}

// TestSummarizeByProduct_QuestionsDoNotLeakAcrossSessions proves the
// per-session split: a question opened in one session never counts for
// another, and each session's own reopen/resolve history is its own.
func TestSummarizeByProduct_QuestionsDoNotLeakAcrossSessions(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-cross-session-questions-test")

	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "blocking"},
		{QuestionID: "q2", Blocking: false, Text: "not blocking"},
	}})
	// Same question id, other session: distinct questions entirely.
	dssAppend(t, f, f.sessionIDs[1], store.EventTypeRuling, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "a different blocking question"},
	}})
	// Resolving q1 in session 0 leaves session 1's q1 untouched.
	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{Resolved: []string{"q1"}})

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	byID := dssStageByID(t, summary)
	assert.Equal(t, 0, byID[f.sessionIDs[0]].OpenBlockingQuestions)
	assert.Equal(t, 1, byID[f.sessionIDs[0]].OpenNonBlockingQuestions)
	assert.Equal(t, 1, byID[f.sessionIDs[1]].OpenBlockingQuestions)
	assert.Equal(t, 0, byID[f.sessionIDs[1]].OpenNonBlockingQuestions)
	assert.Equal(t, 0, byID[f.sessionIDs[2]].OpenBlockingQuestions, "an untouched session holds nothing")
}

// TestSummarizeByProduct_ProductLevelBlockingTotals covers the two
// product-wide figures: the open-blocking total across every session and
// the number of sessions holding at least one.
func TestSummarizeByProduct_ProductLevelBlockingTotals(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-product-totals-test")

	dssAppend(t, f, f.sessionIDs[0], store.EventTypeRuling, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "blocking"},
		{QuestionID: "q2", Blocking: true, Text: "also blocking"},
		{QuestionID: "q3", Blocking: false, Text: "not blocking"},
	}})
	dssAppend(t, f, f.sessionIDs[1], store.EventTypeRuling, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q4", Blocking: true, Text: "blocking elsewhere"},
	}})

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	assert.Equal(t, 3, summary.OpenBlockingQuestionCount, "two in the first session plus one in the second")
	assert.Equal(t, 2, summary.SessionsHoldingOpenBlocking, "two sessions hold at least one open blocking question")

	// The totals are the per-row counts folded, not an independent count.
	var blocking, holding int
	for _, row := range summary.Sessions {
		blocking += row.OpenBlockingQuestions
		if row.OpenBlockingQuestions > 0 {
			holding++
		}
	}
	assert.Equal(t, blocking, summary.OpenBlockingQuestionCount)
	assert.Equal(t, holding, summary.SessionsHoldingOpenBlocking)
}

// TestSummarizeByProduct_NewestSessionFirstWithFullRow proves the
// ordering and that each row carries the whole design_session row: the
// opening request and opened time, not just the derived values.
func TestSummarizeByProduct_NewestSessionFirstWithFullRow(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-ordering-test")

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	require.Len(t, summary.Sessions, 3)
	assert.Equal(t, f.productID, summary.ProductID)

	assert.Equal(t, f.sessionIDs[2], summary.Sessions[0].ID, "newest session first")
	assert.Equal(t, f.sessionIDs[1], summary.Sessions[1].ID)
	assert.Equal(t, f.sessionIDs[0], summary.Sessions[2].ID)

	newest := summary.Sessions[0]
	assert.Equal(t, "third idea", newest.OpeningSubmission)
	assert.Equal(t, f.scopeID, newest.ScopeID)
	assert.Equal(t, f.productID, newest.ProductID)
	assert.Equal(t, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), newest.CreatedAt.UTC())

	// The row agrees with the plain per-session read it replaces.
	got, err := f.store.DesignSessions().GetByID(ctx, newest.ID)
	require.NoError(t, err)
	assert.Equal(t, got.ID, newest.ID)
	assert.Equal(t, got.OpeningSubmission, newest.OpeningSubmission)
	assert.Equal(t, got.CreatedAt.UTC(), newest.CreatedAt.UTC())
	assert.Equal(t, got.OpenedByKrillSessionID, newest.OpenedByKrillSessionID)
}

// TestSummarizeByProduct_OtherProductsSessionsExcluded proves the read is
// per product: the sibling product's session (which holds its own open
// blocking question) never appears in this product's rows or totals.
func TestSummarizeByProduct_OtherProductsSessionsExcluded(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-product-isolation-test")
	dssAppend(t, f, f.otherSessionID, store.EventTypeRuling, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "the other product's question"},
	}})

	summary, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	assert.Equal(t, 3, len(summary.Sessions))
	assert.Equal(t, 0, summary.OpenBlockingQuestionCount)
	assert.Equal(t, 0, summary.SessionsHoldingOpenBlocking)
	for _, row := range summary.Sessions {
		assert.NotEqual(t, f.otherSessionID, row.ID)
		assert.NotEqual(t, f.otherProductID, row.ProductID)
	}

	other, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.otherProductID)
	require.NoError(t, err)
	require.Len(t, other.Sessions, 1)
	assert.Equal(t, 1, other.OpenBlockingQuestionCount)
	assert.Equal(t, 1, other.SessionsHoldingOpenBlocking)
}

// TestSummarizeByProduct_ProductWithNoSessionsIsEmptyNotNotFound
// separates the two empty cases a caller must be able to tell apart: a
// live product with nothing opened yet is a zeroed aggregate, not an
// error.
func TestSummarizeByProduct_ProductWithNoSessionsIsEmptyNotNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/design-session-empty-product-test")
	product, err := s.Products().Create(ctx, scopeID, "Fresh", "no sessions yet")
	require.NoError(t, err)

	summary, err := s.DesignSessions().SummarizeByProduct(ctx, product.ID)
	require.NoError(t, err, "a live product with no sessions is not an error")
	assert.Empty(t, summary.Sessions)
	assert.Equal(t, product.ID, summary.ProductID)
	assert.Equal(t, 0, summary.OpenBlockingQuestionCount)
	assert.Equal(t, 0, summary.SessionsHoldingOpenBlocking)
}

// TestSummarizeByProduct_UnknownProduct_ReturnsErrNotFound keeps the 404
// path honest: a nonexistent product is an error, not an empty aggregate.
func TestSummarizeByProduct_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-unknown-product-test")

	_, err := f.store.DesignSessions().SummarizeByProduct(ctx, uuid.New())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetSummaryByID (design_session_summary.go, FR
// a77852a9-35f0-4e05-b7f7-985f88db05f7 / d8146d9e-82a7-471f-ac00-7664e89073ed):
// one session's aggregate, through the same statement and the same stage
// derivation SummarizeByProduct runs.
// ---------------------------------------------------------------------------

// TestGetSummaryByID_UnknownSession_ReturnsErrNotFound is the 404 path: an
// id that names no row is an error, never a zero summary. A zero summary
// would render as a real session with an opened stage and an unreadable
// identity, which is the one thing a detail page must never do.
func TestGetSummaryByID_UnknownSession_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-unknown-id-test")

	_, err := f.store.DesignSessions().GetSummaryByID(ctx, uuid.New())
	assert.ErrorIs(t, err, store.ErrNotFound)

	// An id that is a valid UUID but never a session, and one that is a
	// product's id, are both "no such session" -- the read does not widen
	// its key space.
	_, err = f.store.DesignSessions().GetSummaryByID(ctx, f.productID)
	assert.ErrorIs(t, err, store.ErrNotFound, "a product id is not a session id")
}

// TestGetSummaryByID_AgreesWithSummarizeByProduct is the whole reason this
// method exists rather than a caller re-deriving the stage: one session
// read alone and the same session read out of the product-wide aggregate
// must be the same answer.
//
// Every stage is walked, because the interesting failures are per-stage
// (a signoff the single-session path forgets to consider, a question count
// the product path replays but this one does not) and a single happy-path
// case would pass under either a correct or a subtly-wrong derivation.
func TestGetSummaryByID_AgreesWithSummarizeByProduct(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-summary-parity-test")
	sessionID := f.sessionIDs[0]

	dssAppend(t, f, sessionID, store.EventTypeDraft, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "which store?"},
		{QuestionID: "q2", Blocking: false, Text: "wording"},
	}})
	dssAppend(t, f, sessionID, store.EventTypeReconciliation, store.OpenQuestionsDelta{
		Resolved: []string{"q1"},
	})
	// An approval makes the stage terminal, which is the rule most likely
	// to be re-derived differently by a second implementation.
	dssAppendSignoff(t, f, sessionID, store.SignoffStatusApproved)

	byID, err := f.store.DesignSessions().GetSummaryByID(ctx, sessionID)
	require.NoError(t, err)

	aggregate, err := f.store.DesignSessions().SummarizeByProduct(ctx, f.productID)
	require.NoError(t, err)
	fromProduct, ok := dssStageByID(t, aggregate)[sessionID]
	require.True(t, ok, "the session must appear in its own product's aggregate")

	assert.Equal(t, fromProduct.Stage, byID.Stage,
		"the single-session read and the product-wide read derive one stage, or a list and a detail can disagree")
	assert.Equal(t, fromProduct.OpenBlockingQuestions, byID.OpenBlockingQuestions)
	assert.Equal(t, fromProduct.OpenNonBlockingQuestions, byID.OpenNonBlockingQuestions)
	assert.Equal(t, fromProduct.DesignSession, byID.DesignSession,
		"the underlying row is carried whole by both reads")

	// Spelled out so a regression says which half moved: the approval is
	// terminal, and q2's non-blocking count survives q1's resolution.
	assert.Equal(t, store.StageApproved, byID.Stage)
	assert.Equal(t, 0, byID.OpenBlockingQuestions)
	assert.Equal(t, 1, byID.OpenNonBlockingQuestions)
}

// TestGetSummaryByID_SessionWithNoEventsIsOpened is the other end of the
// derivation on its own, so the "no revision events" branch is proved
// without a sibling session's events in the way.
func TestGetSummaryByID_SessionWithNoEventsIsOpened(t *testing.T) {
	ctx := context.Background()
	f := newDSSFixture(t, "whale-net/design-session-get-by-id-opened-test")

	summary, err := f.store.DesignSessions().GetSummaryByID(ctx, f.sessionIDs[0])
	require.NoError(t, err)
	assert.Equal(t, store.StageOpened, summary.Stage)
	assert.Equal(t, 0, summary.OpenBlockingQuestions)
	assert.Equal(t, 0, summary.OpenNonBlockingQuestions)
}

// TestGetSummaryByID_ReadsTheOpeningIdentityFromKrillSession is FR
// d8146d9e's server-side half: the summary carries the acting subject of
// the krill_session that gated the open call, read by the LEFT JOIN rather
// than stored on the design_session row.
func TestGetSummaryByID_ReadsTheOpeningIdentityFromKrillSession(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/design-session-opened-by-test")
	subject := store.Subject{Iss: "https://issuer.example.com", Sub: "alex", Kind: store.SubjectKindHuman}
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, subject)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)
	ds, err := s.DesignSessions().Open(ctx, scopeID, product.ID, "an idea", krillSessionID)
	require.NoError(t, err)

	summary, err := s.DesignSessions().GetSummaryByID(ctx, ds.ID)
	require.NoError(t, err)
	assert.Equal(t, subject, summary.OpenedBy,
		"the summary carries the operator who opened the session, as the server read it")
	assert.Equal(t, krillSessionID, summary.OpenedByKrillSessionID)

	// Both reads carry it, not just the one the detail page happens to use:
	// a field populated by only one of two reads of the same row is a
	// field whose answer depends on which accessor asked.
	aggregate, err := s.DesignSessions().SummarizeByProduct(ctx, product.ID)
	require.NoError(t, err)
	require.Len(t, aggregate.Sessions, 1)
	assert.Equal(t, subject, aggregate.Sessions[0].OpenedBy,
		"the product-wide aggregate carries the opening identity too")
}

// TestGetSummaryByID_MissingKrillSessionYieldsZeroSubject is the boundary
// the LEFT JOIN exists for: when the krill_session row behind
// opened_by_krill_session_id is gone, the answer is the zero Subject.
//
// Not a Subject of empty strings pretending to name somebody -- a caller
// that renders a label must be able to tell "nobody" from "somebody whose
// details are blank", and only the zero value distinguishes them.
func TestGetSummaryByID_MissingKrillSessionYieldsZeroSubject(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/design-session-missing-session-row-test")
	subject := dsTestSubject("agent-1")
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, subject)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)
	ds, err := s.DesignSessions().Open(ctx, scopeID, product.ID, "an idea", krillSessionID)
	require.NoError(t, err)

	// Drop the referential guarantee and the row. The FK is what makes the
	// dangling reference unrepresentable in practice; the LEFT JOIN is
	// still correct without it, and this is the only way to drive the
	// branch -- no production path deletes a krill_session (migration 003
	// is append-only, no delete verb).
	_, err = db.Pool.Exec(ctx, `ALTER TABLE design_session DROP CONSTRAINT design_session_opened_by_krill_session_id_fkey`)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `DELETE FROM krill_session WHERE id = $1`, krillSessionID)
	require.NoError(t, err)

	summary, err := s.DesignSessions().GetSummaryByID(ctx, ds.ID)
	require.NoError(t, err, "the session row is still there; only its session row is not")
	assert.Equal(t, store.Subject{}, summary.OpenedBy,
		"an unreadable krill_session yields the zero Subject, never a fabricated one")
	assert.Empty(t, summary.OpenedBy.Sub)
	assert.Empty(t, summary.OpenedBy.Iss)
	// The provenance the design_session row does hold survives, or a
	// missing join would cost the page the id it can still show.
	assert.Equal(t, krillSessionID, summary.OpenedByKrillSessionID)
}
