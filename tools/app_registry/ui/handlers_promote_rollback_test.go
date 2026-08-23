package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// devUserAuth injects a signed-in user holding every role (htmxauth's
// AuthModeNone dev sentinel) into the request context via the real
// htmxauth middleware -- the context key it uses is package-private, so
// this is the only supported way for a test outside libs/go/htmxauth to
// populate htmxauth.GetUser(r.Context()). Needed for Rollback's screen,
// whose GateCellAction check (FR-43/44/45) would otherwise deny every
// request from a bare context (nil user == no roles).
func devUserAuth(t *testing.T) *htmxauth.Authenticator {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{Mode: htmxauth.AuthModeNone})
	if err != nil {
		t.Fatalf("failed to construct AuthModeNone authenticator: %v", err)
	}
	return auth
}

// --- fakes ------------------------------------------------------------

// fakeEnvironmentClient is a minimal stand-in for pb.EnvironmentRegistryClient
// covering only GetEnvironment, which every promote/rollback render path
// calls first.
type fakeEnvironmentClient struct {
	pb.EnvironmentRegistryClient

	resp *pb.GetEnvironmentResponse
	err  error
}

func (f *fakeEnvironmentClient) GetEnvironment(ctx context.Context, in *pb.GetEnvironmentRequest, opts ...grpc.CallOption) (*pb.GetEnvironmentResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: in.GetKey(), Rank: 0}}, nil
}

// promoteArtifactClient wraps the fakeArtifactClient type declared in
// handlers_builds_test.go (#655) with the one extra method screen
// 50-promote needs (ListArtifacts), rather than editing that shared file.
type promoteArtifactClient struct {
	*fakeArtifactClient
	listArtifactsResp *pb.ListArtifactsResponse
}

func (f *promoteArtifactClient) ListArtifacts(ctx context.Context, in *pb.ListArtifactsRequest, opts ...grpc.CallOption) (*pb.ListArtifactsResponse, error) {
	if f.listArtifactsResp == nil {
		return &pb.ListArtifactsResponse{}, nil
	}
	return f.listArtifactsResp, nil
}

// fakePromotionClient is a minimal stand-in for pb.PromotionRegistryClient.
// Recording every call's IdempotencyKey is the whole point of these tests
// (FR-51): every assertion about key stability/rotation is made by
// inspecting promoteCalls/rollbackCalls, never by re-deriving a key
// independently.
type fakePromotionClient struct {
	pb.PromotionRegistryClient

	promoteCalls []*pb.PromoteRequest
	promoteResp  *pb.PromoteResponse
	promoteErr   error

	rollbackCalls []*pb.RollbackRequest
	rollbackResp  *pb.RollbackResponse
	rollbackErr   error

	// rollbackPreviewResp/Err govern only the informational dry_run=true
	// call handleRollbackShow issues to populate the FR-17 confirmation
	// cards -- kept separate from rollbackResp/Err (the dry_run=false
	// commit) since both go through the same RPC method.
	rollbackPreviewResp *pb.RollbackResponse
	rollbackPreviewErr  error

	listEventsCalls []*pb.ListPromotionEventsRequest
	listEventsResp  *pb.ListPromotionEventsResponse
	listEventsErr   error

	// getDetailsMu guards getDetailsCalls/getDetailsFunc below --
	// fetchPromotionSyncOutcomes (promotion_sync_outcome_data.go) fans out
	// concurrent GetPromotionDetails calls, unlike every other method on
	// this fake, which only ever sees sequential test-driven calls.
	getDetailsMu    sync.Mutex
	getDetailsCalls []*pb.GetPromotionDetailsRequest
	// getDetailsFunc, when set, computes the response/error per request --
	// lets a test return a different result per promotion_id (e.g. one
	// failing lookup among several succeeding ones). Falls back to a
	// minimal default response echoing PromotionId when unset.
	getDetailsFunc func(*pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error)

	retryArgoSyncCalls []*pb.RetryArgoSyncRequest
	retryArgoSyncResp  *pb.RetryArgoSyncResponse
	retryArgoSyncErr   error
}

func (f *fakePromotionClient) Promote(ctx context.Context, in *pb.PromoteRequest, opts ...grpc.CallOption) (*pb.PromoteResponse, error) {
	f.promoteCalls = append(f.promoteCalls, in)
	if f.promoteErr != nil {
		return nil, f.promoteErr
	}
	if f.promoteResp != nil {
		return f.promoteResp, nil
	}
	return &pb.PromoteResponse{Promotion: &pb.Promotion{PromotionId: "promo-1"}}, nil
}

