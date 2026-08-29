package authz

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// -- fake Queryer -----------------------------------------------------------
//
// PGResolver depends only on the Queryer interface (QueryRow/Query), not a
// live *pgxpool.Pool -- these fakes let the resolution logic in
// resolver.go (inheritance rules, error mapping, NFR2's single-query shape)
// be exercised without Docker/Postgres. Real-SQL coverage (the actual
// recursive region CTE, the actual household_membership join) belongs to
// this package's integration tests once those exist; this file proves the
// Go-level contract PGResolver promises callers.

// fakeRow is a canned pgx.Row: Scan copies values into dest positionally,
// or returns err if set (e.g. pgx.ErrNoRows, to exercise the "no such row"
// branch every resolve* method has).
type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("fakeRow: Scan got %d dest, want %d", len(dest), len(r.values))
	}
	for i, d := range dest {
		if err := scanInto(d, r.values[i]); err != nil {
			return fmt.Errorf("fakeRow: dest[%d]: %w", i, err)
		}
	}
	return nil
}

// scanInto assigns value into the pointer dest via reflection, mirroring
// how a real pgx driver assigns a decoded column into a Scan destination.
// A nil value (e.g. a NULL household_id) zeroes dest, matching pgx's
// behavior for a nil-scannable destination like *int64.
func scanInto(dest any, value any) error {
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return fmt.Errorf("dest %T is not a non-nil pointer", dest)
	}
	elem := dv.Elem()
	if value == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}
	vv := reflect.ValueOf(value)
	if !vv.Type().AssignableTo(elem.Type()) {
		return fmt.Errorf("cannot assign %T into %s", value, elem.Type())
	}
	elem.Set(vv)
	return nil
}

// fakeRows is a canned pgx.Rows over an in-memory table (one []any per
// row), for ScopeForPrincipal's household_membership query.
type fakeRows struct {
	data [][]any
	idx  int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }

func (r *fakeRows) Next() bool {
	if r.idx >= len(r.data) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.data[r.idx-1]
	if len(dest) != len(row) {
		return fmt.Errorf("fakeRows: Scan got %d dest, want %d", len(dest), len(row))
	}
	for i, d := range dest {
		if err := scanInto(d, row[i]); err != nil {
			return fmt.Errorf("fakeRows: dest[%d]: %w", i, err)
		}
	}
	return nil
}

func (r *fakeRows) Values() ([]any, error) {
	return r.data[r.idx-1], nil
}

// fakeQueryer implements Queryer with programmable QueryRow/Query results
// and a call counter -- the counter is what backs the "resolve the entity
// and the scope in one query" structural assertions below (NFR2).
type fakeQueryer struct {
	queryRowResult fakeRow
	queryRowCalls  int

	queryRows  []any // rows for fakeRows.data, one element per row's []any
	queryErr   error
	queryCalls int
}

func (f *fakeQueryer) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.queryRowCalls++
	return f.queryRowResult
}

func (f *fakeQueryer) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.queryCalls++
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	data := make([][]any, len(f.queryRows))
	for i, row := range f.queryRows {
		data[i] = row.([]any)
	}
	return &fakeRows{data: data}, nil
}

// -- Resolve: board ----------------------------------------------------------

func TestPGResolver_Resolve_Board_Found(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{householdPtr(42)}}}
	r := &PGResolver{db: q}

	res, err := r.Resolve(context.Background(), EntityRef{Kind: EntityBoard, ID: 1})
	if err != nil {
		t.Fatalf("Resolve board: unexpected error: %v", err)
	}
	if res.Unclaimed {
		t.Error("Resolution.Unclaimed = true, want false for a claimed board")
	}
	if res.HouseholdID != 42 {
		t.Errorf("Resolution.HouseholdID = %d, want 42", res.HouseholdID)
	}
}

func TestPGResolver_Resolve_Board_Unclaimed(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{(*int64)(nil)}}}
	r := &PGResolver{db: q}

	res, err := r.Resolve(context.Background(), EntityRef{Kind: EntityBoard, ID: 1})
	if err != nil {
		t.Fatalf("Resolve board: unexpected error: %v", err)
	}
	if !res.Unclaimed {
		t.Error("Resolution.Unclaimed = false, want true for a never-claimed board (FR1.1)")
	}
}

