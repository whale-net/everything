package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/events"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// This file guards task #2277 (manmanv2 M5 Activity live status): FR15's
// no-transport/no-fallback constraint, NFR10's fleet-wide topic/authorized
// set (explicitly distinct from resolveScopedServerGameConfigs), and the
// empty-topic-set 503 guard shared with handleDeploymentsLiveSSE via
// requireLiveTopics (handlers_live_common.go) rather than forked.
//
// Reuses fakeActivityAPIClient (handlers_activity_test.go) -- the same fake
// backs both handleActivity's plain-request path and
// handleActivityLiveSSE's stream path, since both are built on the exact
// same buildActivityPageData/resolveFleetWideActivitySet call graph -- and
// the SSE machinery (fakeLiveTransport, newTestHub, waitForHandlerRegistered)
// from handlers_sessions_live_test.go, all in package main.

func newActivityLiveTestApp(api *fakeActivityAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}, auth: liveTestAuthenticator}
}

// runActivityLiveSSE runs handleActivityLiveSSE for wait, then cancels the
// request context and returns everything written to the response -- mirrors
// runLiveSSE (handlers_sessions_live_test.go) but for the fleet-wide route.
func runActivityLiveSSE(t *testing.T, app *App, wait time.Duration) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/live/activity", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		app.handleActivityLiveSSE(w, req)
		close(done)
	}()

	time.Sleep(wait)
	cancel()
	<-done

	return w.Code, w.Body.String()
}

// --- 1. Empty topic set -> 503, never a panic (mirrors
// TestHandleDeploymentsLiveSSE_NilHub_Returns503NotPanicOrHang's
// hang/panic guard, but for the empty-derivation path specifically) ---

func TestHandleActivityLiveSSE_NoServers_EmptyTopicSet_503NotPanic(t *testing.T) {
	api := &fakeActivityAPIClient{}
	app := newActivityLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	code, body := runActivityLiveSSE(t, app, 50*time.Millisecond)

	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (empty fleet-wide topic set must be a handled 503, not a panic); body: %s", code, http.StatusServiceUnavailable, body)
	}
}

func TestHandleActivityLiveSSE_ServersWithNoConfigs_EmptyTopicSet_503NotPanic(t *testing.T) {
	// NFR10: servers exist but resolve to zero ServerGameConfigs anywhere in
	// the fleet -- still an empty derived topic set, still a handled 503.
	api := &fakeActivityAPIClient{
		servers:        []*manmanpb.Server{{ServerId: 10, Name: "Alpha"}, {ServerId: 20, Name: "Beta"}},
		sgcsByServerID: map[int64][]*manmanpb.ServerGameConfig{},
	}
	app := newActivityLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	code, body := runActivityLiveSSE(t, app, 50*time.Millisecond)

	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusServiceUnavailable, body)
	}
}

func TestHandleActivityLiveSSE_NilHub_Returns503NotPanicOrHang(t *testing.T) {
	app := &App{sseHub: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/live/activity", nil)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		app.handleActivityLiveSSE(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleActivityLiveSSE hung with app.sseHub == nil")
	}

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// --- 2. Fleet-wide topics span multiple servers, and are not what
// resolveScopedServerGameConfigs (the server-scoped derivation
// /sessions and handleDeploymentsLiveSSE use) would produce ---

func fleetActivityFixture() *fakeActivityAPIClient {
	return &fakeActivityAPIClient{
		servers: []*manmanpb.Server{
			{ServerId: 10, Name: "Alpha"},
			{ServerId: 20, Name: "Beta"},
		},
		sgcsByServerID: map[int64][]*manmanpb.ServerGameConfig{
			10: {{ServerGameConfigId: 55, ServerId: 10, GameConfigId: 900, Status: "active"}},
			20: {{ServerGameConfigId: 66, ServerId: 20, GameConfigId: 901, Status: "active"}},
		},
		gameConfigsByID: map[int64]*manmanpb.GameConfig{
			900: {ConfigId: 900, GameId: 9000, Name: "Survival"},
			901: {ConfigId: 901, GameId: 9001, Name: "Hardcore"},
		},
		gamesByID: map[int64]*manmanpb.Game{
			9000: {GameId: 9000, Name: "Minecraft"},
			9001: {GameId: 9001, Name: "Valheim"},
		},
	}
}

func TestHandleActivityLiveSSE_FleetWideTopicsSpanMultipleServers(t *testing.T) {
	api := fleetActivityFixture()
	app := newActivityLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	code, body := runActivityLiveSSE(t, app, 100*time.Millisecond)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, sgcID := range []int64{55, 66} {
		topic := events.TopicForDeployment(sgcID)
		if !strings.Contains(body, "event: "+topic+"\n") {
			t.Errorf("expected an initial swap for fleet-wide topic %s (sgc %d, spans both servers), got body %q", topic, sgcID, body)
		}
	}
}

