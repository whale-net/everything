//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See the go_test target's gotags in BUILD.bazel for how to run it.
//
// These tests exercise exactly what grantindex_test.go's pure-Go unit
// tests cannot: a real PostgreSQL round trip and cross-subject/cross-domain
// isolation backed by actual SQL rather than an in-memory fake -- mirrors
// grpcauth/pgstore/pgstore_integration_test.go's pattern via
// libs/go/dbtest. Each test creates the expected grpcauth_grant_index-shaped
// table itself (no shipped migration -- FR13; see grantindex.go's package
// doc for the schema contract).
package grantindex

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// grantIndexSchema is a self-contained copy of the schema contract
// documented in grantindex.go's package doc comment. dbtest's own README
// asks integration tests to keep schema self-contained rather than
// importing another package's migrations.
const grantIndexSchema = `
	CREATE TABLE grpcauth_grant_index (
		subject_iss        TEXT        NOT NULL,
		subject_sub        TEXT        NOT NULL,
		domain              TEXT        NOT NULL,
		preferred_username  TEXT        NOT NULL,
		granted_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (subject_iss, subject_sub, domain)
	);
`

// newTestIndex stands up a real Postgres-backed *Index with the
// grpcauth_grant_index schema already applied.
func newTestIndex(ctx context.Context, t *testing.T) (*Index, *dbtest.Postgres) {
	t.Helper()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: grantIndexSchema})
	idx, err := New(db.Pool, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return idx, db
}

// TestIndex_RecordThenListBySubject_ReturnsExactlyThatEntry asserts
// Record then ListBySubject returns exactly the recorded entry.
func TestIndex_RecordThenListBySubject_ReturnsExactlyThatEntry(t *testing.T) {
	ctx := context.Background()
	idx, _ := newTestIndex(ctx, t)

	entry := Entry{
		SubjectIss:        "https://keycloak.example/realms/whale-net",
		SubjectSub:        "alice-sub",
		Domain:            "audience_score_system",
		PreferredUsername: "alice",
	}
	if err := idx.Record(ctx, entry); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := idx.ListBySubject(ctx, entry.SubjectIss, entry.SubjectSub)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListBySubject returned %d entries, want exactly 1: %+v", len(got), got)
	}
	if got[0].SubjectIss != entry.SubjectIss || got[0].SubjectSub != entry.SubjectSub ||
		got[0].Domain != entry.Domain || got[0].PreferredUsername != entry.PreferredUsername {
		t.Fatalf("ListBySubject entry = %+v, want SubjectIss/SubjectSub/Domain/PreferredUsername matching %+v", got[0], entry)
	}
	if got[0].GrantedAt.IsZero() {
		t.Fatal("ListBySubject entry.GrantedAt is zero, want a populated timestamp")
	}
}

