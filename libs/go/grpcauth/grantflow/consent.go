package grantflow

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/sessions"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

const (
	pendingCookie    = "grantflow_consent"
	pendingMaxAgeSec = 10 * 60
)

// Consent runs the one-time Keycloak consent that stores a user's offline
// grant, inserted in front of the MCP provider's GET /authorize so an MCP
// client can only finish sign-in once the MCP is able to act as that user.
type Consent struct {
	Components
	// Grant is the fixed grant key stored per user.
	Grant string
	// Subject returns the raw Keycloak `sub` of the signed-in UI user.
	Subject func(*http.Request) (string, bool)
	// Secret signs the cookie that carries state across the Keycloak round trip.
	Secret string
	// CallbackPath is the path of Config.RedirectURI on the hosting app.
	CallbackPath string
	// SignInURL receives users whose UI session is missing at the callback.
	SignInURL string
	Logger    *slog.Logger

	store *sessions.CookieStore
}

type pending struct {
	grpcauth.PendingAuthorization
	ReturnTo string
}

func (c *Consent) cookies() *sessions.CookieStore {
	if c.store == nil {
		key := sha256.Sum256([]byte(c.Secret))
		c.store = sessions.NewCookieStore(key[:])
		c.store.Options = &sessions.Options{Path: "/", MaxAge: pendingMaxAgeSec, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	}
	return c.store
}

// Mount registers the Keycloak callback and wraps mux so GET /authorize is
// gated; use the returned handler as the server's root handler.
func (c *Consent) Mount(mux *http.ServeMux) http.Handler {
	mux.HandleFunc("GET "+c.CallbackPath, c.HandleCallback)
	return c.GateAuthorize(mux)
}

// GateAuthorize sends a signed-in user without an active grant to Keycloak
// consent before the wrapped /authorize runs; every other request, and
// unauthenticated /authorize calls (the provider sends those to sign-in),
// passes through.
func (c *Consent) GateAuthorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/authorize" {
			next.ServeHTTP(w, r)
			return
		}
		sub, ok := c.Subject(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if st, err := c.Store.Status(r.Context(), sub, c.Grant); err == nil && st == grpcauth.GrantStatusActive {
			next.ServeHTTP(w, r)
			return
		}
		authURL, p, err := c.Source.BeginAuthorization(r.Context(), sub, c.Grant)
		if err != nil {
			c.log().Error("begin grant authorization failed", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if err := c.save(w, r, pending{PendingAuthorization: p, ReturnTo: r.URL.RequestURI()}); err != nil {
			c.log().Error("save pending grant authorization failed", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, authURL, http.StatusFound)
	})
}

// HandleCallback completes consent and resumes the interrupted /authorize.
func (c *Consent) HandleCallback(w http.ResponseWriter, r *http.Request) {
	p, err := c.load(r)
	c.clear(w, r)
	if err != nil {
		http.Error(w, "This consent link has expired or was already used. Please start over.", http.StatusBadRequest)
		return
	}
	sub, ok := c.Subject(r)
	if !ok {
		http.Redirect(w, r, c.SignInURL, http.StatusFound)
		return
	}
	if sub != p.Subject {
		http.Error(w, "This consent link does not belong to your signed-in session.", http.StatusForbidden)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "Consent was not completed: "+url.QueryEscape(e), http.StatusForbidden)
		return
	}
	if err := c.Source.CompleteAuthorization(r.Context(), p.PendingAuthorization, r.URL.Query().Get("state"), r.URL.Query().Get("code")); err != nil {
		c.log().Info("grant consent could not be completed", "error", err)
		http.Error(w, "Consent could not be completed. Please try again.", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(p.ReturnTo, "/authorize") {
		http.Error(w, "Consent complete. Return to your MCP client and retry.", http.StatusOK)
		return
	}
	http.Redirect(w, r, p.ReturnTo, http.StatusFound)
}

func (c *Consent) log() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c *Consent) save(w http.ResponseWriter, r *http.Request, p pending) error {
	s, _ := c.cookies().New(r, pendingCookie)
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	s.Values["p"] = string(b)
	return s.Save(r, w)
}

func (c *Consent) load(r *http.Request) (pending, error) {
	s, err := c.cookies().Get(r, pendingCookie)
	if err != nil {
		return pending{}, err
	}
	raw, _ := s.Values["p"].(string)
	var p pending
	if raw == "" || json.Unmarshal([]byte(raw), &p) != nil {
		return pending{}, fmt.Errorf("no pending consent")
	}
	return p, nil
}

func (c *Consent) clear(w http.ResponseWriter, r *http.Request) {
	if s, err := c.cookies().Get(r, pendingCookie); err == nil {
		s.Options.MaxAge = -1
		_ = s.Save(r, w)
	}
}