func TestHandleActivityLiveSSE_FleetWideTopicsDifferFromScopedDerivation(t *testing.T) {
	api := fleetActivityFixture()
	app := newActivityLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	// The fleet-wide derivation handleActivityLiveSSE uses.
	ctx := context.Background()
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	fleetWide, _ := app.resolveFleetWideActivitySet(ctx, servers)

	// The server-scoped derivation /sessions and handleDeploymentsLiveSSE
	// use, scoped to a single server via server_id -- NFR10's contrast
	// case: Activity must not accidentally inherit this.
	scopedReq := httptest.NewRequest(http.MethodGet, "/sessions?server_id=10", nil)
	_, scopedConfigs, err := app.resolveScopedServerGameConfigs(ctx, scopedReq, servers)
	if err != nil {
		t.Fatalf("resolveScopedServerGameConfigs: %v", err)
	}

	if len(fleetWide) <= len(scopedConfigs) {
		t.Fatalf("expected the fleet-wide set (%d SGCs) to be strictly larger than the server_id=10-scoped set (%d SGCs)", len(fleetWide), len(scopedConfigs))
	}
	if _, ok := fleetWide[66]; !ok {
		t.Errorf("expected the fleet-wide set to include sgc 66 (server 20), got %v", fleetWide)
	}
	for _, sgc := range scopedConfigs {
		if sgc.ServerGameConfigId == 66 {
			t.Errorf("resolveScopedServerGameConfigs(server_id=10) unexpectedly included sgc 66 (server 20) -- fixture or scoping assumption broke")
		}
	}
}

// --- 3. Authorization parity: the subscription's authorized set equals
// #2271's page-query authorized set (same resolveFleetWideActivitySet
// call), so a deployment excluded from the page can never appear on the
// stream, and vice versa ---

func TestHandleActivityLiveSSE_AuthorizationParityWithPageQuery(t *testing.T) {
	api := fleetActivityFixture()
	app := newActivityLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	pageReq := httptest.NewRequest(http.MethodGet, "/activity", nil)
	pageData := app.buildActivityPageData(context.Background(), pageReq)

	code, body := runActivityLiveSSE(t, app, 100*time.Millisecond)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	var streamTopics []string
	for _, sgcID := range []int64{55, 66} {
		topic := events.TopicForDeployment(sgcID)
		if strings.Contains(body, "event: "+topic+"\n") {
			streamTopics = append(streamTopics, topic)
		}
	}
	sort.Strings(streamTopics)

	pageTopics := append([]string(nil), pageData.LiveTopics...)
	sort.Strings(pageTopics)

	if len(streamTopics) == 0 {
		t.Fatalf("expected the stream to have subscribed to at least one topic, got none in body %q", body)
	}
	if strings.Join(streamTopics, ",") != strings.Join(pageTopics, ",") {
		t.Errorf("stream's authorized topic set %v diverged from the page query's LiveTopics %v -- a deployment excluded from the page must be excluded from the stream, and vice versa", streamTopics, pageTopics)
	}
}

// TestHandleActivityLiveSSE_ExcludedDeploymentNeverAppearsOnStream is the
// negative case of authorization parity: a session whose SGC is outside the
// fleet-wide authorized set (deleted mid-request, held back by a
// ListServerGameConfigs failure, etc.) must never be subscribed to, mirroring
// #2271's own NFR10 defence-in-depth ("a session whose SGC fell outside the
// authorized set never reaches the page").
func TestHandleActivityLiveSSE_ExcludedDeploymentNeverAppearsOnStream(t *testing.T) {
	api := fleetActivityFixture()
	// A third server whose ListServerGameConfigs call fails -- its sgc (77)
	// must never show up in the fleet-wide authorized set or the stream.
	api.servers = append(api.servers, &manmanpb.Server{ServerId: 30, Name: "Gamma"})
	// sgcsByServerID intentionally has no entry for server 30, so
	// fakeActivityAPIClient.ListServerGameConfigs returns an empty (not
	// erroring) response for it -- sgc 77 is simply absent everywhere.

	app := newActivityLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	_, body := runActivityLiveSSE(t, app, 100*time.Millisecond)

	excludedTopic := events.TopicForDeployment(77)
	if strings.Contains(body, excludedTopic) {
		t.Errorf("excluded deployment's topic %s must never appear anywhere in the stream, got body %q", excludedTopic, body)
	}
}

// --- 4. No polling fallback: FR15 headline constraint that there is no
// polling mechanism to degrade to, and M5 does not add one for Activity ---

func TestActivity_NoPollingFallbackAddedForLiveRows(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		// A transient status: on /sessions, deploymentRowPollAttrs
		// (pages/sessions.templ) would render hx-trigger="every 3s" for
		// exactly this case (#1628's poll fallback). Activity must not.
		{SessionId: 1, ServerGameConfigId: 55, Status: "starting", StartedAt: time.Now().Unix()},
	}

	app := &App{grpc: &ControlClient{api: api}}
	req := httptest.NewRequest(http.MethodGet, "/activity", nil)
	w := httptest.NewRecorder()
	app.handleActivity(w, req)

	body := w.Body.String()
	if strings.Contains(body, "hx-trigger") {
		t.Errorf("Activity must not render any hx-trigger poll wiring (FR15: no polling fallback exists or is added), got body: %s", body)
	}
	if strings.Contains(body, "every 3s") || strings.Contains(body, "every ") {
		t.Errorf("Activity must not render a periodic poll interval, got body: %s", body)
	}
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Errorf("Activity must not render a meta-refresh poll fallback, got body: %s", body)
	}
}
