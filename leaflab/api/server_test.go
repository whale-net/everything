package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// fakeRepo implements deviceRepository entirely in memory. GetHealth's
// tests below only exercise Ping; ListBoards is exercised by the FR5/NFR2
// tests further down, which capture the authz.Scope a call was made with
// (listBoardsScope) so a test can assert it was threaded through
// unmodified, rather than widened or dropped, without a live Postgres
// connection. The remaining methods still panic if unexpectedly called --
// nothing in this file exercises PushDeviceConfig's write path.
type fakeRepo struct {
	pingErr error

	// getLatestAcceptedConfigCalls counts calls to GetLatestAcceptedConfig
	// -- the NFR2 tests below use this to prove an authorization refusal
	// short-circuits *before* this repository call is ever reached, not
	// just that the RPC eventually returns an error.
	getLatestAcceptedConfigCalls int

	listBoardsScope authz.Scope
	listBoardsRows  []BoardRow
	listBoardsErr   error

	// setBoardDisplayName* fields capture SetBoardDisplayName's arguments and
	// control its return, for the FR57 tests further down: setBoardDisplayNameCalls
	// proves an authorization refusal short-circuits before this repository
	// call is ever reached (mirroring getLatestAcceptedConfigCalls above),
	// and the captured boardID/displayName/entry let a successful-call test
	// assert the write and its audit.Entry carry exactly what the handler
	// was asked to record.
	setBoardDisplayNameCalls   int
	setBoardDisplayNameBoardID int64
	setBoardDisplayNameValue   string
	setBoardDisplayNameEntry   audit.Entry
	setBoardDisplayNameErr     error
}

func (f *fakeRepo) GetOrCreateBoard(ctx context.Context, deviceID string) (int64, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, entry audit.Entry) (int64, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error) {
	f.getLatestAcceptedConfigCalls++
	return nil, nil
}

func (f *fakeRepo) ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error) {
	f.listBoardsScope = scope
	return f.listBoardsRows, f.listBoardsErr
}

func (f *fakeRepo) SetBoardDisplayName(ctx context.Context, boardID int64, displayName string, entry audit.Entry) error {
	f.setBoardDisplayNameCalls++
	f.setBoardDisplayNameBoardID = boardID
	f.setBoardDisplayNameValue = displayName
	f.setBoardDisplayNameEntry = entry
	return f.setBoardDisplayNameErr
}

func (f *fakeRepo) Ping(ctx context.Context) error {
	return f.pingErr
}

// fakeAuthz implements authzResolver entirely in memory, with call
// counters so tests can assert on NFR2's "one query" structural shape
// (resolve the entity and the scope in the same number of round trips
// regardless of outcome) without a live Postgres connection.
type fakeAuthz struct {
	scope      authz.Scope
	scopeErr   error
	scopeCalls int

	resolveRef   authz.EntityRef
	resolveRes   authz.Resolution
	resolveErr   error
	resolveCalls int
}

func (f *fakeAuthz) ScopeForPrincipal(ctx context.Context, principalSubject string) (authz.Scope, error) {
	f.scopeCalls++
	return f.scope, f.scopeErr
}

func (f *fakeAuthz) ResolveBoardByDeviceID(ctx context.Context, deviceID string) (authz.EntityRef, authz.Resolution, error) {
	f.resolveCalls++
	return f.resolveRef, f.resolveRes, f.resolveErr
}

// allPermittingScope is a test-only Scope that permits everything -- used
// to prove scopeForCaller's fail-closed branch never even reaches
// authzSvc.ScopeForPrincipal when the context carries no Claims, rather
// than proving it merely returns something restrictive by coincidence.
type allPermittingScope struct{}

func (allPermittingScope) Permits(ref authz.EntityRef, res authz.Resolution) bool { return true }
func (allPermittingScope) Filter(argStart int) (string, []any)                    { return "TRUE", nil }

