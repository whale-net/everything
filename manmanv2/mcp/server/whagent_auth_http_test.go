package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantflow"
	"github.com/whale-net/everything/libs/go/whagent"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

const (
	testWhagentIssuer = "https://whagent.example.test"
	testAudience      = "https://mcp.example.test"
	testUserIssuer    = "https://keycloak.example.test/realms/manman"
)

type whagentFixture struct {
	signer   *whagent.Signer
	verifier *whagent.Verifier
	priv     ed25519.PrivateKey
}

func newWhagentFixture(t *testing.T) whagentFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := whagent.New(priv, testWhagentIssuer, "k1")
	if err != nil {
		t.Fatal(err)
	}
	v, err := whagent.NewVerifierFromKey(pub, testWhagentIssuer)
	if err != nil {
		t.Fatal(err)
	}
	return whagentFixture{signer, v, priv}
}

func (f whagentFixture) mint(t *testing.T, sub, subIss, aud string) string {
	t.Helper()
	tok, err := f.signer.Mint(context.Background(), whagent.MintRequest{
		Subject: sub, SubjectIssuer: subIss, Audience: aud, SessionID: "s1",
		Actor: whagent.Actor{Subject: "agent-1", AgentID: "a1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// raw signs arbitrary claims with the whagent key (for expired / odd issuers).
func (f whagentFixture) raw(t *testing.T, claims map[string]any) string {
	t.Helper()
	js, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: f.priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.Signed(js).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f whagentFixture) claims(iss string, exp time.Time, aud string) map[string]any {
	return map[string]any{
		"iss": iss, "sub": "human-1", "aud": []string{aud}, "exp": exp.Unix(), "iat": time.Now().Unix(),
		"jti": "j", "sub_iss": testUserIssuer, "act": map[string]string{"sub": "agent-1", "agent_id": "a1"},
		"whagent_session_id": "s1",
	}
}

func okInner() (http.Handler, *bool) {
	called := false
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true }), &called
}

func doReq(h http.Handler, tok string) int {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestWhagentHTTPAuth(t *testing.T) {
	f := newWhagentFixture(t)
	other := newWhagentFixture(t)
	existing := OIDCCallerVerifier(fakeVerifier{"kc": claims("u", "gamer")})
	cfg := WhagentAuthConfig{Verifier: f.verifier, Audience: testAudience, UserIssuer: testUserIssuer, WhagentIssuer: testWhagentIssuer}
	future := time.Now().Add(5 * time.Minute)

	cases := []struct {
		name    string
		token   string
		want    int
		reaches bool
	}{
		{"valid whagent token", f.mint(t, "human-1", testUserIssuer, testAudience), 200, true},
		{"bad signature", other.mint(t, "human-1", testUserIssuer, testAudience), 401, false},
		{"keycloak-signed JWT claiming whagent iss", other.raw(t, f.claims(testWhagentIssuer, future, testAudience)), 401, false},
		{"expired", f.raw(t, f.claims(testWhagentIssuer, time.Now().Add(-time.Minute), testAudience)), 401, false},
		{"wrong audience", f.mint(t, "human-1", testUserIssuer, "https://other.test"), 401, false},
		{"absent audience", f.raw(t, func() map[string]any { c := f.claims(testWhagentIssuer, future, ""); delete(c, "aud"); return c }()), 401, false},
		{"wrong sub_iss", f.mint(t, "human-1", "https://evil.test/realms/x", testAudience), 401, false},
		{"opaque/direct OIDC token unchanged", "kc", 200, true},
		{"unknown direct token still rejected", "nope", 401, false},
		{"no token", "", 401, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inner, called := okInner()
			h := WhagentHTTPAuth(existing, cfg, "")(inner)
			if got := doReq(h, c.token); got != c.want {
				t.Fatalf("status = %d, want %d", got, c.want)
			}
			if *called != c.reaches {
				t.Fatalf("handler reached = %v, want %v", *called, c.reaches)
			}
		})
	}
}

func TestWhagentHTTPAuthDisabledIssuerRoutesToExisting(t *testing.T) {
	f := newWhagentFixture(t)
	existing := OIDCCallerVerifier(fakeVerifier{})
	cfg := WhagentAuthConfig{Verifier: f.verifier, Audience: testAudience, UserIssuer: testUserIssuer}
	inner, called := okInner()
	if got := doReq(WhagentHTTPAuth(existing, cfg, "")(inner), f.mint(t, "human-1", testUserIssuer, testAudience)); got != 401 || *called {
		t.Fatalf("status = %d, reached = %v", got, *called)
	}
}

type grantSrc map[string]string // subject -> access token

type tokSrc struct {
	tok string
	err bool
}

func (t tokSrc) Token(context.Context) (*oauth2.Token, error) {
	if t.err {
		return nil, errors.New("no grant")
	}
	return &oauth2.Token{AccessToken: t.tok}, nil
}

func (g grantSrc) TokenSource(subject, _ string) grpcauth.GrantTokenSource {
	tok, ok := g[subject]
	return tokSrc{tok: tok, err: !ok}
}

// fakeResolver maps "iss|sub" to a manmanv2 user sub.
type fakeResolver map[string]string

func (f fakeResolver) Resolve(_ context.Context, iss, sub string) (string, bool, error) {
	u, ok := f[iss+"|"+sub]
	return u, ok, nil
}

// testResolver links human-1 (and no-grant-user, who has no stored grant).
func testResolver() fakeResolver {
	return fakeResolver{
		testUserIssuer + "|human-1":       "human-1",
		testUserIssuer + "|no-grant-user": "no-grant-user",
		testUserIssuer + "|alias-whagent": "human-1",
	}
}

func testExchanger() grantflow.Exchanger {
	return grantflow.Exchanger{
		Source:   grantSrc{"human-1": "user-token"},
		Grant:    "g",
		Verifier: fakeVerifier{"user-token": {Issuer: testUserIssuer, Subject: "human-1", Roles: []string{"gamer"}}},
	}
}

type wgHarness struct {
	url string
	log *bytes.Buffer
	f   whagentFixture
}

func newWhagentHarness(t *testing.T) wgHarness {
	t.Helper()
	f := newWhagentFixture(t)
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	reg := NewRegistry(ConnectAddressTool)
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "v0"}, nil)
	srv.AddReceivingMiddleware(Middleware(reg, LogAuditor{Logger: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))}))
	AddConnectAddressTool(srv, fakeConnectAPI{
		configs: map[int64]*manmanpb.ServerGameConfig{1: {ServerId: 10}},
		hosts:   map[int64]*manmanpb.Server{10: {HostPublicAddress: "1.2.3.4"}},
	})
	srv.AddReceivingMiddleware(WhagentMiddleware(testExchanger(), testResolver()))
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	cfg := WhagentAuthConfig{Verifier: f.verifier, Audience: testAudience, UserIssuer: testUserIssuer, WhagentIssuer: testWhagentIssuer}
	direct := OIDCCallerVerifier(fakeVerifier{"direct": {Issuer: testUserIssuer, Subject: "human-1", Roles: []string{"gamer"}}})
	ts := httptest.NewServer(WhagentHTTPAuth(direct, cfg, "")(h))
	t.Cleanup(ts.Close)
	return wgHarness{ts.URL, buf, f}
}