func TestPGResolver_Resolve_Board_NotFound(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{err: pgx.ErrNoRows}}
	r := &PGResolver{db: q}

	_, err := r.Resolve(context.Background(), EntityRef{Kind: EntityBoard, ID: 999})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve nonexistent board: err = %v, want ErrNotFound", err)
	}
}

func TestPGResolver_Resolve_UnknownKind_Error(t *testing.T) {
	q := &fakeQueryer{}
	r := &PGResolver{db: q}

	_, err := r.Resolve(context.Background(), EntityRef{Kind: "widget", ID: 1})
	if err == nil {
		t.Fatal("Resolve with an unknown EntityKind returned nil error, want a failure")
	}
	if q.queryRowCalls != 0 {
		t.Errorf("unknown-kind Resolve issued %d queries, want 0", q.queryRowCalls)
	}
}

// -- Resolve: plant/sensor/reading (spot checks; full inheritance-chain
// coverage over real SQL is this package's future integration test) -------

func TestPGResolver_Resolve_Plant_Found(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{int64(7)}}}
	r := &PGResolver{db: q}

	res, err := r.Resolve(context.Background(), EntityRef{Kind: EntityPlant, ID: 1})
	if err != nil {
		t.Fatalf("Resolve plant: unexpected error: %v", err)
	}
	if res.HouseholdID != 7 {
		t.Errorf("Resolution.HouseholdID = %d, want 7", res.HouseholdID)
	}
}

func TestPGResolver_Resolve_Sensor_UnclaimedBoard(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{(*int64)(nil)}}}
	r := &PGResolver{db: q}

	res, err := r.Resolve(context.Background(), EntityRef{Kind: EntitySensor, ID: 1})
	if err != nil {
		t.Fatalf("Resolve sensor: unexpected error: %v", err)
	}
	if !res.Unclaimed {
		t.Error("sensor on an unclaimed board: Unclaimed = false, want true (inherits FR1.1's exception)")
	}
}

// -- ResolveBoardByDeviceID --------------------------------------------------

func TestPGResolver_ResolveBoardByDeviceID_Found(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{int64(5), householdPtr(3)}}}
	r := &PGResolver{db: q}

	ref, res, err := r.ResolveBoardByDeviceID(context.Background(), "device-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Kind != EntityBoard || ref.ID != 5 {
		t.Errorf("ref = %+v, want {Board 5}", ref)
	}
	if res.HouseholdID != 3 || res.Unclaimed {
		t.Errorf("res = %+v, want {HouseholdID:3 Unclaimed:false}", res)
	}
	if q.queryRowCalls != 1 {
		t.Errorf("ResolveBoardByDeviceID issued %d queries, want exactly 1 (NFR2: resolve entity+scope in one query)", q.queryRowCalls)
	}
}

func TestPGResolver_ResolveBoardByDeviceID_Unclaimed(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{int64(5), (*int64)(nil)}}}
	r := &PGResolver{db: q}

	_, res, err := r.ResolveBoardByDeviceID(context.Background(), "device-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Unclaimed {
		t.Error("Unclaimed = false, want true for a never-claimed board")
	}
}

func TestPGResolver_ResolveBoardByDeviceID_NotFound(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{err: pgx.ErrNoRows}}
	r := &PGResolver{db: q}

	_, _, err := r.ResolveBoardByDeviceID(context.Background(), "no-such-device")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if q.queryRowCalls != 1 {
		t.Errorf("ResolveBoardByDeviceID (not found) issued %d queries, want exactly 1 -- a not-found board must take the same one-query path as any other outcome (NFR2)", q.queryRowCalls)
	}
}

// -- ScopeForPrincipal --------------------------------------------------------

// TestPGResolver_ScopeForPrincipal_MultipleMemberships_UnionScope is FR75's
// multi-household case at the resolver boundary: a principal with more
// than one current household_membership row gets a Scope that permits
// entities in every one of them, not just the first row returned.
func TestPGResolver_ScopeForPrincipal_MultipleMemberships_UnionScope(t *testing.T) {
	q := &fakeQueryer{queryRows: []any{
		[]any{int64(10)},
		[]any{int64(20)},
	}}
	r := &PGResolver{db: q}

	scope, err := r.ScopeForPrincipal(context.Background(), "bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scope.Permits(board(1), Resolution{HouseholdID: 10}) {
		t.Error("Scope does not permit household 10, want it to (bob has a current membership there)")
	}
	if !scope.Permits(board(1), Resolution{HouseholdID: 20}) {
		t.Error("Scope does not permit household 20, want it to (bob has a current membership there)")
	}
	if scope.Permits(board(1), Resolution{HouseholdID: 30}) {
		t.Error("Scope permits household 30, want false (bob has no membership there)")
	}
}

