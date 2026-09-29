package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/authdoor"
	"github.com/whale-net/everything/krill/caller"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/libs/go/auth"
)

type credStore struct{ auth.CredentialStore }

func (credStore) Verify(_ context.Context, raw string) (string, auth.Credential, error) {
	switch raw {
	case "reader":
		return "https://kc.example|r1", auth.Credential{Persona: "reader"}, nil
	case "operator":
		return "https://kc.example|o1", auth.Credential{Persona: "swarm_operator"}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

// registeredRoutes lists every "METHOD /path" pattern setupRoutes registers.
// Literal patterns come from routes.go's AST; the one loop-built family
// (per-entity notes reads) is listed explicitly, and any other computed
// pattern fails the test so a new route cannot slip past enforcement.
func registeredRoutes(t *testing.T) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "routes.go", nil, 0)
	require.NoError(t, err)
	var patterns []string
	computed := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "mux" || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok {
			p, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)
			patterns = append(patterns, p)
		} else {
			computed++
		}
		return true
	})
	require.Equal(t, 1, computed, "unexpected computed route pattern; list it in registeredRoutes")
	for _, prefix := range []string{"products", "feature-sets", "features", "requirements", "load-bearing-decisions"} {
		patterns = append(patterns, "GET /"+prefix+"/{id}/notes")
	}
	require.Greater(t, len(patterns), 60)
	return patterns
}

func realHandler(t *testing.T) (http.Handler, *http.ServeMux) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	mux := http.NewServeMux()
	setupRoutes(mux, pool, "", func(r *http.Request) (caller.Identity, bool) {
		c, ok := authdoor.FromContext(r.Context())
		return c.Identity, ok
	})
	h := authdoor.Middleware(authdoor.Config{
		Credentials: credStore{},
		Roles:       server.RoleConfig{OperatorRole: "op", ReaderRole: "ro"},
	})(mux)
	return h, mux
}

func TestEveryRegisteredRouteEnforcesAuth(t *testing.T) {
	h, mux := realHandler(t)
	for _, p := range registeredRoutes(t) {
		if p == "/healthz" {
			continue // probe, exempt by design
		}
		method, path, _ := strings.Cut(p, " ")
		path = strings.ReplaceAll(path, "{id}", "x")
		mk := func(token string) *http.Request {
			r := httptest.NewRequest(method, path, nil)
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			return r
		}
		if _, matched := mux.Handler(mk("")); matched == "" {
			t.Fatalf("%s: not served by the real mux", p)
		}
		for _, tok := range []string{"", "bogus"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, mk(tok))
			require.Equal(t, 401, rec.Code, p+" token="+tok)
		}
		if method != http.MethodGet {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, mk("reader"))
			require.Equal(t, 403, rec.Code, p+" reader")
		}
	}
}