func (h wgHarness) session(t *testing.T, tok string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: h.url, HTTPClient: &http.Client{Transport: bearerRT{tok}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func toolNames(t *testing.T, s *mcp.ClientSession) string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, tl := range res.Tools {
		n = append(n, tl.Name)
	}
	return strings.Join(n, ",")
}

func callText(t *testing.T, s *mcp.ClientSession) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_connect_address", Arguments: map[string]any{"deployment_id": 1}})
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return res, sb.String()
}

func TestWhagentPersonaEqualsDirectCall(t *testing.T) {
	h := newWhagentHarness(t)
	wg := h.session(t, h.f.mint(t, "human-1", testUserIssuer, testAudience))
	direct := h.session(t, "direct")

	if a, b := toolNames(t, wg), toolNames(t, direct); a != b || a == "" {
		t.Fatalf("tools via whagent %q != direct %q", a, b)
	}
	wr, wt := callText(t, wg)
	dr, dt := callText(t, direct)
	if wr.IsError || dr.IsError || wt != dt || wt == "" {
		t.Fatalf("results differ: whagent=%q (err=%v) direct=%q (err=%v)", wt, wr.IsError, dt, dr.IsError)
	}
}

func TestWhagentLinkedToDifferentUserActsAsLinkedUser(t *testing.T) {
	h := newWhagentHarness(t)
	s := h.session(t, h.f.mint(t, "alias-whagent", testUserIssuer, testAudience))
	if res, text := callText(t, s); res.IsError || text == "" {
		t.Fatalf("isError=%v text=%q", res.IsError, text)
	}
}

func TestWhagentUnlinkedUserGetsLinkInstructions(t *testing.T) {
	h := newWhagentHarness(t)
	s := h.session(t, h.f.mint(t, "never-linked", testUserIssuer, testAudience))
	res, text := callText(t, s)
	if !res.IsError || text != ErrWhagentNotLinked || !strings.HasPrefix(text, ErrWhagentUnresolved) || !strings.Contains(text, "/grants") {
		t.Fatalf("isError=%v text=%q", res.IsError, text)
	}
	if _, err := s.ListTools(context.Background(), nil); err == nil || !strings.Contains(err.Error(), ErrWhagentNotLinked) {
		t.Fatalf("tools/list err = %v", err)
	}
}

func TestWhagentLinkedWithoutGrantIsToolError(t *testing.T) {
	h := newWhagentHarness(t)
	s := h.session(t, h.f.mint(t, "no-grant-user", testUserIssuer, testAudience))
	res, text := callText(t, s)
	if !res.IsError || text != ErrWhagentNoAccess || !strings.HasPrefix(text, ErrWhagentUnresolved) {
		t.Fatalf("isError=%v text=%q", res.IsError, text)
	}
	if _, err := s.ListTools(context.Background(), nil); err == nil || !strings.Contains(err.Error(), ErrWhagentNoAccess) {
		t.Fatalf("tools/list err = %v", err)
	}
	if out := h.log.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "whagent identity could not be resolved") {
		t.Fatalf("expected WARN log, got %q", out)
	}
}

func TestWhagentMiddlewareFallsThroughWithoutClaim(t *testing.T) {
	called := false
	next := mcp.MethodHandler(func(context.Context, string, mcp.Request) (mcp.Result, error) { called = true; return nil, nil })
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{}
	if _, err := WhagentMiddleware(testExchanger(), testResolver())(next)(context.Background(), "tools/call", req); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
