package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

func TestResolvePersona(t *testing.T) {
	cases := []struct {
		roles []string
		want  Persona
	}{
		{nil, PersonaNone},
		{[]string{"viewer"}, PersonaNone},
		{[]string{"gamer"}, PersonaGamer},
		{[]string{"gamer", "server-manager"}, PersonaServerManager},
		{[]string{"manmanv2-admin", "gamer"}, PersonaAdmin},
		{[]string{"server-manager", "manmanv2-admin"}, PersonaAdmin},
		{[]string{"admin"}, PersonaNone},
	}
	for _, c := range cases {
		if got := ResolvePersona(c.roles); got != c.want {
			t.Errorf("ResolvePersona(%v) = %v, want %v", c.roles, got, c.want)
		}
	}
}

func fixtureRegistry() *Registry {
	return NewRegistry(
		Tool{Name: "read", MinPersona: PersonaGamer, TargetArg: "id"},
		Tool{Name: "restart", MinPersona: PersonaServerManager, TargetArg: "id"},
		Tool{Name: "drain", MinPersona: PersonaAdmin},
	)
}

func TestRegistryAuthorizeAndVisible(t *testing.T) {
	reg := fixtureRegistry()
	cases := []struct {
		p       Persona
		allowed []string
	}{
		{PersonaNone, nil},
		{PersonaGamer, []string{"read"}},
		{PersonaServerManager, []string{"read", "restart"}},
		{PersonaAdmin, []string{"drain", "read", "restart"}},
	}
	for _, c := range cases {
		var got []string
		for _, tl := range reg.Visible(c.p) {
			got = append(got, tl.Name)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(c.allowed, ",") {
			t.Errorf("Visible(%v) = %v, want %v", c.p, got, c.allowed)
		}
		for _, name := range []string{"read", "restart", "drain"} {
			err := reg.Authorize(c.p, name)
			want := strings.Contains(strings.Join(c.allowed, ","), name)
			if (err == nil) != want {
				t.Errorf("Authorize(%v,%s) = %v, want allowed=%v", c.p, name, err, want)
			}
		}
	}
	if reg.Authorize(PersonaAdmin, "nope") != ErrUnknownTool {
		t.Error("unknown tool must be refused")
	}
}

type fakeVerifier map[string]*grpcauth.Claims

func (f fakeVerifier) Verify(_ context.Context, token string) (*grpcauth.Claims, error) {
	if c, ok := f[token]; ok {
		return c, nil
	}
	return nil, context.Canceled
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// harness serves the full HTTPAuth+Middleware stack with fixture tools; each
// tool counts backend invocations and performs a real gRPC call.
type harness struct {
	url      string
	backend  *int
	tokens   *[]string
	auditBuf *bytes.Buffer
}

func newHarness(t *testing.T, v grpcauth.TokenVerifier) *harness {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	var tokens []string

	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		mu.Lock()
		calls++
		tokens = append(tokens, strings.Join(md.Get("authorization"), ""))
		mu.Unlock()
		return h(ctx, req)
	}))
	grpc_health_v1.RegisterHealthServer(gs, health.NewServer())
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		insecureCreds(),
		grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	hc := grpc_health_v1.NewHealthClient(conn)

	reg := fixtureRegistry()
	buf := &bytes.Buffer{}
	audit := LogAuditor{Logger: slog.New(slog.NewTextHandler(buf, nil))}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "v0"}, nil)
	srv.AddReceivingMiddleware(Middleware(reg, audit))
	type in struct {
		ID string `json:"id"`
	}
	for _, name := range []string{"read", "restart", "drain"} {
		mcp.AddTool(srv, &mcp.Tool{Name: name}, func(ctx context.Context, _ *mcp.CallToolRequest, _ in) (*mcp.CallToolResult, any, error) {
			if _, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
				return nil, nil, err
			}
			return nil, map[string]string{"ok": "1"}, nil
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(HTTPAuth(v, "")(h))
	t.Cleanup(ts.Close)

	return &harness{url: ts.URL, backend: &calls, tokens: &tokens, auditBuf: buf}
}

func (h *harness) connect(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	return c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   h.url,
		HTTPClient: &http.Client{Transport: bearerRT{token}},
	}, nil)
}