// authedTestCtx returns a context carrying grpcauth.Claims for subject,
// exactly as the auth interceptor chain would inject them.
func authedTestCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// countPopulatedFields returns how many fields protoreflect considers set
// (non-default) on msg. Used to assert FR63.2's "no version, no dependency
// names, no per-dependency status detail" -- GetHealthResponse must carry
// exactly one populated field (status) no matter which branch produced it.
func countPopulatedFields(msg protoreflect.Message) int {
	n := 0
	msg.Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
		n++
		return true
	})
	return n
}

// TestGetHealth_NoCredential_Succeeds proves GetHealth is callable with an
// empty (unauthenticated) context -- no grpcauth.Claims required -- and
// that the response carries exactly one populated field (FR63.2). rmqConn
// is nil (mirrors a test server with no RabbitMQ dependency; GetHealth's
// nil handling treats that as mqUp=false), so together with a healthy DB
// ping this exercises the DEGRADED branch. See main_test.go's bufconn test
// for the same assertion exercised through the full RPC/interceptor chain,
// including the allowlist itself.
func TestGetHealth_NoCredential_Succeeds(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{}, nil, nil, nil, discardLogger())

	resp, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("GetHealth with no credential returned an error, want success (FR63.1): %v", err)
	}
	if got := countPopulatedFields(resp.ProtoReflect()); got != 1 {
		t.Errorf("GetHealthResponse has %d populated fields, want exactly 1 (FR63.2)", got)
	}
}

// TestGetHealth_DatabaseUnreachable_Degraded proves a DB probe failure maps
// to HEALTH_DEGRADED and nothing more specific -- no error, no detail about
// which dependency failed (FR63.2).
func TestGetHealth_DatabaseUnreachable_Degraded(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{pingErr: errors.New("connection refused")}, nil, nil, nil, discardLogger())

	resp, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("GetHealth returned an error rather than a degraded status: %v", err)
	}
	if resp.Status != pb.HealthStatus_HEALTH_DEGRADED {
		t.Errorf("Status = %v, want HEALTH_DEGRADED", resp.Status)
	}
	if got := countPopulatedFields(resp.ProtoReflect()); got != 1 {
		t.Errorf("GetHealthResponse has %d populated fields, want exactly 1 (FR63.2)", got)
	}
}

// TestGetHealth_MQConnectionNil_Degraded proves a nil/unavailable
// RabbitMQ-MQTT connection also maps to HEALTH_DEGRADED, independent of DB
// health (FR63.1's "pgx pool or the RabbitMQ/MQTT connection").
func TestGetHealth_MQConnectionNil_Degraded(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{}, nil, nil, nil, discardLogger())

	resp, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != pb.HealthStatus_HEALTH_DEGRADED {
		t.Errorf("Status = %v, want HEALTH_DEGRADED with rmqConn nil", resp.Status)
	}
}

// TestGetHealth_ErrorNeverCarriesDependencyDetail is NFR13/FR63.2's
// leak-nothing guarantee applied to GetHealth specifically: even the error
// path (which cannot happen today -- GetHealth never returns a non-nil
// error) must never surface a dependency-specific reason if one were added
// later. Documented here as a regression tripwire: GetHealth returning a
// non-nil error at all would already violate FR63.1 ("this is our problem,
// not yours" must still answer as a successful RPC), so this test simply
// pins that invariant.
func TestGetHealth_ErrorNeverCarriesDependencyDetail(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{pingErr: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}, nil, nil, nil, discardLogger())

	_, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("GetHealth returned a transport error %v -- FR63.1 requires GetHealth to always succeed and report DEGRADED instead", err)
	}
}

// -- FR4/NFR2: GetDeviceConfig's board authorization -------------------------

// failureBytes marshals the full gRPC status (code, message, and the
// pb.Failure detail) carried by err, for a genuinely byte-identical
// comparison -- not just "the fields I thought to check are equal". A
// caller reading raw bytes off the wire would see exactly this.
func failureBytes(t *testing.T, err error) []byte {
	t.Helper()
	st := status.Convert(err)
	b, marshalErr := proto.Marshal(st.Proto())
	if marshalErr != nil {
		t.Fatalf("marshal status proto: %v", marshalErr)
	}
	return b
}