// TestPGResolver_ScopeForPrincipal_NoMemberships_PermitsNothing is FR5.1's
// "caller with no household" case at the resolver boundary: zero current
// membership rows must produce a Scope that permits nothing, never one
// that's unrestricted.
func TestPGResolver_ScopeForPrincipal_NoMemberships_PermitsNothing(t *testing.T) {
	q := &fakeQueryer{queryRows: []any{}}
	r := &PGResolver{db: q}

	scope, err := r.ScopeForPrincipal(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope.Permits(board(1), Resolution{HouseholdID: 1}) {
		t.Fatal("Scope for a principal with no current membership permits a household, want it to permit nothing")
	}
	frag, _ := scope.Filter(1)
	if frag != "FALSE" {
		t.Errorf("Filter fragment for a no-membership principal = %q, want %q (must render as empty, never as unfiltered)", frag, "FALSE")
	}
}

// -- ResolveInScope -----------------------------------------------------------

// TestResolveInScope_Permitted_ReturnsResolution is the happy path: a
// caller whose Scope permits the resolved entity gets the Resolution back,
// no error.
func TestResolveInScope_Permitted_ReturnsResolution(t *testing.T) {
	resolver := stubResolver{res: Resolution{HouseholdID: 1}}
	scope := NewHouseholdScope(1)

	res, err := ResolveInScope(context.Background(), resolver, board(1), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.HouseholdID != 1 {
		t.Errorf("res.HouseholdID = %d, want 1", res.HouseholdID)
	}
}

// TestResolveInScope_OutOfScope_ReturnsErrOutOfScope proves a real (not
// missing) entity outside the caller's Scope is refused via ErrOutOfScope
// -- server.go maps this to the exact same contract.NotFound failure a
// missing entity gets (NFR2); this test pins the Go-level signal that
// mapping depends on.
func TestResolveInScope_OutOfScope_ReturnsErrOutOfScope(t *testing.T) {
	resolver := stubResolver{res: Resolution{HouseholdID: 99}}
	scope := NewHouseholdScope(1)

	_, err := ResolveInScope(context.Background(), resolver, board(1), scope)
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("err = %v, want ErrOutOfScope", err)
	}
}

// TestResolveInScope_ResolveError_PropagatesUnchanged proves a genuine
// not-found (or any other Resolve error) passes through ResolveInScope
// unchanged, rather than being reclassified -- server.go's errors.Is
// dispatch (ErrNotFound vs ErrOutOfScope) depends on this.
func TestResolveInScope_ResolveError_PropagatesUnchanged(t *testing.T) {
	resolver := stubResolver{err: ErrNotFound}
	scope := NewHouseholdScope(1)

	_, err := ResolveInScope(context.Background(), resolver, board(1), scope)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound to propagate unchanged", err)
	}
}

// -- RoleForPrincipalInHousehold ---------------------------------------------

// TestPGResolver_RoleForPrincipalInHousehold_Member proves a current member
// resolves to RoleMember.
func TestPGResolver_RoleForPrincipalInHousehold_Member(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{true, false}}}
	r := &PGResolver{db: q}

	role, err := r.RoleForPrincipalInHousehold(context.Background(), "alice", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if role != RoleMember {
		t.Errorf("role = %v, want RoleMember", role)
	}
	if q.queryRowCalls != 1 {
		t.Errorf("RoleForPrincipalInHousehold issued %d queries, want exactly 1 (NFR2: resolve membership+grant reach in one round trip)", q.queryRowCalls)
	}
}

// TestPGResolver_RoleForPrincipalInHousehold_Grantee proves a principal
// with no current membership but an active grant resolves to RoleGrantee.
func TestPGResolver_RoleForPrincipalInHousehold_Grantee(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{false, true}}}
	r := &PGResolver{db: q}

	role, err := r.RoleForPrincipalInHousehold(context.Background(), "helper", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if role != RoleGrantee {
		t.Errorf("role = %v, want RoleGrantee", role)
	}
}

