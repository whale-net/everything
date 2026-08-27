package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	pushconfig "github.com/whale-net/everything/leaflab/api/config"
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

	// getOrCreateBoardID/getOrCreateBoardErr and
	// insertDeviceConfigNextVersionVersion/-Err configure
	// GetOrCreateBoard/InsertDeviceConfigNextVersion's returns -- see their
	// method doc comments below.
	getOrCreateBoardID    int64
	getOrCreateBoardErr   error
	getOrCreateBoardCalls int

	insertDeviceConfigNextVersionVersion int64
	insertDeviceConfigNextVersionErr     error
	insertDeviceConfigNextVersionCalls   []insertDeviceConfigNextVersionCall

	// resolveSensorTypeIDResponses configures resolveSensorTypeID's return
	// per typeName; a name with no entry here resolves to "not found" (see
	// resolveSensorTypeID's doc comment).
	resolveSensorTypeIDResponses map[string]int64

	// getLatestAcceptedConfigResponse configures GetLatestAcceptedConfig's
	// return -- nil (the zero value) means "no accepted config exists for
	// this board", FR82.3's EDIT-with-no-base refusal condition; a
	// non-nil *configpb.DeviceConfig is the EDIT materialisation base
	// server_push_device_config_scope_test.go's FR82 tests configure.
	getLatestAcceptedConfigResponse *configpb.DeviceConfig

	// loadBoardSensorIdentitiesResponse configures LoadBoardSensorIdentities'
	// return -- used by server_push_device_config_scope_test.go's
	// TestPushDeviceConfig_Edit_MaterialisationBase_NeverTheStaleManifest
	// to simulate a device-reported manifest that disagrees with the
	// accepted config, proving FR82.3's materialisation base never leaks
	// from here.
	loadBoardSensorIdentitiesResponse []BoardSensorIdentity
}

// getOrCreateBoardID/getOrCreateBoardErr configure GetOrCreateBoard's
// return, and getOrCreateBoardCalls counts invocations --
// server_push_device_config_test.go's FR1.2/FR1.3 tests need this to reach
// PushDeviceConfig's household-resolution/validation step, unlike this
// file's own tests (which never exercise PushDeviceConfig's write path).
func (f *fakeRepo) GetOrCreateBoard(ctx context.Context, deviceID string) (int64, error) {
	f.getOrCreateBoardCalls++
	return f.getOrCreateBoardID, f.getOrCreateBoardErr
}

// insertDeviceConfigNextVersionCalls records every call (each carrying the
// boardID/configJSON/entry it was invoked with) so a test can assert
// PushDeviceConfig either did or did not reach the storage step -- FR1.3's
// "nothing stored" on a refused push is proven by asserting this stays
// empty, not just that PushDeviceConfig returned an error.
// insertDeviceConfigNextVersionVersion/-Err configure what each call
// returns.
func (f *fakeRepo) InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, entries []pushconfig.Entry, entry audit.Entry) (int64, error) {
	f.insertDeviceConfigNextVersionCalls = append(f.insertDeviceConfigNextVersionCalls, insertDeviceConfigNextVersionCall{
		boardID:    boardID,
		configJSON: configJSON,
		entries:    entries,
		entry:      entry,
	})
	return f.insertDeviceConfigNextVersionVersion, f.insertDeviceConfigNextVersionErr
}

// insertDeviceConfigNextVersionCall is one recorded
// InsertDeviceConfigNextVersion invocation -- see fakeRepo's doc comment.
type insertDeviceConfigNextVersionCall struct {
	boardID    int64
	configJSON []byte
	entries    []pushconfig.Entry
	entry      audit.Entry
}

func (f *fakeRepo) GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error) {
	f.getLatestAcceptedConfigCalls++
	return f.getLatestAcceptedConfigResponse, nil
}

func (f *fakeRepo) GetRegionApplySkips(ctx context.Context, deviceID string) ([]RegionApplySkipRow, error) {
	return nil, nil
}

