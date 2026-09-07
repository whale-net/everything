package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/events"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	"google.golang.org/grpc"
)

// This file guards #1724: handleDeploymentsLiveSSE's topic scoping (FR7),
// its fragment's exact-match with buildDeploymentRowData/DeploymentRowInner
// (FR6, no second derivation), FR8's transient-error/stream-survival
// contract, NFR11 byte-stability on heartbeat, NFR6 auth rejection, and
// route dispatch onto a dedicated "/api/live/" prefix.
//
// No broker is needed: fakeLiveTransport is a hand-rolled htmxsse.Transport
// double (htmxsse's own fakeTransport/mockClock aren't exported for reuse
// outside package htmxsse), and fakeLiveClock is an htmxsse.Clock double
// that lets TestHandleDeploymentsLiveSSE_HeartbeatUnchangedStateIsKeepalive
// drive the heartbeat deterministically instead of sleeping past a real
// interval.

// fakeLiveAPIClient embeds the nil manmanpb.ManManAPIClient interface and
// overrides only the RPCs handleDeploymentsLiveSSE's call graph reaches
// (ListServers, ListServerGameConfigs, GetServerGameConfig, GetGameConfig,
// ListSessions, ListPendingRestarts); any other call panics on the nil
// embedded interface, deliberately, per
// handlers_sessions_deployment_row_test.go's fakeSessionsAPIClient
// precedent. GetGameConfig always errors (display name resolution isn't
// what these tests guard -- DisplayName falls back to "SGC %d" and that
// fallback is exercised identically everywhere). ListPendingRestarts
// (#1735) always returns an empty response -- these tests guard the SSE
// fragment/exact-match pipeline, not restart-state badges.
type fakeLiveAPIClient struct {
	manmanpb.ManManAPIClient

	mu sync.Mutex

	servers             []*manmanpb.Server
	configsByServer     map[int64][]*manmanpb.ServerGameConfig
	configsByID         map[int64]*manmanpb.ServerGameConfig
	liveSessionByConfig map[int64]*manmanpb.Session
	// sgcErrors, when set for an id, makes GetServerGameConfig fail for
	// that id -- used to drive buildDeploymentRowData's error path
	// (FR8) after a stream is already open.
	sgcErrors map[int64]error
}

func (f *fakeLiveAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeLiveAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &manmanpb.ListServerGameConfigsResponse{Configs: f.configsByServer[in.ServerId]}, nil
}

func (f *fakeLiveAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.sgcErrors[in.ServerGameConfigId]; ok {
		return nil, err
	}
	cfg, ok := f.configsByID[in.ServerGameConfigId]
	if !ok {
		return nil, fmt.Errorf("sgc %d not found", in.ServerGameConfigId)
	}
	return &manmanpb.GetServerGameConfigResponse{Config: cfg}, nil
}

func (f *fakeLiveAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return nil, fmt.Errorf("no game config configured for id %d", in.ConfigId)
}

func (f *fakeLiveAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess := f.liveSessionByConfig[in.ServerGameConfigId]
	if sess == nil {
		return &manmanpb.ListSessionsResponse{}, nil
	}
	// The same fixture session backs both buildDeploymentRowData's
	// unfiltered "all sessions" call and getLiveSession's LiveOnly call --
	// they're never expected to disagree in these tests.
	return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{sess}}, nil
}

func (f *fakeLiveAPIClient) ListPendingRestarts(ctx context.Context, in *manmanpb.ListPendingRestartsRequest, opts ...grpc.CallOption) (*manmanpb.ListPendingRestartsResponse, error) {
	return &manmanpb.ListPendingRestartsResponse{}, nil
}

// liveTestAuthenticator is a shared AuthModeNone authenticator (auto-
// authenticates every request, mirroring local dev) for tests that don't
// exercise auth rejection themselves -- deploymentRowFragment.Render calls
// app.auth.GetAccessToken per delivery (FR3), so app.auth must never be nil
// here even when a test's own request never goes through RequireAuthFunc.
var liveTestAuthenticator = func() *htmxauth.Authenticator {
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "live-sse-test-secret-at-least-32-bytes-long",
		SessionName:   "manmanv2_ui_live_sse_fixture_session",
	})
	if err != nil {
		panic(fmt.Sprintf("liveTestAuthenticator: %v", err))
	}
	return auth
}()