func claims(sub string, roles ...string) *grpcauth.Claims {
	return &grpcauth.Claims{Subject: sub, Roles: roles}
}

func TestUnauthenticatedRejectedBeforeAnyCall(t *testing.T) {
	h := newHarness(t, fakeVerifier{"good": claims("u1", "manmanv2-admin")})
	for _, tok := range []string{"", "bogus"} {
		if s, err := h.connect(t, tok); err == nil {
			s.Close()
			t.Fatalf("token %q: connect succeeded", tok)
		}
	}
	if *h.backend != 0 {
		t.Fatalf("backend called %d times", *h.backend)
	}
}

func TestPersonaGatingListAndCall(t *testing.T) {
	h := newHarness(t, fakeVerifier{
		"none":  claims("u0", "viewer"),
		"gamer": claims("u1", "gamer"),
		"mgr":   claims("u2", "server-manager"),
		"adm":   claims("u3", "manmanv2-admin"),
	})
	ctx := context.Background()
	cases := []struct {
		token   string
		list    string
		denied  []string
		allowed []string
	}{
		{"none", "", []string{"read", "restart", "drain"}, nil},
		{"gamer", "read", []string{"restart", "drain"}, []string{"read"}},
		{"mgr", "read,restart", []string{"drain"}, []string{"read", "restart"}},
		{"adm", "drain,read,restart", nil, []string{"read", "restart", "drain"}},
	}
	for _, c := range cases {
		s, err := h.connect(t, c.token)
		if err != nil {
			t.Fatal(err)
		}
		lr, err := s.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tl := range lr.Tools {
			names = append(names, tl.Name)
		}
		sort.Strings(names)
		if strings.Join(names, ",") != c.list {
			t.Errorf("%s: tools/list = %v, want %q", c.token, names, c.list)
		}
		for _, name := range c.denied {
			before := *h.backend
			if _, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"id": "x"}}); err == nil {
				t.Errorf("%s: %s should be refused", c.token, name)
			}
			if *h.backend != before {
				t.Errorf("%s: refused %s made a backend call", c.token, name)
			}
		}
		for _, name := range c.allowed {
			res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"id": "x"}})
			if err != nil || res.IsError {
				t.Errorf("%s: %s failed: %v", c.token, name, err)
			}
		}
		s.Close()
	}
}

func TestTokenPassThroughAndAudit(t *testing.T) {
	h := newHarness(t, fakeVerifier{"tok-1": claims("u1", "server-manager"), "tok-2": claims("u2", "gamer")})
	ctx := context.Background()
	for _, tok := range []string{"tok-1", "tok-2"} {
		s, err := h.connect(t, tok)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{"id": "dep-7"}}); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	want := []string{"Bearer tok-1", "Bearer tok-2"}
	if strings.Join(*h.tokens, "|") != strings.Join(want, "|") {
		t.Fatalf("backend saw %v, want %v", *h.tokens, want)
	}

	s, _ := h.connect(t, "tok-2")
	defer s.Close()
	_, _ = s.CallTool(ctx, &mcp.CallToolParams{Name: "restart", Arguments: map[string]any{"id": "dep-9"}})
	log := h.auditBuf.String()
	for _, frag := range []string{
		"subject=u1 persona=server-manager tool=read target_id=dep-7 outcome=allowed",
		"subject=u2 persona=gamer tool=restart target_id=dep-9 outcome=refused",
	} {
		if !strings.Contains(log, frag) {
			t.Errorf("audit log missing %q in:\n%s", frag, log)
		}
	}
	if strings.Contains(log, "level=ERROR") {
		t.Errorf("refusal/allowed must not log ERROR:\n%s", log)
	}
}

func TestAuditorErrorLevelAndSnapshot(t *testing.T) {
	buf := &bytes.Buffer{}
	a := LogAuditor{Logger: slog.New(slog.NewTextHandler(buf, nil))}
	a.Record(context.Background(), AuditRecord{Subject: "u", Persona: PersonaAdmin, Tool: "t", Outcome: OutcomeError, Snapshot: "before"})
	if !strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "snapshot=before") {
		t.Errorf("got %s", buf.String())
	}
}

func insecureCreds() grpc.DialOption { return grpc.WithTransportCredentials(insecure.NewCredentials()) }