// TestGetDeviceConfig_NonexistentAndOutOfScope_ByteIdenticalFailure is
// NFR2's core assertion: a request for a board id that does not exist and
// a request for a board that exists but is outside the caller's
// household (FR4.1) must produce byte-identical gRPC statuses -- same
// code, same message, same pb.Failure detail -- so a caller cannot
// distinguish "no such board" from "not yours" by response shape.
func TestGetDeviceConfig_NonexistentAndOutOfScope_ByteIdenticalFailure(t *testing.T) {
	callerScope := authz.NewHouseholdScope(1)

	nonexistentAuthz := &fakeAuthz{
		scope:      callerScope,
		resolveErr: authz.ErrNotFound,
	}
	nonexistentRepo := &fakeRepo{}
	nonexistentServer := NewLeafLabAPIServer(nonexistentRepo, nonexistentAuthz, nil, nil, discardLogger())
	_, nonexistentErr := nonexistentServer.GetDeviceConfig(authedTestCtx("alice"), &pb.GetDeviceConfigRequest{DeviceId: "does-not-exist"})
	if nonexistentErr == nil {
		t.Fatal("GetDeviceConfig for a nonexistent device_id returned nil error, want a refusal")
	}
	if nonexistentRepo.getLatestAcceptedConfigCalls != 0 {
		t.Errorf("nonexistent-device refusal reached the repository %d times, want 0 -- authorization must short-circuit before any config lookup", nonexistentRepo.getLatestAcceptedConfigCalls)
	}

	outOfScopeAuthz := &fakeAuthz{
		scope:      callerScope,
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: 7},
		resolveRes: authz.Resolution{HouseholdID: 2}, // a different household than callerScope's 1
	}
	outOfScopeRepo := &fakeRepo{}
	outOfScopeServer := NewLeafLabAPIServer(outOfScopeRepo, outOfScopeAuthz, nil, nil, discardLogger())
	_, outOfScopeErr := outOfScopeServer.GetDeviceConfig(authedTestCtx("alice"), &pb.GetDeviceConfigRequest{DeviceId: "device-belongs-to-household-2"})
	if outOfScopeErr == nil {
		t.Fatal("GetDeviceConfig for an out-of-scope device returned nil error, want a refusal")
	}
	if outOfScopeRepo.getLatestAcceptedConfigCalls != 0 {
		t.Errorf("out-of-scope refusal reached the repository %d times, want 0 -- authorization must short-circuit before any config lookup", outOfScopeRepo.getLatestAcceptedConfigCalls)
	}

	nonexistentDetail, ok := contract.FromError(nonexistentErr)
	if !ok {
		t.Fatal("nonexistent-device error carries no Failure detail")
	}
	outOfScopeDetail, ok := contract.FromError(outOfScopeErr)
	if !ok {
		t.Fatal("out-of-scope error carries no Failure detail")
	}
	if nonexistentDetail.Class != string(contract.FailureNotFound) {
		t.Errorf("nonexistent-device Class = %q, want %q", nonexistentDetail.Class, contract.FailureNotFound)
	}
	if !proto.Equal(nonexistentDetail, outOfScopeDetail) {
		t.Errorf("Failure details differ: nonexistent=%v, out-of-scope=%v", nonexistentDetail, outOfScopeDetail)
	}

	nonexistentBytes := failureBytes(t, nonexistentErr)
	outOfScopeBytes := failureBytes(t, outOfScopeErr)
	if string(nonexistentBytes) != string(outOfScopeBytes) {
		t.Errorf("marshaled gRPC status differs between nonexistent and out-of-scope refusals -- NFR2 requires byte-identical status and body\nnonexistent:  %x\nout-of-scope: %x", nonexistentBytes, outOfScopeBytes)
	}
}