func newLiveTestApp(api *fakeLiveAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}, auth: liveTestAuthenticator}
}

// fakeLiveTransport is a hand-rolled htmxsse.Transport double: it captures
// the handler htmxsse.Hub registers and lets a test invoke it directly to
// simulate a broker delivery, without a real RabbitMQ connection.
type fakeLiveTransport struct {
	mu      sync.Mutex
	handler rmq.MessageHandler
}

func (f *fakeLiveTransport) BindExchange(exchange string, routingKeys []string) error {
	return nil
}

func (f *fakeLiveTransport) RegisterHandler(queue string, handler rmq.MessageHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

// Start returns nil immediately, mirroring *rmq.Consumer's real contract
// (see htmxsse.Hub.attachWithRetry's doc comment) -- consumption is
// considered live until the Hub's context is cancelled.
func (f *fakeLiveTransport) Start(ctx context.Context) error {
	return nil
}

func (f *fakeLiveTransport) Close() error {
	return nil
}

// deliver simulates a broker delivery by invoking the registered handler
// directly.
func (f *fakeLiveTransport) deliver(routingKey string, body []byte) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	if h == nil {
		return
	}
	_ = h(context.Background(), rmq.Message{RoutingKey: routingKey, Body: body})
}

// waitForHandlerRegistered polls until the Hub has attached to transport
// and registered its message handler (htmxsse.Hub.Subscribe triggers
// attachment asynchronously), failing the test if it never does.
func waitForHandlerRegistered(t *testing.T, transport *fakeLiveTransport, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		transport.mu.Lock()
		ready := transport.handler != nil
		transport.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("transport handler was never registered (hub failed to attach)")
}

func newTestHubWithTransport(transport *fakeLiveTransport) *htmxsse.Hub {
	config := htmxsse.DefaultConfig()
	config.ExchangeName = events.ExchangeName
	config.SubscriberBufferDepth = 10
	// Large so real-time heartbeat noise doesn't interleave with these
	// tests' assertions; TestHandleDeploymentsLiveSSE_HeartbeatUnchangedStateIsKeepalive
	// builds its own Hub with a fake clock instead of relying on this one.
	config.HeartbeatInterval = time.Hour
	return htmxsse.NewHub(func(ctx context.Context) (htmxsse.Transport, error) {
		return transport, nil
	}, config)
}

func newTestHub() *htmxsse.Hub {
	return newTestHubWithTransport(&fakeLiveTransport{})
}

// runLiveSSE runs handleDeploymentsLiveSSE for wait, then cancels the
// request context and returns everything written to the response.
func runLiveSSE(t *testing.T, app *App, target string, wait time.Duration) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		app.handleDeploymentsLiveSSE(w, req)
		close(done)
	}()

	time.Sleep(wait)
	cancel()
	<-done

	return w.Body.String()
}

// extractSSEData returns the payload of the first "event: <topic>\n...data:
// <payload>\n\n" swap block for topic, failing the test if none is found.
func extractSSEData(t *testing.T, body, topic string) string {
	t.Helper()
	marker := "event: " + topic + "\n"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("no %q event found in body %q", marker, body)
	}
	rest := body[idx+len(marker):]
	const dataMarker = "\ndata: "
	dataIdx := strings.Index(rest, dataMarker)
	if dataIdx < 0 {
		t.Fatalf("no data line found after %q in %q", marker, rest)
	}
	afterData := rest[dataIdx+len(dataMarker):]
	end := strings.Index(afterData, "\n\n")
	if end < 0 {
		t.Fatalf("no terminating blank line after data for %q in %q", marker, afterData)
	}
	return afterData[:end]
}

// --- 1. Topic set scoped to the selected server's authorized SGCs (FR7) ---