func (f *fakePromotionClient) Rollback(ctx context.Context, in *pb.RollbackRequest, opts ...grpc.CallOption) (*pb.RollbackResponse, error) {
	f.rollbackCalls = append(f.rollbackCalls, in)
	if in.GetDryRun() {
		if f.rollbackPreviewErr != nil {
			return nil, f.rollbackPreviewErr
		}
		if f.rollbackPreviewResp != nil {
			return f.rollbackPreviewResp, nil
		}
		return &pb.RollbackResponse{DryRun: true, Promotion: &pb.Promotion{PromotionId: "rb-1", Version: "v0.9.0"}, Superseded: &pb.Promotion{PromotionId: "cur-1"}}, nil
	}
	if f.rollbackErr != nil {
		return nil, f.rollbackErr
	}
	if f.rollbackResp != nil {
		return f.rollbackResp, nil
	}
	return &pb.RollbackResponse{Promotion: &pb.Promotion{PromotionId: "rb-1"}, Superseded: &pb.Promotion{PromotionId: "prev-1"}}, nil
}

func (f *fakePromotionClient) ListPromotionEvents(ctx context.Context, in *pb.ListPromotionEventsRequest, opts ...grpc.CallOption) (*pb.ListPromotionEventsResponse, error) {
	f.listEventsCalls = append(f.listEventsCalls, in)
	if f.listEventsErr != nil {
		return nil, f.listEventsErr
	}
	if f.listEventsResp != nil {
		return f.listEventsResp, nil
	}
	return &pb.ListPromotionEventsResponse{}, nil
}

func (f *fakePromotionClient) GetPromotionDetails(ctx context.Context, in *pb.GetPromotionDetailsRequest, opts ...grpc.CallOption) (*pb.GetPromotionDetailsResponse, error) {
	f.getDetailsMu.Lock()
	f.getDetailsCalls = append(f.getDetailsCalls, in)
	fn := f.getDetailsFunc
	f.getDetailsMu.Unlock()

	if fn != nil {
		return fn(in)
	}
	return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
		Promotion: &pb.Promotion{PromotionId: in.GetPromotionId()},
	}}, nil
}

// getDetailsCallCount is a thread-safe reader for getDetailsCalls -- tests
// asserting on the fanout's call count must not race
// fetchPromotionSyncOutcomes' still-running goroutines.
func (f *fakePromotionClient) getDetailsCallCount() int {
	f.getDetailsMu.Lock()
	defer f.getDetailsMu.Unlock()
	return len(f.getDetailsCalls)
}

// RetryArgoSync backs handleRetryArgoSync's tests (issue #1033, FR10-FR13).
func (f *fakePromotionClient) RetryArgoSync(ctx context.Context, in *pb.RetryArgoSyncRequest, opts ...grpc.CallOption) (*pb.RetryArgoSyncResponse, error) {
	f.retryArgoSyncCalls = append(f.retryArgoSyncCalls, in)
	if f.retryArgoSyncErr != nil {
		return nil, f.retryArgoSyncErr
	}
	if f.retryArgoSyncResp != nil {
		return f.retryArgoSyncResp, nil
	}
	return &pb.RetryArgoSyncResponse{TemporalWorkflowId: "retry-" + in.GetPromotionId() + "-1"}, nil
}

func newPromoteTestApp(env *fakeEnvironmentClient, artifact pb.ArtifactRegistryClient, promo *fakePromotionClient) *App {
	return &App{
		registry: &RegistryClient{
			Environment: env,
			Artifact:    artifact,
			Promotion:   promo,
		},
	}
}

func defaultArtifactClient() *promoteArtifactClient {
	return &promoteArtifactClient{
		fakeArtifactClient: &fakeArtifactClient{},
		listArtifactsResp: &pb.ListArtifactsResponse{
			Artifacts: []*pb.Artifact{
				{ArtifactId: "art-1", Version: "v1.0.0", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Promotability: pb.Promotability_PROMOTABILITY_PROMOTABLE},
			},
		},
	}
}

// --- helpers ------------------------------------------------------------