func (f *fakeRepo) ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error) {
	f.listBoardsScope = scope
	return f.listBoardsRows, f.listBoardsErr
}

func (f *fakeRepo) FindSensorIDByName(ctx context.Context, boardID int64, name string) (int64, bool, error) {
	panic("not used by this file's tests")
}

// resolveSensorTypeID returns "not found" by default (never panics): this
// file's PushDeviceConfig-adjacent tests (server_push_device_config_test.go)
// reach FR82's resolveConfigEntries, which tolerates an unresolved
// sensor_type entirely (see its own doc comment in config_push.go) rather
// than treat it as an error -- so a fixed "not found" default keeps every
// pre-existing test in this file/package passing without asserting
// anything about a specific catalog id none of them care about. A test
// that does care configures resolveSensorTypeIDResponses.
func (f *fakeRepo) resolveSensorTypeID(ctx context.Context, typeName string) (int64, bool, error) {
	if id, ok := f.resolveSensorTypeIDResponses[typeName]; ok {
		return id, true, nil
	}
	return 0, false, nil
}

func (f *fakeRepo) LoadBoardSensorIdentities(ctx context.Context, boardID int64) ([]BoardSensorIdentity, error) {
	// nil (the zero value) short-circuits checkPushConfigIdentity
	// (identity.go) to a no-op, matching most of this file's tests --
	// which don't exercise FR16/FR17 sensor identity resolution.
	// loadBoardSensorIdentitiesResponse lets a test (see
	// server_push_device_config_scope_test.go's "never the stale
	// manifest" case) configure a non-nil response instead.
	return f.loadBoardSensorIdentitiesResponse, nil
}

func (f *fakeRepo) RewireSensorHW(ctx context.Context, sensorID int64, hw *HardwareAddress) error {
	panic("not used by this file's tests")
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

	// resolveResponses/resolveResponseErrs configure Resolve's return
	// per-ref -- keyed per-ref (rather than a single value like resolveRes
	// above, which backs ResolveBoardByDeviceID) because FR1.2/FR1.3's
	// push-time validation calls Resolve once for the pushing board's own
	// household and once per region_id named in the payload, needing
	// different results for different refs within the same test.
	// resolveCallOrder records every ref Resolve was called with, in call
	// order, so a test can assert AssertSameHousehold stops at the first
	// violation rather than resolving every ref regardless.
	resolveResponses    map[authz.EntityRef]authz.Resolution
	resolveResponseErrs map[authz.EntityRef]error
	resolveCallOrder    []authz.EntityRef
}

func (f *fakeAuthz) ScopeForPrincipal(ctx context.Context, principalSubject string) (authz.Scope, error) {
	f.scopeCalls++
	return f.scope, f.scopeErr
}

func (f *fakeAuthz) ResolveBoardByDeviceID(ctx context.Context, deviceID string) (authz.EntityRef, authz.Resolution, error) {
	f.resolveCalls++
	return f.resolveRef, f.resolveRes, f.resolveErr
}

// Resolve satisfies authz.Resolver, which authzResolver now embeds for
// PushDeviceConfig's FR1.2/FR1.3 push-time invariant check
// (server_push_device_config_test.go). It looks resolveResponses/
// resolveResponseErrs up by ref -- a test must configure a response for
// every ref its scenario resolves, or this panics naming the
// unconfigured ref, rather than silently returning a zero Resolution that
// could mask a validation bug as a false pass.
func (f *fakeAuthz) Resolve(ctx context.Context, ref authz.EntityRef) (authz.Resolution, error) {
	f.resolveCallOrder = append(f.resolveCallOrder, ref)
	if err, ok := f.resolveResponseErrs[ref]; ok {
		return authz.Resolution{}, err
	}
	if res, ok := f.resolveResponses[ref]; ok {
		return res, nil
	}
	panic(fmt.Sprintf("fakeAuthz.Resolve: no configured response for %+v -- configure resolveResponses/resolveResponseErrs for every ref this scenario resolves", ref))
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