func TestHandleDeploymentsLiveSSE_TopicSetScopedToSelectedServer(t *testing.T) {
	sgc1 := &manmanpb.ServerGameConfig{ServerGameConfigId: 101, ServerId: 1, Status: "active"}
	sgc2 := &manmanpb.ServerGameConfig{ServerGameConfigId: 102, ServerId: 1, Status: "active"}
	sgcOther := &manmanpb.ServerGameConfig{ServerGameConfigId: 201, ServerId: 2, Status: "active"}

	api := &fakeLiveAPIClient{
		servers: []*manmanpb.Server{
			{ServerId: 1, Name: "Alpha", IsDefault: true},
			{ServerId: 2, Name: "Beta"},
		},
		configsByServer: map[int64][]*manmanpb.ServerGameConfig{
			1: {sgc1, sgc2},
			2: {sgcOther},
		},
		configsByID: map[int64]*manmanpb.ServerGameConfig{101: sgc1, 102: sgc2, 201: sgcOther},
	}
	app := newLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	body := runLiveSSE(t, app, "/api/live/deployments?server_id=1", 100*time.Millisecond)

	for _, sgcID := range []int64{101, 102} {
		topic := events.TopicForDeployment(sgcID)
		if !strings.Contains(body, "event: "+topic+"\n") {
			t.Errorf("expected an initial swap for in-scope topic %s, got body %q", topic, body)
		}
	}
	outOfScope := events.TopicForDeployment(201)
	if strings.Contains(body, outOfScope) {
		t.Errorf("out-of-scope SGC 201's topic %s must not appear anywhere in the stream: %q", outOfScope, body)
	}
}

// --- 2. Initial fragment bytes equal a direct DeploymentRowInner(buildDeploymentRowData(...)) render ---

func TestHandleDeploymentsLiveSSE_InitialFragmentMatchesDirectRender(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 55, ServerId: 1, Status: "active"}
	sess := &manmanpb.Session{SessionId: 9, ServerGameConfigId: 55, StartedAt: 100, Status: "running"}
	api := &fakeLiveAPIClient{
		servers:             []*manmanpb.Server{{ServerId: 1, IsDefault: true}},
		configsByServer:     map[int64][]*manmanpb.ServerGameConfig{1: {sgc}},
		configsByID:         map[int64]*manmanpb.ServerGameConfig{55: sgc},
		liveSessionByConfig: map[int64]*manmanpb.Session{55: sess},
	}
	app := newLiveTestApp(api)
	app.sseHub = newTestHub()
	defer app.sseHub.Close()

	body := runLiveSSE(t, app, "/api/live/deployments?server_id=1", 100*time.Millisecond)

	topic := events.TopicForDeployment(55)
	if got := strings.Count(body, "event: "+topic+"\n"); got != 1 {
		t.Fatalf("expected exactly one initial swap for topic %s, got %d in body %q", topic, got, body)
	}
	got := extractSSEData(t, body, topic)

	want, err := app.buildDeploymentRowData(context.Background(), 55)
	if err != nil {
		t.Fatalf("buildDeploymentRowData: %v", err)
	}
	var buf bytes.Buffer
	if err := pages.DeploymentRowInner(want).Render(context.Background(), &buf); err != nil {
		t.Fatalf("DeploymentRowInner.Render: %v", err)
	}
	wantBytes := strings.TrimSpace(buf.String())

	if got != wantBytes {
		t.Errorf("fragment bytes diverged from a direct DeploymentRowInner(buildDeploymentRowData(...)) render (proves a second derivation exists):\n got:  %q\nwant: %q", got, wantBytes)
	}
}

// --- 3. A published event swaps only its own topic ---