// TestGetDeviceConfig_NonexistentAndOutOfScope_SameQueryShape is NFR2's
// "resolve the entity and the scope in one query" requirement, at the Go
// call-count level: both refusal reasons must take the exact same number
// of round trips -- one caller-scope resolution, one board resolution --
// so neither branch is distinguishable by an extra query/round trip a
// timing side channel could observe.
func TestGetDeviceConfig_NonexistentAndOutOfScope_SameQueryShape(t *testing.T) {
	callerScope := authz.NewHouseholdScope(1)

	nonexistentAuthz := &fakeAuthz{scope: callerScope, resolveErr: authz.ErrNotFound}
	nonexistentServer := NewLeafLabAPIServer(&fakeRepo{}, nonexistentAuthz, nil, nil, discardLogger())
	if _, err := nonexistentServer.GetDeviceConfig(authedTestCtx("alice"), &pb.GetDeviceConfigRequest{DeviceId: "does-not-exist"}); err == nil {
		t.Fatal("want a refusal")
	}

	outOfScopeAuthz := &fakeAuthz{
		scope:      callerScope,
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: 7},
		resolveRes: authz.Resolution{HouseholdID: 2},
	}
	outOfScopeServer := NewLeafLabAPIServer(&fakeRepo{}, outOfScopeAuthz, nil, nil, discardLogger())
	if _, err := outOfScopeServer.GetDeviceConfig(authedTestCtx("alice"), &pb.GetDeviceConfigRequest{DeviceId: "device-b"}); err == nil {
		t.Fatal("want a refusal")
	}

	if nonexistentAuthz.scopeCalls != outOfScopeAuthz.scopeCalls {
		t.Errorf("scopeForCaller call count differs: nonexistent=%d, out-of-scope=%d", nonexistentAuthz.scopeCalls, outOfScopeAuthz.scopeCalls)
	}
	if nonexistentAuthz.resolveCalls != outOfScopeAuthz.resolveCalls {
		t.Errorf("ResolveBoardByDeviceID call count differs: nonexistent=%d, out-of-scope=%d", nonexistentAuthz.resolveCalls, outOfScopeAuthz.resolveCalls)
	}
	if nonexistentAuthz.scopeCalls != 1 || nonexistentAuthz.resolveCalls != 1 {
		t.Errorf("want exactly one scope resolution and one board resolution per call, got scopeCalls=%d resolveCalls=%d", nonexistentAuthz.scopeCalls, nonexistentAuthz.resolveCalls)
	}
}

// -- FR5.1: ListBoards is household-scoped, never widened ---------------------

// TestListBoards_ScopeThreadedToRepository_MultiHousehold proves the Scope
// ListBoards passes to the repository is the caller's actual (possibly
// multi-household, FR75) Scope, unmodified by the handler -- not a bare
// household id, and not silently widened.
func TestListBoards_ScopeThreadedToRepository_MultiHousehold(t *testing.T) {
	callerScope := authz.NewUnionScope(authz.NewHouseholdScope(10), authz.NewHouseholdScope(20))
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{scope: callerScope}, nil, nil, discardLogger())

	if _, err := server.ListBoards(authedTestCtx("bob"), &pb.ListBoardsRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.listBoardsScope == nil {
		t.Fatal("Repository.ListBoards was not called with a Scope")
	}
	gotFrag, gotArgs := repo.listBoardsScope.Filter(1)
	wantFrag, wantArgs := callerScope.Filter(1)
	if gotFrag != wantFrag {
		t.Errorf("Scope threaded to repository has fragment %q, want %q", gotFrag, wantFrag)
	}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("Scope threaded to repository has %d args, want %d", len(gotArgs), len(wantArgs))
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Errorf("arg[%d] = %v, want %v", i, gotArgs[i], wantArgs[i])
		}
	}
}