// TestIndex_RecordTwice_IdempotentNoDuplicateNoGrantedAtChange asserts
// Record twice for the same (subject_iss, subject_sub, domain) does not
// error and does not duplicate the row or change granted_at.
func TestIndex_RecordTwice_IdempotentNoDuplicateNoGrantedAtChange(t *testing.T) {
	ctx := context.Background()
	idx, _ := newTestIndex(ctx, t)

	entry := Entry{
		SubjectIss:        "https://keycloak.example/realms/whale-net",
		SubjectSub:        "bob-sub",
		Domain:            "manmanv2",
		PreferredUsername: "bob",
	}
	if err := idx.Record(ctx, entry); err != nil {
		t.Fatalf("Record (first): %v", err)
	}

	first, err := idx.ListBySubject(ctx, entry.SubjectIss, entry.SubjectSub)
	if err != nil {
		t.Fatalf("ListBySubject (after first record): %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("ListBySubject after first record returned %d entries, want 1", len(first))
	}
	firstGrantedAt := first[0].GrantedAt

	// Sleep briefly so that, if Record incorrectly bumped granted_at on a
	// re-record, the timestamp would observably differ.
	time.Sleep(50 * time.Millisecond)

	// Re-record with a different preferred_username too: this must not
	// overwrite the original snapshot either (write-once semantics).
	reconsented := entry
	reconsented.PreferredUsername = "bob-renamed"
	if err := idx.Record(ctx, reconsented); err != nil {
		t.Fatalf("Record (second, same key): %v", err)
	}

	second, err := idx.ListBySubject(ctx, entry.SubjectIss, entry.SubjectSub)
	if err != nil {
		t.Fatalf("ListBySubject (after second record): %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("ListBySubject after second record returned %d entries, want 1 (no duplicate row)", len(second))
	}
	if !second[0].GrantedAt.Equal(firstGrantedAt) {
		t.Fatalf("GrantedAt changed across re-record: before = %v, after = %v, want unchanged", firstGrantedAt, second[0].GrantedAt)
	}
	if second[0].PreferredUsername != entry.PreferredUsername {
		t.Fatalf("PreferredUsername after re-record = %q, want %q (write-once snapshot must not be overwritten)", second[0].PreferredUsername, entry.PreferredUsername)
	}
}

// TestIndex_ListBySubject_NeverReturnsAnotherSubjectsRows asserts
// ListBySubject for operator A never returns operator B's rows.
func TestIndex_ListBySubject_NeverReturnsAnotherSubjectsRows(t *testing.T) {
	ctx := context.Background()
	idx, _ := newTestIndex(ctx, t)

	const iss = "https://keycloak.example/realms/whale-net"
	if err := idx.Record(ctx, Entry{SubjectIss: iss, SubjectSub: "carol-sub", Domain: "manmanv2", PreferredUsername: "carol"}); err != nil {
		t.Fatalf("Record carol: %v", err)
	}
	if err := idx.Record(ctx, Entry{SubjectIss: iss, SubjectSub: "dave-sub", Domain: "manmanv2", PreferredUsername: "dave"}); err != nil {
		t.Fatalf("Record dave: %v", err)
	}

	got, err := idx.ListBySubject(ctx, iss, "carol-sub")
	if err != nil {
		t.Fatalf("ListBySubject(carol): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListBySubject(carol) returned %d entries, want 1", len(got))
	}
	if got[0].SubjectSub != "carol-sub" {
		t.Fatalf("ListBySubject(carol) returned SubjectSub = %q, want %q", got[0].SubjectSub, "carol-sub")
	}
	for _, e := range got {
		if e.PreferredUsername == "dave" {
			t.Fatalf("ListBySubject(carol) returned dave's entry: %+v", e)
		}
	}
}

// TestIndex_ListAll_MultipleOperatorsAndDomains_DeterministicOrder asserts
// ListAll returns entries for multiple operators and multiple domains,
// deterministically ordered.
func TestIndex_ListAll_MultipleOperatorsAndDomains_DeterministicOrder(t *testing.T) {
	ctx := context.Background()
	idx, _ := newTestIndex(ctx, t)

	const iss = "https://keycloak.example/realms/whale-net"
	entries := []Entry{
		{SubjectIss: iss, SubjectSub: "zed-sub", Domain: "manmanv2", PreferredUsername: "zed"},
		{SubjectIss: iss, SubjectSub: "amy-sub", Domain: "manmanv2", PreferredUsername: "amy"},
		{SubjectIss: iss, SubjectSub: "amy-sub", Domain: "audience_score_system", PreferredUsername: "amy"},
	}
	for _, e := range entries {
		if err := idx.Record(ctx, e); err != nil {
			t.Fatalf("Record %+v: %v", e, err)
		}
	}

	got, err := idx.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListAll returned %d entries, want 3: %+v", len(got), got)
	}

	// Deterministic order: preferred_username, domain -- amy's two rows
	// (audience_score_system before manmanv2 alphabetically) must come
	// before zed's.
	wantOrder := []struct {
		username, domain string
	}{
		{"amy", "audience_score_system"},
		{"amy", "manmanv2"},
		{"zed", "manmanv2"},
	}
	for i, want := range wantOrder {
		if got[i].PreferredUsername != want.username || got[i].Domain != want.domain {
			t.Fatalf("ListAll()[%d] = (username=%q, domain=%q), want (username=%q, domain=%q); full result: %+v",
				i, got[i].PreferredUsername, got[i].Domain, want.username, want.domain, got)
		}
	}

	// Calling ListAll again must reproduce the exact same order.
	again, err := idx.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll (second call): %v", err)
	}
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("ListAll order changed across calls at index %d: first = %+v, second = %+v", i, got[i], again[i])
		}
	}
}

// TestIndex_SameSubjectDifferentDomains_AreDistinctRows asserts an entry
// for (A, audience_score_system) and one for (A, manmanv2) are distinct
// rows.
func TestIndex_SameSubjectDifferentDomains_AreDistinctRows(t *testing.T) {
	ctx := context.Background()
	idx, _ := newTestIndex(ctx, t)

	const iss = "https://keycloak.example/realms/whale-net"
	const sub = "erin-sub"
	if err := idx.Record(ctx, Entry{SubjectIss: iss, SubjectSub: sub, Domain: "audience_score_system", PreferredUsername: "erin"}); err != nil {
		t.Fatalf("Record (audience_score_system): %v", err)
	}
	if err := idx.Record(ctx, Entry{SubjectIss: iss, SubjectSub: sub, Domain: "manmanv2", PreferredUsername: "erin"}); err != nil {
		t.Fatalf("Record (manmanv2): %v", err)
	}

	got, err := idx.ListBySubject(ctx, iss, sub)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListBySubject returned %d entries, want 2 distinct domain rows: %+v", len(got), got)
	}
	domains := map[string]bool{}
	for _, e := range got {
		domains[e.Domain] = true
	}
	if !domains["audience_score_system"] || !domains["manmanv2"] {
		t.Fatalf("ListBySubject domains = %v, want both audience_score_system and manmanv2", domains)
	}
}