func TestHandleDeploymentsLiveSSE_PublishedEventSwapsOnlyThatTopic(t *testing.T) {
	sgcA := &manmanpb.ServerGameConfig{ServerGameConfigId: 11, ServerId: 1, Status: "active"}
	sgcB := &manmanpb.ServerGameConfig{ServerGameConfigId: 12, ServerId: 1, Status: "active"}
	api := &fakeLiveAPIClient{
		servers:         []*manmanpb.Server{{ServerId: 1, IsDefault: true}},
		configsByServer: map[int64][]*manmanpb.ServerGameConfig{1: {sgcA, sgcB}},
		configsByID:     map[int64]*manmanpb.ServerGameConfig{11: sgcA, 12: sgcB},
	}
	app := newLiveTestApp(api)
	transport := &fakeLiveTransport{}
	app.sseHub = newTestHubWithTransport(transport)
	defer app.sseHub.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/live/deployments?server_id=1", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		app.handleDeploymentsLiveSSE(w, req)
		close(done)
	}()

	waitForHandlerRegistered(t, transport, time.Second)
	time.Sleep(30 * time.Millisecond) // let the connect-time fragments flush

	topicA := events.TopicForDeployment(11)
	topicB := events.TopicForDeployment(12)

	transport.deliver(topicA, nil)
	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	body := w.Body.String()
	if got := strings.Count(body, "event: "+topicA+"\n"); got != 2 { // connect + the delivered event
		t.Errorf("expected 2 swaps for topic %s (connect + delivered event), got %d in body %q", topicA, got, body)
	}
	if got := strings.Count(body, "event: "+topicB+"\n"); got != 1 { // connect only
		t.Errorf("expected exactly 1 swap for topic %s (connect only, no event delivered), got %d in body %q", topicB, got, body)
	}
}

// --- 4. NFR11: unchanged state on heartbeat is a keepalive, not a swap ---

// fakeLiveTicker/fakeLiveClock are a hand-rolled htmxsse.Ticker/htmxsse.Clock
// pair (htmxsse's own mockClock isn't exported for reuse outside package
// htmxsse) that let tick(d) fire every outstanding ticker created with
// interval d, so the heartbeat loop can be driven deterministically instead
// of racing a real timer.
type fakeLiveTicker struct {
	interval time.Duration
	ch       chan time.Time
}

func (t *fakeLiveTicker) C() <-chan time.Time { return t.ch }
func (t *fakeLiveTicker) Stop()               {}

type fakeLiveClock struct {
	mu      sync.Mutex
	tickers []*fakeLiveTicker
}

func (c *fakeLiveClock) Now() time.Time { return time.Now() }

func (c *fakeLiveClock) NewTicker(d time.Duration) htmxsse.Ticker {
	tk := &fakeLiveTicker{interval: d, ch: make(chan time.Time, 1)}
	c.mu.Lock()
	c.tickers = append(c.tickers, tk)
	c.mu.Unlock()
	return tk
}

func (c *fakeLiveClock) Sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (c *fakeLiveClock) tick(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tk := range c.tickers {
		if tk.interval == d {
			select {
			case tk.ch <- time.Now():
			default:
			}
		}
	}
}

func TestHandleDeploymentsLiveSSE_HeartbeatUnchangedStateIsKeepalive(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 77, ServerId: 1, Status: "active"}
	api := &fakeLiveAPIClient{
		servers:         []*manmanpb.Server{{ServerId: 1, IsDefault: true}},
		configsByServer: map[int64][]*manmanpb.ServerGameConfig{1: {sgc}},
		configsByID:     map[int64]*manmanpb.ServerGameConfig{77: sgc},
	}
	app := newLiveTestApp(api)

	clock := &fakeLiveClock{}
	transport := &fakeLiveTransport{}
	heartbeat := 20 * time.Millisecond
	hubConfig := htmxsse.DefaultConfig()
	hubConfig.ExchangeName = events.ExchangeName
	hubConfig.SubscriberBufferDepth = 10
	hubConfig.HeartbeatInterval = heartbeat
	app.sseHub = htmxsse.NewHubWithClock(func(ctx context.Context) (htmxsse.Transport, error) {
		return transport, nil
	}, hubConfig, clock)
	defer app.sseHub.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/live/deployments?server_id=1", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		app.handleDeploymentsLiveSSE(w, req)
		close(done)
	}()

	// Let the connect-time fragment settle before driving heartbeats.
	time.Sleep(20 * time.Millisecond)

	// The fake's underlying state never changes between calls, so each
	// heartbeat tick must produce a keepalive, never a second swap.
	clock.tick(heartbeat)
	time.Sleep(20 * time.Millisecond)
	clock.tick(heartbeat)
	time.Sleep(20 * time.Millisecond)

	cancel()
	<-done

	body := w.Body.String()
	topic := events.TopicForDeployment(77)
	swapCount := strings.Count(body, "event: "+topic+"\n")
	keepaliveCount := strings.Count(body, "event: "+topic+"-keepalive\n")

	if swapCount != 1 {
		t.Errorf("expected exactly 1 swap (the initial connect fragment) with unchanged state, got %d: %q", swapCount, body)
	}
	if keepaliveCount == 0 {
		t.Errorf("expected at least one heartbeat keepalive for unchanged state, got 0: %q", body)
	}
}