// TestListBoards_EmptyScope_NotWidened proves a caller with no current
// household membership gets a Scope that matches no row passed straight
// through to the repository (never widened to "everything"), and the RPC
// itself still succeeds with an empty list rather than an error (FR5.1).
func TestListBoards_EmptyScope_NotWidened(t *testing.T) {
	repo := &fakeRepo{listBoardsRows: nil}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{scope: authz.NewUnionScope()}, nil, nil, discardLogger())

	resp, err := server.ListBoards(authedTestCtx("nobody"), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("ListBoards for a caller with no household returned an error, want an empty list: %v", err)
	}
	if len(resp.Boards) != 0 {
		t.Errorf("len(Boards) = %d, want 0", len(resp.Boards))
	}

	frag, args := repo.listBoardsScope.Filter(1)
	if frag != "FALSE" || len(args) != 0 {
		t.Errorf("Scope threaded to repository = (%q, %v), want (\"FALSE\", []) -- must never be widened to unfiltered", frag, args)
	}
}

// -- NFR1/FR4: fail-closed with no authenticated principal --------------------

// TestScopeForCaller_NoClaims_FailsClosed proves scopeForCaller denies by
// default when the context carries no grpcauth.Claims -- the "handler
// registered with no scope check denies" case the task's Testing section
// names. authzSvc is stubbed to grant an all-permitting Scope, so if
// scopeForCaller ever fell through to querying it on a Claims-less
// context, this test would catch the widening: the correct behavior is to
// never even call it.
func TestScopeForCaller_NoClaims_FailsClosed(t *testing.T) {
	authzSvc := &fakeAuthz{scope: allPermittingScope{}}
	server := NewLeafLabAPIServer(&fakeRepo{}, authzSvc, nil, nil, discardLogger())

	scope, err := server.scopeForCaller(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope.Permits(authz.EntityRef{Kind: authz.EntityBoard, ID: 1}, authz.Resolution{HouseholdID: 1}) {
		t.Fatal("scopeForCaller with no Claims in context permitted an entity, want fail-closed (permits nothing)")
	}
	if authzSvc.scopeCalls != 0 {
		t.Errorf("authzSvc.ScopeForPrincipal was called %d times with no Claims present, want 0 -- fail-closed must not consult authzSvc at all", authzSvc.scopeCalls)
	}
}

// TestScopeForCaller_WithClaims_DelegatesToAuthzSvc is the companion case:
// once Claims are present, scopeForCaller does consult authzSvc (using the
// authenticated subject), rather than failing closed unconditionally.
func TestScopeForCaller_WithClaims_DelegatesToAuthzSvc(t *testing.T) {
	authzSvc := &fakeAuthz{scope: authz.NewHouseholdScope(5)}
	server := NewLeafLabAPIServer(&fakeRepo{}, authzSvc, nil, nil, discardLogger())

	scope, err := server.scopeForCaller(authedTestCtx("alice"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scope.Permits(authz.EntityRef{Kind: authz.EntityBoard, ID: 1}, authz.Resolution{HouseholdID: 5}) {
		t.Fatal("scope with Claims present did not permit the household authzSvc granted")
	}
	if authzSvc.scopeCalls != 1 {
		t.Errorf("authzSvc.ScopeForPrincipal was called %d times, want exactly 1", authzSvc.scopeCalls)
	}
}

// -- FR57/NFR18.1: ListBoards' display_name projection ------------------------

// TestDisplayNameOrDeviceIDFallback is a plain table-driven unit test of the
// pure helper displayNameOrDeviceIDFallback: a set name is returned as-is,
// an unset (empty) name falls back to the device id.
func TestDisplayNameOrDeviceIDFallback(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		deviceID    string
		want        string
	}{
		{"set name returned as-is", "Living Room Board", "leaflab-abc123", "Living Room Board"},
		{"unset name falls back to device id", "", "leaflab-abc123", "leaflab-abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayNameOrDeviceIDFallback(tc.displayName, tc.deviceID); got != tc.want {
				t.Errorf("displayNameOrDeviceIDFallback(%q, %q) = %q, want %q", tc.displayName, tc.deviceID, got, tc.want)
			}
		})
	}
}