var hiddenValueRe = func(name string) *regexp.Regexp {
	return regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`)
}

func hiddenValue(t *testing.T, body, name string) string {
	t.Helper()
	m := hiddenValueRe(name).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("hidden field %q not found in body:\n%s", name, body)
	}
	return m[1]
}

func getPromote(t *testing.T, app *App, owner, kind, env string) (int, string) {
	t.Helper()
	target := "/promote?owner=" + url.QueryEscape(owner) + "&kind=" + url.QueryEscape(kind) + "&env=" + url.QueryEscape(env)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handlePromoteShow(w, req)
	return w.Code, w.Body.String()
}

func postPromote(t *testing.T, app *App, form url.Values) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/promote", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handlePromoteSubmit(w, req)
	return w.Code, w.Body.String()
}

func baseCommitForm(idemKey, intentFP, dryRunFP string) url.Values {
	return url.Values{
		"owner":       {"platform-app"},
		"kind":        {"image"},
		"env":         {"prod"},
		"action":      {"commit"},
		"artifact_id": {"art-1"},
		"reason":      {"ship the fix"},
		"idem_key":    {idemKey},
		"intent_fp":   {intentFP},
		"dryrun_fp":   {dryRunFP},
	}
}

// --- Promote: key stability (must NOT change) ----------------------------

func TestPromote_KeyMintedOnceOnShow(t *testing.T) {
	app := newPromoteTestApp(&fakeEnvironmentClient{}, defaultArtifactClient(), &fakePromotionClient{})
	code, body := getPromote(t, app, "platform-app", "image", "dev")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	key := hiddenValue(t, body, "idem_key")
	if key == "" {
		t.Fatal("expected a non-empty idem_key minted on GET render")
	}
}

func TestPromote_KeyStableAcrossDoubleSubmit(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	promo := &fakePromotionClient{promoteErr: status.Error(codes.Unavailable, "simulated transport failure")}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	// First submit: dry_run to get a fingerprint against dev (rank 0, so
	// commit doesn't require a verified dry run), but exercise the
	// double-submit purely on commit to keep this test to the point.
	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	_, body1 := postPromote(t, app, form)
	key1 := hiddenValue(t, body1, "idem_key")
	fp1 := hiddenValue(t, body1, "intent_fp")
	if key1 == "" {
		t.Fatal("expected a minted idem_key on first submit")
	}

	// Double-submit: the exact same form state POSTed again (as a browser
	// back-button resubmit or JS double-click would) must keep the SAME
	// key -- it must not be treated as a new intent.
	form2 := baseCommitForm(key1, fp1, "")
	form2.Set("env", "dev")
	_, body2 := postPromote(t, app, form2)
	key2 := hiddenValue(t, body2, "idem_key")
	if key2 != key1 {
		t.Errorf("double-submit of identical form state rotated the key: %q -> %q", key1, key2)
	}

	if len(promo.promoteCalls) != 2 {
		t.Fatalf("expected 2 Promote calls, got %d", len(promo.promoteCalls))
	}
	if promo.promoteCalls[0].GetIdempotencyKey() != promo.promoteCalls[1].GetIdempotencyKey() {
		t.Errorf("the two RPC calls carried different idempotency keys: %q vs %q",
			promo.promoteCalls[0].GetIdempotencyKey(), promo.promoteCalls[1].GetIdempotencyKey())
	}
}

func TestPromote_KeyStableAcrossRetryAfterTransportFailure(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	promo := &fakePromotionClient{promoteErr: status.Error(codes.Unavailable, "simulated timeout")}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	_, body1 := postPromote(t, app, form)
	if !strings.Contains(body1, "simulated timeout") {
		t.Fatalf("expected the transport failure to surface inline, body: %s", body1)
	}
	key1 := hiddenValue(t, body1, "idem_key")
	fp1 := hiddenValue(t, body1, "intent_fp")

	// User retries -- server now succeeds. The retry must carry the SAME
	// key as the failed attempt (FR-51's "retry of a call that failed in
	// transport or timed out" case), so a server-side replay-of-the-failed-
	// attempt-turned-success can be detected as the same intent.
	promo.promoteErr = nil
	form2 := baseCommitForm(key1, fp1, "")
	form2.Set("env", "dev")
	_, body2 := postPromote(t, app, form2)
	if !strings.Contains(body2, "Promotion recorded.") {
		t.Fatalf("expected the retry to succeed, body: %s", body2)
	}
	if promo.promoteCalls[1].GetIdempotencyKey() != key1 {
		t.Errorf("retry sent a different idempotency key: got %q, want %q", promo.promoteCalls[1].GetIdempotencyKey(), key1)
	}
}

func TestPromote_KeyStableAcrossValidationRerender(t *testing.T) {
	// Above dev (rank > 0), commit is rejected client-side unless a dry run
	// was already verified against the current form state -- exactly the
	// FR-16 validation re-render case.
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "prod", Rank: 10}}}
	promo := &fakePromotionClient{}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	_, body1 := postPromote(t, app, form)
	if !strings.Contains(body1, "Run a dry run") {
		t.Fatalf("expected the missing-dry-run validation error, body: %s", body1)
	}
	key1 := hiddenValue(t, body1, "idem_key")
	fp1 := hiddenValue(t, body1, "intent_fp")
	if key1 == "" {
		t.Fatal("expected a key even on a validation re-render")
	}

	// Re-submit the SAME (still-invalid) state -- key must not rotate.
	form2 := baseCommitForm(key1, fp1, "")
	_, body2 := postPromote(t, app, form2)
	key2 := hiddenValue(t, body2, "idem_key")
	if key2 != key1 {
		t.Errorf("validation re-render rotated the key: %q -> %q", key1, key2)
	}
	if len(promo.promoteCalls) != 0 {
		t.Fatalf("a UI-policy validation failure must never call Promote, got %d calls", len(promo.promoteCalls))
	}
}

// --- Promote: key rotation (MUST change) ---------------------------------

func TestPromote_KeyRotatesOnFieldChange(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}

	cases := []struct {
		name   string
		mutate func(v url.Values)
	}{
		{"entity", func(v url.Values) { v.Set("owner", "other-app") }},
		{"environment", func(v url.Values) { v.Set("env", "dev2") }},
		{"target_artifact", func(v url.Values) { v.Set("artifact_id", "art-2") }},
		{"override_ack", func(v url.Values) { v.Set("override_ack", "true") }},
		{"reason", func(v url.Values) { v.Set("reason", "a different reason entirely") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			promo := &fakePromotionClient{promoteErr: status.Error(codes.Unavailable, "force a re-render without committing")}
			app := newPromoteTestApp(env, defaultArtifactClient(), promo)

			form := baseCommitForm("", "", "")
			_, body1 := postPromote(t, app, form)
			key1 := hiddenValue(t, body1, "idem_key")
			fp1 := hiddenValue(t, body1, "intent_fp")

			form2 := baseCommitForm(key1, fp1, "")
			tc.mutate(form2)
			_, body2 := postPromote(t, app, form2)
			key2 := hiddenValue(t, body2, "idem_key")
			if key2 == key1 {
				t.Errorf("changing %s did not rotate the key (stayed %q)", tc.name, key1)
			}
		})
	}
}

func TestPromote_KeyRotatesAfterCommittedWrite(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	promo := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{Promotion: &pb.Promotion{PromotionId: "promo-1", Version: "v1.0.0"}},
	}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	_, body1 := postPromote(t, app, form)
	if !strings.Contains(body1, "Promotion recorded.") {
		t.Fatalf("expected a successful commit, body: %s", body1)
	}
	usedKey := promo.promoteCalls[0].GetIdempotencyKey()

	// A fresh GET (the next intent -- e.g. the user navigates back to
	// promote again) must mint a brand-new key, never the one just spent.
	_, body2 := getPromote(t, app, "platform-app", "image", "dev")
	newKey := hiddenValue(t, body2, "idem_key")
	if newKey == usedKey {
		t.Errorf("a fresh render after a committed write reused the spent key %q", usedKey)
	}
}

func TestPromote_DistinctKeysAcrossSessionsWithIdenticalFormContent(t *testing.T) {
	app := newPromoteTestApp(&fakeEnvironmentClient{}, defaultArtifactClient(), &fakePromotionClient{})
	_, body1 := getPromote(t, app, "platform-app", "image", "dev")
	_, body2 := getPromote(t, app, "platform-app", "image", "dev")
	key1 := hiddenValue(t, body1, "idem_key")
	key2 := hiddenValue(t, body2, "idem_key")
	if key1 == key2 {
		t.Errorf("two independent renders of identical form content minted the same key %q -- the key must never be derived from form content", key1)
	}
}

func TestPromote_KeyNamespaceDistinctFromRollback(t *testing.T) {
	// A key/fingerprint pair minted by Promote must never be accepted as
	// "unchanged" by Rollback's own freshness check -- they compute their
	// fingerprints over disjoint field sets, so a stolen/replayed pair from
	// one flow can never coast through the other's stability check.
	promoEnv := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	promoApp := newPromoteTestApp(promoEnv, defaultArtifactClient(), &fakePromotionClient{promoteErr: status.Error(codes.Unavailable, "no commit needed")})
	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	_, body1 := postPromote(t, promoApp, form)
	promoKey := hiddenValue(t, body1, "idem_key")
	promoFP := hiddenValue(t, body1, "intent_fp")

	rbEnv := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	rbPromo := &fakePromotionClient{}
	rbApp := newPromoteTestApp(rbEnv, defaultArtifactClient(), rbPromo)
	rbForm := url.Values{
		"owner":     {"platform-app"},
		"kind":      {"image"},
		"env":       {"dev"},
		"reason":    {"ship the fix"},
		"idem_key":  {promoKey},
		"intent_fp": {promoFP},
	}
	req := httptest.NewRequest(http.MethodPost, "/rollback", strings.NewReader(rbForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(rbApp.handleRollbackSubmit)(w, req)

	// The rollback commit call is the source of truth for which key was
	// actually spent -- the rendered page may have already moved to the
	// no-hidden-fields confirmation view by this point.
	if len(rbPromo.rollbackCalls) == 0 {
		t.Fatal("expected the rollback commit call to happen")
	}
	rbKey := rbPromo.rollbackCalls[len(rbPromo.rollbackCalls)-1].GetIdempotencyKey()
	if rbKey == promoKey {
		t.Errorf("Rollback accepted a Promote-minted key/fingerprint pair as unchanged and reused it: %q", promoKey)
	}
}

// --- Dry run --------------------------------------------------------------

func TestPromote_DryRunDoesNotRotateKeyAcrossRepeatedDryRuns(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "prod", Rank: 10}}}
	promo := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{DryRun: true, Promotion: &pb.Promotion{Version: "v1.0.0"}},
	}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	form.Set("action", "dry_run")
	_, body1 := postPromote(t, app, form)
	if !strings.Contains(body1, "Dry run only") {
		t.Fatalf("expected dry-run framing, body: %s", body1)
	}
	key1 := hiddenValue(t, body1, "idem_key")
	fp1 := hiddenValue(t, body1, "intent_fp")
	dryRunFP1 := hiddenValue(t, body1, "dryrun_fp")

	// A second dry run against the identical (still-unchanged) form state
	// must not mint a new key -- the key is not "reserved" or consumed by
	// dry_run at all.
	form2 := baseCommitForm(key1, fp1, dryRunFP1)
	form2.Set("action", "dry_run")
	_, body2 := postPromote(t, app, form2)
	key2 := hiddenValue(t, body2, "idem_key")
	if key2 != key1 {
		t.Errorf("a repeated dry run rotated the key: %q -> %q", key1, key2)
	}

	// The eventual commit against that same unchanged state reuses the
	// same key too -- proving the dry run(s) never consumed it.
	commitForm := baseCommitForm(key1, fp1, dryRunFP1)
	promo.promoteResp = &pb.PromoteResponse{Promotion: &pb.Promotion{PromotionId: "promo-1", Version: "v1.0.0"}}
	_, body3 := postPromote(t, app, commitForm)
	if !strings.Contains(body3, "Promotion recorded.") {
		t.Fatalf("expected the commit to succeed, body: %s", body3)
	}
	if promo.promoteCalls[len(promo.promoteCalls)-1].GetIdempotencyKey() != key1 {
		t.Errorf("commit after dry run(s) used a different key than the one minted at render time")
	}
}

func TestPromote_DryRunResultFramedAsNonCommitted(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	promo := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{DryRun: true, Promotion: &pb.Promotion{Version: "v1.0.0", Digest: "sha256:bbbb"}},
	}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	form.Set("action", "dry_run")
	_, body := postPromote(t, app, form)

	if !strings.Contains(body, "alert-warning") {
		t.Errorf("dry-run result must render inside an alert-warning, not a success/confirmation container, body: %s", body)
	}
	if strings.Contains(body, "alert-success") {
		t.Errorf("dry-run result must never render inside an alert-success container (FR-15), body: %s", body)
	}
	if !strings.Contains(body, "no write performed") {
		t.Errorf("dry-run result must explicitly say no write was performed, body: %s", body)
	}
	if strings.Contains(body, "Promotion recorded.") {
		t.Errorf("a dry run must never render the committed-write confirmation copy, body: %s", body)
	}
}

// --- Confirmation rendering (FR-52) ---------------------------------------

func TestPromote_ConfirmationRendersResponseIdentityNotForm(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	// The response's identity deliberately differs from the submitted
	// form (different artifact_id was chosen server-side, different
	// version/digest/environment_key rendered back) -- FR-52 requires the
	// confirmation to render THIS, not an echo of the submitted form.
	promo := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{
			Promotion: &pb.Promotion{
				PromotionId:    "promo-xyz",
				EnvironmentKey: "prod-eu", // form said "dev"
				Version:        "v9.9.9",  // never submitted
				Digest:         "sha256:cccccccccccccccccccccccccccccccc",
				IsOverride:     true,
				ValidFrom:      1700000000,
			},
			Superseded: &pb.Promotion{PromotionId: "promo-old", Version: "v1.0.0", ValidTo: 1699999999},
			Event: &pb.PromotionEvent{
				Actor:      "server-derived-actor",
				Reason:     "server-derived-reason",
				OccurredAt: 1700000001,
			},
		},
	}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	form.Set("artifact_id", "art-1")
	form.Set("reason", "the reason I typed")
	_, body := postPromote(t, app, form)

	for _, want := range []string{"promo-xyz", "prod-eu", "v9.9.9", "server-derived-actor", "server-derived-reason"} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation body missing response-derived value %q, body: %s", want, body)
		}
	}
	if strings.Contains(body, "the reason I typed") {
		t.Errorf("confirmation must never echo the submitted form's reason; response's own event.reason is what renders")
	}
}

func TestPromote_AlreadyPromotedRendersAsNoChangeNotSuccess(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	promo := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{
			Promotion:       &pb.Promotion{PromotionId: "promo-1", Version: "v1.0.0"},
			AlreadyPromoted: true,
			// No Event -- exactly the already_promoted shape (promotion.go:99).
		},
		listEventsResp: &pb.ListPromotionEventsResponse{
			Events: []*pb.PromotionEvent{{Actor: "earlier-actor", Reason: "earlier reason", OccurredAt: 1000}},
		},
	}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	_, body := postPromote(t, app, form)

	if !strings.Contains(body, "No change written.") {
		t.Fatalf("already_promoted must render an explicit no-change outcome, body: %s", body)
	}
	if strings.Contains(body, "Promotion recorded.") {
		t.Errorf("already_promoted must never render as a successful promotion, body: %s", body)
	}
	if !strings.Contains(body, "earlier-actor") || !strings.Contains(body, "earlier reason") {
		t.Errorf("already_promoted must attribute actor/reason to the resolved earlier event, body: %s", body)
	}
	if !strings.Contains(body, "EARLIER promotion") {
		t.Errorf("already_promoted's attribution must be explicitly labelled as the earlier promotion's, not this submission's, body: %s", body)
	}
	if len(promo.listEventsCalls) != 1 || promo.listEventsCalls[0].GetPromotionId() != "promo-1" {
		t.Errorf("expected exactly one ListPromotionEvents(promotion_id=promo-1) call, got %+v", promo.listEventsCalls)
	}
}

func TestPromote_AlreadyPromotedWithUnresolvableEventNeverBlankOrCurrentSubmission(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	promo := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{
			Promotion:       &pb.Promotion{PromotionId: "promo-1", Version: "v1.0.0"},
			AlreadyPromoted: true,
		},
		listEventsErr: status.Error(codes.Unavailable, "lookup failed"),
	}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)

	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	form.Set("reason", "my actual submitted reason")
	_, body := postPromote(t, app, form)

	if !strings.Contains(body, "unavailable") {
		t.Errorf("an unresolvable earlier event must be stated as unavailable, not silently omitted, body: %s", body)
	}
	if strings.Contains(body, "my actual submitted reason") {
		t.Errorf("an unresolvable earlier event must never fall back to the current submission's own form-derived reason, body: %s", body)
	}
}

func TestPromote_NoTimestampComparisonAffectsRendering(t *testing.T) {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}

	// valid_from/occurred_at skewed far into the future relative to the
	// test process's own clock (time.Now()) must not change how the
	// result is classified/rendered -- no code path may use a timestamp
	// comparison as a correctness signal.
	farFuture := int64(4102444800) // year 2100
	promo := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{
			Promotion: &pb.Promotion{PromotionId: "promo-1", Version: "v1.0.0", ValidFrom: farFuture},
			Event:     &pb.PromotionEvent{Actor: "a", Reason: "r", OccurredAt: farFuture},
		},
	}
	app := newPromoteTestApp(env, defaultArtifactClient(), promo)
	form := baseCommitForm("", "", "")
	form.Set("env", "dev")
	_, bodyFuture := postPromote(t, app, form)
	if !strings.Contains(bodyFuture, "Promotion recorded.") {
		t.Errorf("a future-skewed valid_from must not change the success classification, body: %s", bodyFuture)
	}

	farPast := int64(0)
	promo2 := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{
			Promotion: &pb.Promotion{PromotionId: "promo-2", Version: "v1.0.0", ValidFrom: farPast},
			Event:     &pb.PromotionEvent{Actor: "a", Reason: "r", OccurredAt: farPast},
		},
	}
	app2 := newPromoteTestApp(env, defaultArtifactClient(), promo2)
	_, bodyPast := postPromote(t, app2, form)
	if !strings.Contains(bodyPast, "Promotion recorded.") {
		t.Errorf("a zero/epoch valid_from must not change the success classification, body: %s", bodyPast)
	}
}

// --- Rollback: mirrors the Promote cases above (both screens covered) ----

func getRollback(t *testing.T, app *App, owner, kind, env string) (int, string) {
	t.Helper()
	target := "/rollback?owner=" + url.QueryEscape(owner) + "&kind=" + url.QueryEscape(kind) + "&env=" + url.QueryEscape(env)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleRollbackShow)(w, req)
	return w.Code, w.Body.String()
}

func postRollback(t *testing.T, app *App, form url.Values) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/rollback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleRollbackSubmit)(w, req)
	return w.Code, w.Body.String()
}

func baseRollbackForm(idemKey, intentFP string) url.Values {
	return url.Values{
		"owner":     {"platform-app"},
		"kind":      {"image"},
		"env":       {"dev"},
		"reason":    {"revert the bad rollout"},
		"idem_key":  {idemKey},
		"intent_fp": {intentFP},
	}
}

func rollbackTestApp(promo *fakePromotionClient) *App {
	env := &fakeEnvironmentClient{resp: &pb.GetEnvironmentResponse{Environment: &pb.Environment{Key: "dev", Rank: 0}}}
	return newPromoteTestApp(env, defaultArtifactClient(), promo)
}

func TestRollback_KeyMintedOnceOnShow(t *testing.T) {
	promo := &fakePromotionClient{
		rollbackResp: &pb.RollbackResponse{Promotion: &pb.Promotion{PromotionId: "prev-1"}, Superseded: &pb.Promotion{PromotionId: "cur-1"}},
	}
	app := rollbackTestApp(promo)
	code, body := getRollback(t, app, "platform-app", "image", "dev")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	key := hiddenValue(t, body, "idem_key")
	if key == "" {
		t.Fatal("expected a non-empty idem_key minted on GET render")
	}
	// The informational dry-run preview issued by the GET must never carry
	// an idempotency key -- it short-circuits before the key is read.
	if len(promo.rollbackCalls) != 1 || promo.rollbackCalls[0].GetIdempotencyKey() != "" {
		t.Errorf("the informational preview dry run must not spend/carry a key, calls: %+v", promo.rollbackCalls)
	}
}

func TestRollback_KeyStableAcrossDoubleSubmitAndRetry(t *testing.T) {
	promo := &fakePromotionClient{
		rollbackResp: &pb.RollbackResponse{Promotion: &pb.Promotion{PromotionId: "prev-1"}, Superseded: &pb.Promotion{PromotionId: "cur-1"}},
		rollbackErr:  status.Error(codes.Unavailable, "simulated timeout"),
	}
	app := rollbackTestApp(promo)

	form := baseRollbackForm("", "")
	_, body1 := postRollback(t, app, form)
	if !strings.Contains(body1, "simulated timeout") {
		t.Fatalf("expected the transport failure to surface, body: %s", body1)
	}
	key1 := hiddenValue(t, body1, "idem_key")
	fp1 := hiddenValue(t, body1, "intent_fp")

	// Double-submit of the identical failed state.
	form2 := baseRollbackForm(key1, fp1)
	_, body2 := postRollback(t, app, form2)
	key2 := hiddenValue(t, body2, "idem_key")
	if key2 != key1 {
		t.Errorf("double-submit rotated the key: %q -> %q", key1, key2)
	}

	// Retry succeeds -- same key must be sent.
	promo.rollbackErr = nil
	form3 := baseRollbackForm(key1, fp1)
	_, body3 := postRollback(t, app, form3)
	if !strings.Contains(body3, "Rollback recorded.") {
		t.Fatalf("expected the retry to succeed, body: %s", body3)
	}
	last := promo.rollbackCalls[len(promo.rollbackCalls)-1]
	if last.GetIdempotencyKey() != key1 {
		t.Errorf("retry sent a different idempotency key: got %q, want %q", last.GetIdempotencyKey(), key1)
	}
}

func TestRollback_KeyRotatesOnReasonChange(t *testing.T) {
	promo := &fakePromotionClient{rollbackErr: status.Error(codes.Unavailable, "no commit needed")}
	app := rollbackTestApp(promo)

	form := baseRollbackForm("", "")
	_, body1 := postRollback(t, app, form)
	key1 := hiddenValue(t, body1, "idem_key")
	fp1 := hiddenValue(t, body1, "intent_fp")

	form2 := baseRollbackForm(key1, fp1)
	form2.Set("reason", "a completely different reason")
	_, body2 := postRollback(t, app, form2)
	key2 := hiddenValue(t, body2, "idem_key")
	if key2 == key1 {
		t.Errorf("changing the reason did not rotate the key (stayed %q)", key1)
	}
}

func TestRollback_KeyRotatesAfterCommittedWrite(t *testing.T) {
	promo := &fakePromotionClient{
		rollbackResp: &pb.RollbackResponse{Promotion: &pb.Promotion{PromotionId: "prev-1"}, Superseded: &pb.Promotion{PromotionId: "cur-1"}},
	}
	app := rollbackTestApp(promo)

	form := baseRollbackForm("", "")
	_, body1 := postRollback(t, app, form)
	if !strings.Contains(body1, "Rollback recorded.") {
		t.Fatalf("expected a successful commit, body: %s", body1)
	}
	usedKey := promo.rollbackCalls[0].GetIdempotencyKey()

	_, body2 := getRollback(t, app, "platform-app", "image", "dev")
	newKey := hiddenValue(t, body2, "idem_key")
	if newKey == usedKey {
		t.Errorf("a fresh render after a committed rollback reused the spent key %q", usedKey)
	}
}

func TestRollback_ConfirmationRendersResponseIdentityNotForm(t *testing.T) {
	promo := &fakePromotionClient{
		rollbackResp: &pb.RollbackResponse{
			Promotion: &pb.Promotion{
				PromotionId:    "rb-xyz",
				EnvironmentKey: "prod-eu", // form said "dev"
				Version:        "v0.9.0",  // the prior version, not anything typed
				Digest:         "sha256:dddddddddddddddddddddddddddddddd",
				ValidFrom:      1700000000,
			},
			Superseded: &pb.Promotion{PromotionId: "cur-1", Version: "v1.0.0", ValidTo: 1699999999},
			Event: &pb.PromotionEvent{
				Actor:      "server-derived-actor",
				Reason:     "server-derived-reason",
				OccurredAt: 1700000001,
			},
		},
	}
	app := rollbackTestApp(promo)

	form := baseRollbackForm("", "")
	form.Set("reason", "the reason I typed")
	_, body := postRollback(t, app, form)

	for _, want := range []string{"rb-xyz", "prod-eu", "v0.9.0", "server-derived-actor", "server-derived-reason"} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation body missing response-derived value %q, body: %s", want, body)
		}
	}
	if strings.Contains(body, "the reason I typed") {
		t.Errorf("confirmation must never echo the submitted form's reason; response's own event.reason is what renders")
	}
}

// TestPromoteKindFromQuery covers the "kind" query/form value accepted by
// both the promote and rollback handlers (they share this parser). "binary"
// was added alongside tool-binary promotability (#780) -- before that,
// there was no reachable kind value for tools-release_helper_go/
// tools-app-registry at all, so promote attempts against them either 404'd
// on "invalid kind" or (with kind=image, the only value anyone thought to
// try) rendered NOT_PROMOTABLE for the wrong artifact kind. "firmware" is
// deliberately still rejected: DerivePromotability always returns
// NOT_PROMOTABLE for it, so a promote screen for it would never have
// anything to show.
func TestPromoteKindFromQuery(t *testing.T) {
	cases := []struct {
		raw     string
		want    pb.ArtifactKind
		wantErr bool
	}{
		{"image", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, false},
		{"chart", pb.ArtifactKind_ARTIFACT_KIND_CHART, false},
		{"binary", pb.ArtifactKind_ARTIFACT_KIND_BINARY, false},
		{"firmware", pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED, true},
		{"", pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED, true},
		{"bogus", pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED, true},
	}
	for _, c := range cases {
		got, err := promoteKindFromQuery(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("promoteKindFromQuery(%q): expected error, got nil (kind=%v)", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("promoteKindFromQuery(%q): unexpected error: %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("promoteKindFromQuery(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// TestDefaultPromoteCandidate_HighestSemverNotFirst covers #815: the
// candidate list stays ordered state_changed_at DESC (recency), so the
// highest-semver artifact is not necessarily candidates[0]. The default
// selection must still be the highest semver, compared numerically
// (major, then minor, then patch) -- never candidates[0] and never a
// lexicographic string compare (which would also get "0.0.42" vs "0.1.0"
// wrong, just in the other direction).
func TestDefaultPromoteCandidate_HighestSemverNotFirst(t *testing.T) {
	// v0.0.42 is listed first (most recently state-changed, e.g. due to
	// the version-advance-on-failure behavior in #814) even though v0.1.0
	// is the higher real version.
	candidates := []*pb.Artifact{
		{ArtifactId: "art-recent-low", Version: "v0.0.42"},
		{ArtifactId: "art-older-high", Version: "v0.1.0"},
		{ArtifactId: "art-mid", Version: "v0.0.9"},
	}
	got := defaultPromoteCandidate(candidates)
	if got == nil || got.GetArtifactId() != "art-older-high" {
		t.Fatalf("defaultPromoteCandidate() = %v, want art-older-high (v0.1.0)", got)
	}
}

func TestDefaultPromoteCandidate_Empty(t *testing.T) {
	if got := defaultPromoteCandidate(nil); got != nil {
		t.Fatalf("defaultPromoteCandidate(nil) = %v, want nil", got)
	}
}

func TestDefaultPromoteCandidate_UnparseableFallsBackToFirst(t *testing.T) {
	candidates := []*pb.Artifact{
		{ArtifactId: "art-a", Version: "not-a-semver"},
		{ArtifactId: "art-b", Version: "also-bad"},
	}
	got := defaultPromoteCandidate(candidates)
	if got == nil || got.GetArtifactId() != "art-a" {
		t.Fatalf("defaultPromoteCandidate() = %v, want art-a (fallback to first when nothing parses)", got)
	}
}

// TestPromote_DefaultSelectionIsHighestSemverNotFirstInList is the
// HTTP-level counterpart of TestDefaultPromoteCandidate_HighestSemverNotFirst:
// it confirms handlePromoteShow actually wires the highest-semver candidate
// into the rendered form's pre-selected option, not candidates[0]. The
// dropdown's own display order (state_changed_at DESC) is untouched --
// this only asserts which option carries the `selected` attribute.
func TestPromote_DefaultSelectionIsHighestSemverNotFirstInList(t *testing.T) {
	artifact := &promoteArtifactClient{
		fakeArtifactClient: &fakeArtifactClient{},
		listArtifactsResp: &pb.ListArtifactsResponse{
			Artifacts: []*pb.Artifact{
				{ArtifactId: "art-recent-low", Version: "v0.0.42", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Promotability: pb.Promotability_PROMOTABILITY_PROMOTABLE},
				{ArtifactId: "art-older-high", Version: "v0.1.0", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Promotability: pb.Promotability_PROMOTABILITY_PROMOTABLE},
			},
		},
	}
	app := newPromoteTestApp(&fakeEnvironmentClient{}, artifact, &fakePromotionClient{})
	code, body := getPromote(t, app, "platform-app", "image", "dev")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}

	selectedRe := regexp.MustCompile(`<option value="([^"]+)"[^>]*\sselected[^>]*>`)
	m := selectedRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no selected <option> found in body:\n%s", body)
	}
	if m[1] != "art-older-high" {
		t.Errorf("selected artifact_id = %q, want %q (highest semver v0.1.0, not the recency-first v0.0.42)", m[1], "art-older-high")
	}
}

func TestPromote_BinaryToolApp_RendersCandidatesAndAllowsPromotion(t *testing.T) {
	artifact := &promoteArtifactClient{
		fakeArtifactClient: &fakeArtifactClient{},
		listArtifactsResp: &pb.ListArtifactsResponse{
			Artifacts: []*pb.Artifact{
				{ArtifactId: "art-bin-1", Version: "v0.3.0", Digest: "sha256:cccccccccccccccccccccccccccccccc", Kind: pb.ArtifactKind_ARTIFACT_KIND_BINARY, Promotability: pb.Promotability_PROMOTABILITY_PROMOTABLE},
			},
		},
	}
	promoClient := &fakePromotionClient{
		promoteResp: &pb.PromoteResponse{
			Promotion: &pb.Promotion{
				PromotionId: "promo-1",
				Version:     "v0.3.0",
			},
		},
	}
	app := newPromoteTestApp(&fakeEnvironmentClient{}, artifact, promoClient)
	code, body := getPromote(t, app, "tools-app-registry", "binary", "dev")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}

	if !strings.Contains(body, "v0.3.0") {
		t.Errorf("expected candidate version v0.3.0 in promote form, got: %s", body)
	}
}