// --- 5. Malformed/unknown topic and a buildDeploymentRowData error each skip only that delivery ---

func TestDeploymentRowFragment_MalformedTopic_TransientNoBytesNoCancel(t *testing.T) {
	app := newLiveTestApp(&fakeLiveAPIClient{})
	canceled := false
	frag := deploymentRowFragment{
		r:          httptest.NewRequest(http.MethodGet, "/api/live/deployments", nil),
		topic:      "not-a-deployment-topic",
		cancel:     func() { canceled = true },
		app:        app,
		authorized: map[int64]struct{}{1: {}},
	}

	var buf bytes.Buffer
	if err := frag.Render(context.Background(), &buf); err == nil {
		t.Fatalf("expected an error for a malformed topic")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no bytes written for a malformed topic, got %q", buf.String())
	}
	if canceled {
		t.Errorf("a malformed topic is transient (FR8) and must not end the stream")
	}
}

func TestDeploymentRowFragment_UnauthorizedTopic_TransientNoBytesNoCancel(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 9, Status: "active"}
	app := newLiveTestApp(&fakeLiveAPIClient{configsByID: map[int64]*manmanpb.ServerGameConfig{9: sgc}})
	canceled := false
	frag := deploymentRowFragment{
		r:          httptest.NewRequest(http.MethodGet, "/api/live/deployments", nil),
		topic:      events.TopicForDeployment(9),
		cancel:     func() { canceled = true },
		app:        app,
		authorized: map[int64]struct{}{1: {}}, // 9 deliberately absent (FR7 defence-in-depth)
	}

	var buf bytes.Buffer
	if err := frag.Render(context.Background(), &buf); err == nil {
		t.Fatalf("expected an error for an unauthorized sgc")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no bytes written for an unauthorized sgc, got %q", buf.String())
	}
	if canceled {
		t.Errorf("an unauthorized topic is transient defence-in-depth (FR7/FR8), not a terminal auth failure -- must not end the stream")
	}
}

func TestDeploymentRowFragment_BuildRowDataError_TransientNoBytesNoCancel(t *testing.T) {
	app := newLiveTestApp(&fakeLiveAPIClient{sgcErrors: map[int64]error{7: fmt.Errorf("control-api down")}})
	canceled := false
	frag := deploymentRowFragment{
		r:          httptest.NewRequest(http.MethodGet, "/api/live/deployments", nil),
		topic:      events.TopicForDeployment(7),
		cancel:     func() { canceled = true },
		app:        app,
		authorized: map[int64]struct{}{7: {}},
	}

	var buf bytes.Buffer
	if err := frag.Render(context.Background(), &buf); err == nil {
		t.Fatalf("expected an error when buildDeploymentRowData fails")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no bytes written when buildDeploymentRowData fails, got %q", buf.String())
	}
	if canceled {
		t.Errorf("a single row's control-api failure (FR8) must not end the stream")
	}
}