// TestListBoards_DisplayName_ServiceSideFallback proves NFR18.1's "the
// fallback is a rendering decision in the service, not in the BFF" at the
// handler level: ListBoards' response carries the device id for a board
// whose repository row has no display_name, and the stored display_name
// as-is for one that has it -- with the repository (fakeRepo) contributing
// only the raw BoardRow, never the substitution itself.
func TestListBoards_DisplayName_ServiceSideFallback(t *testing.T) {
	repo := &fakeRepo{listBoardsRows: []BoardRow{
		{BoardID: 1, DeviceID: "leaflab-unset", DisplayName: ""},
		{BoardID: 2, DeviceID: "leaflab-set", DisplayName: "Kitchen Board"},
	}}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{scope: authz.NewHouseholdScope(1)}, nil, nil, discardLogger())

	resp, err := server.ListBoards(authedTestCtx("alice"), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Boards) != 2 {
		t.Fatalf("len(Boards) = %d, want 2", len(resp.Boards))
	}
	if resp.Boards[0].DisplayName != "leaflab-unset" {
		t.Errorf("Boards[0].DisplayName = %q, want the device id fallback %q", resp.Boards[0].DisplayName, "leaflab-unset")
	}
	if resp.Boards[1].DisplayName != "Kitchen Board" {
		t.Errorf("Boards[1].DisplayName = %q, want the stored display name %q, not device_id", resp.Boards[1].DisplayName, "Kitchen Board")
	}
}

// -- FR57/FR4/NFR2: SetBoardDisplayName's authorization and audit trail -------

// TestSetBoardDisplayName_NonexistentAndOutOfScope_RefusedBeforeRepositoryWrite
// mirrors TestGetDeviceConfig_NonexistentAndOutOfScope_ByteIdenticalFailure:
// a nonexistent device_id and a device_id that resolves to a board outside
// the caller's household must both refuse before the repository write is
// ever reached (repo.SetBoardDisplayName must not be called), and must
// produce the same NFR2 not-found failure shape.
func TestSetBoardDisplayName_NonexistentAndOutOfScope_RefusedBeforeRepositoryWrite(t *testing.T) {
	callerScope := authz.NewHouseholdScope(1)

	nonexistentRepo := &fakeRepo{}
	nonexistentAuthz := &fakeAuthz{scope: callerScope, resolveErr: authz.ErrNotFound}
	nonexistentServer := NewLeafLabAPIServer(nonexistentRepo, nonexistentAuthz, nil, nil, discardLogger())
	_, nonexistentErr := nonexistentServer.SetBoardDisplayName(authedTestCtx("alice"), &pb.SetBoardDisplayNameRequest{DeviceId: "does-not-exist", DisplayName: "New Name"})
	if nonexistentErr == nil {
		t.Fatal("SetBoardDisplayName for a nonexistent device_id returned nil error, want a refusal")
	}
	if nonexistentRepo.setBoardDisplayNameCalls != 0 {
		t.Errorf("nonexistent-device refusal reached the repository %d times, want 0", nonexistentRepo.setBoardDisplayNameCalls)
	}

	outOfScopeRepo := &fakeRepo{}
	outOfScopeAuthz := &fakeAuthz{
		scope:      callerScope,
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: 7},
		resolveRes: authz.Resolution{HouseholdID: 2}, // a different household than callerScope's 1
	}
	outOfScopeServer := NewLeafLabAPIServer(outOfScopeRepo, outOfScopeAuthz, nil, nil, discardLogger())
	_, outOfScopeErr := outOfScopeServer.SetBoardDisplayName(authedTestCtx("alice"), &pb.SetBoardDisplayNameRequest{DeviceId: "device-belongs-to-household-2", DisplayName: "New Name"})
	if outOfScopeErr == nil {
		t.Fatal("SetBoardDisplayName for an out-of-scope device returned nil error, want a refusal")
	}
	if outOfScopeRepo.setBoardDisplayNameCalls != 0 {
		t.Errorf("out-of-scope refusal reached the repository %d times, want 0", outOfScopeRepo.setBoardDisplayNameCalls)
	}

	nonexistentDetail, ok := contract.FromError(nonexistentErr)
	if !ok {
		t.Fatal("nonexistent-device error carries no Failure detail")
	}
	outOfScopeDetail, ok := contract.FromError(outOfScopeErr)
	if !ok {
		t.Fatal("out-of-scope error carries no Failure detail")
	}
	if nonexistentDetail.Class != string(contract.FailureNotFound) {
		t.Errorf("nonexistent-device Class = %q, want %q", nonexistentDetail.Class, contract.FailureNotFound)
	}
	if !proto.Equal(nonexistentDetail, outOfScopeDetail) {
		t.Errorf("Failure details differ: nonexistent=%v, out-of-scope=%v -- NFR2 requires a non-member's refusal to be indistinguishable from a nonexistent board", nonexistentDetail, outOfScopeDetail)
	}
}

