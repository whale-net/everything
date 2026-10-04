package grantflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

type fakeTokens struct{ err error }

func (f fakeTokens) TokenSource(subject, grant string) grpcauth.GrantTokenSource { return f }
func (f fakeTokens) Token(context.Context) (*oauth2.Token, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &oauth2.Token{AccessToken: "kc-access"}, nil
}

type fakeVerifier struct{ sub string }

func (f fakeVerifier) Verify(context.Context, string) (*grpcauth.Claims, error) {
	return &grpcauth.Claims{Subject: f.sub, Roles: []string{"admin"}}, nil
}

func TestExchangeReturnsTokenAndClaims(t *testing.T) {
	ex := Exchanger{Source: fakeTokens{}, Grant: DefaultGrant, Verifier: fakeVerifier{sub: "u1"}}
	tok, claims, err := ex.Exchange(context.Background(), "u1")
	if err != nil || tok != "kc-access" || claims.Roles[0] != "admin" {
		t.Fatalf("got %q %+v %v", tok, claims, err)
	}
}

func TestExchangeRejectsMissingGrantAndSubjectMismatch(t *testing.T) {
	ex := Exchanger{Source: fakeTokens{err: grpcauth.ErrGrantNeedsReauth}, Grant: DefaultGrant, Verifier: fakeVerifier{sub: "u1"}}
	if _, _, err := ex.Exchange(context.Background(), "u1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("missing grant: %v", err)
	}
	ex = Exchanger{Source: fakeTokens{}, Grant: DefaultGrant, Verifier: fakeVerifier{sub: "someone-else"}}
	if _, _, err := ex.Exchange(context.Background(), "u1"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("subject mismatch: %v", err)
	}
}

func TestGateAuthorizePassesThroughWhenNotApplicable(t *testing.T) {
	hit := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit++ })
	c := &Consent{Subject: func(*http.Request) (string, bool) { return "", false }}
	h := c.GateAuthorize(next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/token", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/authorize?x=1", nil)) // not signed in
	if hit != 2 {
		t.Fatalf("next hit %d times, want 2", hit)
	}
}

func TestGateAuthorizeSkipsConsentForActiveGrant(t *testing.T) {
	store := grpcauth.NewFakeStore()
	if err := store.Persist(context.Background(), "u1", DefaultGrant, grpcauth.TokenMaterial{RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	hit := 0
	c := &Consent{Components: Components{Store: store}, Grant: DefaultGrant, Subject: func(*http.Request) (string, bool) { return "u1", true }}
	c.GateAuthorize(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit++ })).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/authorize?x=1", nil))
	if hit != 1 {
		t.Fatal("active grant should fall through to /authorize")
	}
}

func TestBeginReportsFalseWhenNoConsentNeeded(t *testing.T) {
	store := grpcauth.NewFakeStore()
	if err := store.Persist(context.Background(), "u1", DefaultGrant, grpcauth.TokenMaterial{RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	active := &Consent{Components: Components{Store: store}, Grant: DefaultGrant, Subject: func(*http.Request) (string, bool) { return "u1", true }}
	w := httptest.NewRecorder()
	if started, err := active.Begin(w, httptest.NewRequest("GET", "/x", nil), "/x"); started || err != nil || w.Code != http.StatusOK {
		t.Fatalf("active grant: started=%v err=%v code=%d", started, err, w.Code)
	}
	anon := &Consent{Subject: func(*http.Request) (string, bool) { return "", false }}
	if started, err := anon.Begin(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil), "/x"); started || err != nil {
		t.Fatalf("anonymous: started=%v err=%v", started, err)
	}
}

func TestCanResume(t *testing.T) {
	c := &Consent{ResumePrefixes: []string{"/link/whagent/complete"}}
	for path, want := range map[string]bool{
		"/authorize?x=1":                  true,
		"/link/whagent/complete?return=u": true,
		"/other":                          false,
		"//evil.example/authorize":        false,
		"https://evil.example/authorize":  false,
	} {
		if got := c.canResume(path); got != want {
			t.Errorf("canResume(%q) = %v, want %v", path, got, want)
		}
	}
}