// TestHandleDeploymentsLiveSSE_DeliveryErrorSkipsRowKeepsStreamOpen proves
// FR8's stream-survival contract end to end, through the real Hub/Handler
// pipeline: a delivery whose buildDeploymentRowData call fails produces no
// swap for that topic, and a subsequent good delivery on a different topic
// still swaps -- the earlier error never closed the stream.
func TestHandleDeploymentsLiveSSE_DeliveryErrorSkipsRowKeepsStreamOpen(t *testing.T) {
	sgcGood := &manmanpb.ServerGameConfig{ServerGameConfigId: 21, ServerId: 1, Status: "active"}
	sgcBad := &manmanpb.ServerGameConfig{ServerGameConfigId: 22, ServerId: 1, Status: "active"}
	api := &fakeLiveAPIClient{
		servers:         []*manmanpb.Server{{ServerId: 1, IsDefault: true}},
		configsByServer: map[int64][]*manmanpb.ServerGameConfig{1: {sgcGood, sgcBad}},
		configsByID:     map[int64]*manmanpb.ServerGameConfig{21: sgcGood, 22: sgcBad},
	}
	app := newLiveTestApp(api)
	transport := &fakeLiveTransport{}
	app.sseHub = newTestHubWithTransport(transport)
	defer app.sseHub.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/live/deployments?server_id=1", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		app.handleDeploymentsLiveSSE(w, req)
		close(done)
	}()

	waitForHandlerRegistered(t, transport, time.Second)
	time.Sleep(20 * time.Millisecond)

	// Break the bad SGC only after connect, so the connect-time fragment
	// still succeeded and only the delivered event exercises the error path.
	api.mu.Lock()
	api.sgcErrors = map[int64]error{22: fmt.Errorf("control-api down")}
	api.mu.Unlock()

	topicBad := events.TopicForDeployment(22)
	topicGood := events.TopicForDeployment(21)

	transport.deliver(topicBad, nil)
	time.Sleep(30 * time.Millisecond)
	transport.deliver(topicGood, nil)
	time.Sleep(30 * time.Millisecond)

	cancel()
	<-done

	body := w.Body.String()
	if got := strings.Count(body, "event: "+topicBad+"\n"); got != 1 { // connect only; the errored delivery added no bytes
		t.Errorf("expected the errored delivery to add no swap for %s, got %d in body %q", topicBad, got, body)
	}
	if got := strings.Count(body, "event: "+topicGood+"\n"); got != 2 { // connect + the good delivery
		t.Errorf("expected the good delivery to still swap for %s after the other topic's error, got %d in body %q", topicGood, got, body)
	}
}

// --- 6. NFR6 auth rejection, and app.sseHub == nil degradation ---

func TestHandleDeploymentsLiveSSE_Unauthenticated_RejectedByRequireAuthFunc(t *testing.T) {
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		SessionSecret: "dev-secret-at-least-32-bytes-long-xxxx",
		SessionName:   "manmanv2_ui_live_sse_test_session",
		// Mode deliberately left at zero value -- not none, not oidc -- so
		// RequireAuth takes the session-check branch and fails on the
		// missing cookie below, mirroring
		// tools/app_registry/ui/handlers_sse_test.go's precedent.
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	app := &App{auth: auth}
	handlerCalled := false
	wrapped := auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		app.handleDeploymentsLiveSSE(w, r)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/live/deployments", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	if handlerCalled {
		t.Errorf("handleDeploymentsLiveSSE must not run for an unauthenticated request")
	}
	if w.Code == http.StatusOK {
		t.Errorf("expected a rejection status for an unauthenticated request, got 200")
	}
	if loc := w.Header().Get("Location"); loc == "" || !strings.Contains(loc, "/auth/login") {
		t.Errorf("expected a redirect to /auth/login, got Location=%q", loc)
	}
}

func TestHandleDeploymentsLiveSSE_NilHub_Returns503NotPanicOrHang(t *testing.T) {
	app := &App{sseHub: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/live/deployments", nil)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		app.handleDeploymentsLiveSSE(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handleDeploymentsLiveSSE hung with app.sseHub == nil")
	}

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// --- 7. Route dispatch: GET /api/live/deployments reaches this handler and no other ---

func TestRouteDispatch_LiveDeploymentsDistinctFromOtherAPIPrefixes(t *testing.T) {
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "dev-secret-at-least-32-bytes-long-xxxx",
		SessionName:   "manmanv2_ui_live_sse_route_test_session",
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	app := &App{auth: auth, grpc: &ControlClient{api: &fakeLiveAPIClient{}}}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/live/deployments", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// app.sseHub is nil here, so a request that actually reached
	// handleDeploymentsLiveSSE -- as opposed to handleDeploymentRowFragment
	// ("/api/deployments/") or handleSessionStdin ("/api/sessions/"), both
	// registered as catch-all prefixes elsewhere on the mux -- gets exactly
	// this handler's own 503, not a 404 or either other handler's response
	// shape.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (expected handleDeploymentsLiveSSE's own 503)", w.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(w.Body.String(), "live updates unavailable") {
		t.Errorf("expected handleDeploymentsLiveSSE's own 503 body, got %q", w.Body.String())
	}
}