// TestPGResolver_RoleForPrincipalInHousehold_None proves a principal with
// neither a current membership nor an active grant resolves to RoleNone.
func TestPGResolver_RoleForPrincipalInHousehold_None(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{false, false}}}
	r := &PGResolver{db: q}

	role, err := r.RoleForPrincipalInHousehold(context.Background(), "stranger", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if role != RoleNone {
		t.Errorf("role = %v, want RoleNone", role)
	}
}

// TestPGResolver_RoleForPrincipalInHousehold_MemberPrecedenceOverGrant
// proves the documented (improbable) tie-break: a principal who is
// somehow both a current member and an active grantee of the same
// household resolves to the more permissive RoleMember, not RoleGrantee.
func TestPGResolver_RoleForPrincipalInHousehold_MemberPrecedenceOverGrant(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{true, true}}}
	r := &PGResolver{db: q}

	role, err := r.RoleForPrincipalInHousehold(context.Background(), "both", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if role != RoleMember {
		t.Errorf("role = %v, want RoleMember (membership takes precedence over a grant)", role)
	}
}

// -- ResolveGrantRole ---------------------------------------------------------

// TestPGResolver_ResolveGrantRole_Member proves a grant's revoke/list
// caller who is a current member of the grant's household resolves with
// Role == RoleMember and the grant's household id/revoked state.
func TestPGResolver_ResolveGrantRole_Member(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{int64(7), false, true, false}}}
	r := &PGResolver{db: q}

	res, err := r.ResolveGrantRole(context.Background(), 42, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.HouseholdID != 7 {
		t.Errorf("HouseholdID = %d, want 7", res.HouseholdID)
	}
	if res.Revoked {
		t.Error("Revoked = true, want false")
	}
	if res.Role != RoleMember {
		t.Errorf("Role = %v, want RoleMember", res.Role)
	}
}

// TestPGResolver_ResolveGrantRole_Grantee proves the caller-is-a-grantee
// case: a caller with no membership but an active grant on the same
// household resolves with Role == RoleGrantee.
func TestPGResolver_ResolveGrantRole_Grantee(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{int64(7), false, false, true}}}
	r := &PGResolver{db: q}

	res, err := r.ResolveGrantRole(context.Background(), 42, "helper")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Role != RoleGrantee {
		t.Errorf("Role = %v, want RoleGrantee", res.Role)
	}
}

// TestPGResolver_ResolveGrantRole_RoleNone proves a caller with no reach
// at all over the grant's household resolves with Role == RoleNone --
// server.go maps this to the same grantNotFoundFailure a nonexistent
// grant_id gets (NFR2).
func TestPGResolver_ResolveGrantRole_RoleNone(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{int64(7), false, false, false}}}
	r := &PGResolver{db: q}

	res, err := r.ResolveGrantRole(context.Background(), 42, "stranger")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Role != RoleNone {
		t.Errorf("Role = %v, want RoleNone", res.Role)
	}
}

// TestPGResolver_ResolveGrantRole_Revoked proves an already-revoked grant's
// Revoked field is surfaced, distinct from ErrGrantNotFound -- the grant
// row exists, it is just no longer active.
func TestPGResolver_ResolveGrantRole_Revoked(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{values: []any{int64(7), true, true, false}}}
	r := &PGResolver{db: q}

	res, err := r.ResolveGrantRole(context.Background(), 42, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Revoked {
		t.Error("Revoked = false, want true")
	}
}

// TestPGResolver_ResolveGrantRole_NotFound proves grant_id naming no row
// returns ErrGrantNotFound, in the same single query as every other
// outcome (NFR2).
func TestPGResolver_ResolveGrantRole_NotFound(t *testing.T) {
	q := &fakeQueryer{queryRowResult: fakeRow{err: pgx.ErrNoRows}}
	r := &PGResolver{db: q}

	_, err := r.ResolveGrantRole(context.Background(), 999, "alice")
	if !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("err = %v, want ErrGrantNotFound", err)
	}
	if q.queryRowCalls != 1 {
		t.Errorf("ResolveGrantRole (not found) issued %d queries, want exactly 1", q.queryRowCalls)
	}
}

// stubResolver is a minimal Resolver for ResolveInScope's unit tests --
// narrower than fakeQueryer-backed PGResolver, since ResolveInScope itself
// only depends on the Resolver interface.
type stubResolver struct {
	res Resolution
	err error
}

func (s stubResolver) Resolve(ctx context.Context, ref EntityRef) (Resolution, error) {
	return s.res, s.err
}

func householdPtr(id int64) *int64 { return &id }