// TestSetBoardDisplayName_Success_EchoesRequestAndRecordsAuditEntry proves
// the success path's two remaining contracts: the response echoes back
// exactly what was requested (never the device_id fallback ListBoards
// applies on read), and the audit.Entry handed to the repository (FR8)
// carries the acting subject, the resolved board's household id, and the
// registered action/entity_kind for this RPC (audit_registry.go) -- so a
// mismatch between the registry and this handler's literal values would
// fail here, not just at MustValidateAuditRegistrations's structural check.
func TestSetBoardDisplayName_Success_EchoesRequestAndRecordsAuditEntry(t *testing.T) {
	repo := &fakeRepo{}
	authzSvc := &fakeAuthz{
		scope:      authz.NewHouseholdScope(9),
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: 42},
		resolveRes: authz.Resolution{HouseholdID: 9},
	}
	server := NewLeafLabAPIServer(repo, authzSvc, nil, nil, discardLogger())

	resp, err := server.SetBoardDisplayName(authedTestCtx("alice"), &pb.SetBoardDisplayNameRequest{DeviceId: "leaflab-42", DisplayName: "Living Room Board"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.DisplayName != "Living Room Board" {
		t.Errorf("response DisplayName = %q, want the request's value echoed back as-is: %q", resp.DisplayName, "Living Room Board")
	}

	if repo.setBoardDisplayNameCalls != 1 {
		t.Fatalf("repo.SetBoardDisplayName called %d times, want exactly 1", repo.setBoardDisplayNameCalls)
	}
	if repo.setBoardDisplayNameBoardID != 42 {
		t.Errorf("boardID passed to repository = %d, want the resolved board id 42", repo.setBoardDisplayNameBoardID)
	}
	if repo.setBoardDisplayNameValue != "Living Room Board" {
		t.Errorf("displayName passed to repository = %q, want %q", repo.setBoardDisplayNameValue, "Living Room Board")
	}

	entry := repo.setBoardDisplayNameEntry
	if entry.ActorSubject != "alice" {
		t.Errorf("audit entry ActorSubject = %q, want %q", entry.ActorSubject, "alice")
	}
	if entry.ActorKind != audit.ActorKindHuman {
		t.Errorf("audit entry ActorKind = %q, want %q", entry.ActorKind, audit.ActorKindHuman)
	}
	if entry.TargetHouseholdID == nil || *entry.TargetHouseholdID != 9 {
		t.Errorf("audit entry TargetHouseholdID = %v, want a pointer to the resolved board's household (9)", entry.TargetHouseholdID)
	}
	wantReg := auditRegistrations[setBoardDisplayNameFullMethod]
	if entry.Action != wantReg.Action {
		t.Errorf("audit entry Action = %q, want the registered action %q", entry.Action, wantReg.Action)
	}
	if entry.EntityKind != wantReg.EntityKind {
		t.Errorf("audit entry EntityKind = %q, want the registered entity_kind %q", entry.EntityKind, wantReg.EntityKind)
	}
	if entry.EntityID == nil || *entry.EntityID != "42" {
		t.Errorf("audit entry EntityID = %v, want a pointer to the board id \"42\"", entry.EntityID)
	}
}
