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

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// Task #2279: FR3/FR16 retirement of "/sgc/" (deployment list) and
// "/sgc/<id>" (SGC detail) into permanent redirects, plus their
// deployment-first equivalents "/deployments" and "/deployments/<id>"
// (amendment A3). See handlers_deployment_redirects.go for the handlers
// under test.

// fakeRedirectAPIClient is a dedicated fake for this file, mirroring the
// established per-file-fake convention (handlers_sgc_env_test.go,
// handlers_deployment_settings_test.go, deployment_actions_acceptance_test.go
// each keep their own rather than sharing handlers_sgc_test.go's
// fakeManManAPIClient) -- this one's shape (configurable SGC -> GameConfig
// -> Game hop, with independent per-hop error injection) is specific to
// the two-hop lookup amendment A1 requires and isn't needed anywhere else.
type fakeRedirectAPIClient struct {
	manmanpb.ManManAPIClient

	sgc              *manmanpb.ServerGameConfig
	getSGCErr        error
	gameConfig       *manmanpb.GameConfig
	getGameConfigErr error
	game             *manmanpb.Game
	getGameErr       error
}

func (f *fakeRedirectAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	if f.getSGCErr != nil {
		return nil, f.getSGCErr
	}
	return &manmanpb.GetServerGameConfigResponse{Config: f.sgc}, nil
}

func (f *fakeRedirectAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	if f.getGameConfigErr != nil {
		return nil, f.getGameConfigErr
	}
	return &manmanpb.GetGameConfigResponse{Config: f.gameConfig}, nil
}

func (f *fakeRedirectAPIClient) GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	if f.getGameErr != nil {
		return nil, f.getGameErr
	}
	return &manmanpb.GetGameResponse{Game: f.game}, nil
}

// newRedirectTestApp builds a real *App and *http.ServeMux from
// (*App).setupRoutes, with a real htmxauth.Authenticator in AuthModeNone
// (auto-authenticates every request, mirroring local dev -- same technique
// deployment_actions_acceptance_test.go's newAcceptanceFixture uses) so
// these tests exercise the actual route registration and auth wrapping a
// browser would hit, without constructing a session cookie.
func newRedirectTestApp(t *testing.T, api *fakeRedirectAPIClient) (*App, *http.ServeMux) {
	t.Helper()

	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "redirect-test-secret-at-least-32-bytes-long",
		SessionName:   "manmanv2_ui_redirect_test_session",
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	app := &App{
		auth: auth,
		grpc: &ControlClient{api: api},
	}
	mux := http.NewServeMux()
	app.setupRoutes(mux)
	return app, mux
}

func doRedirectGet(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestSGCListRedirect_PermanentToGames covers FR16's first disposition:
// "/sgc/" (the deployment list) permanently redirects to "/games". There
// never was a real list page to preserve an identifier for (NFR8's
// "preserve identifiers" clause only bites the detail redirect below).
func TestSGCListRedirect_PermanentToGames(t *testing.T) {
	_, mux := newRedirectTestApp(t, &fakeRedirectAPIClient{})

	w := doRedirectGet(mux, "/sgc/")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want %d (permanent redirect, NFR8)", w.Code, http.StatusMovedPermanently)
	}
	if loc := w.Header().Get("Location"); loc != "/games" {
		t.Errorf("Location = %q, want /games", loc)
	}
}

// TestSGCDetailRedirect_PermanentToGamesWithExpand covers FR16's second
// disposition plus amendment A1's two-hop lookup: "/sgc/<id>" permanently
// redirects to "/games?expand=<game_id>", where game_id is resolved via
// ServerGameConfig -> GameConfig -> Game (ServerGameConfig itself carries
// no game_id -- see manmanv2/protos/messages.proto). NFR8: the redirect
// lands on the equivalent new view and preserves the deployment's
// identifier (as the expand param, not the raw SGC id).
func TestSGCDetailRedirect_PermanentToGamesWithExpand(t *testing.T) {
	api := &fakeRedirectAPIClient{
		sgc:        &manmanpb.ServerGameConfig{ServerGameConfigId: 42, GameConfigId: 7},
		gameConfig: &manmanpb.GameConfig{ConfigId: 7, GameId: 99},
		game:       &manmanpb.Game{GameId: 99, Name: "Vanilla"},
	}
	_, mux := newRedirectTestApp(t, api)

	w := doRedirectGet(mux, "/sgc/42")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want %d (permanent redirect, NFR8)", w.Code, http.StatusMovedPermanently)
	}
	if loc := w.Header().Get("Location"); loc != "/games?expand=99" {
		t.Errorf("Location = %q, want /games?expand=99 (the resolved game, not the SGC or GameConfig id)", loc)
	}
}

// TestSGCDetailRedirect_A1FailurePaths covers amendment A1's required
// failure behaviour in full: SGC not found, the GameConfig hop failing,
// and the Game hop failing (game not found) must all redirect to plain
// "/games" with no expand parameter -- never an error response, and never
// dependent on the lookup succeeding (a permanent redirect must always
// succeed).
func TestSGCDetailRedirect_A1FailurePaths(t *testing.T) {
	cases := []struct {
		name string
		api  *fakeRedirectAPIClient
	}{
		{
			name: "SGC not found",
			api:  &fakeRedirectAPIClient{getSGCErr: context.DeadlineExceeded},
		},
		{
			name: "GameConfig hop fails",
			api: &fakeRedirectAPIClient{
				sgc:              &manmanpb.ServerGameConfig{ServerGameConfigId: 43, GameConfigId: 8},
				getGameConfigErr: context.DeadlineExceeded,
			},
		},
		{
			name: "Game not found",
			api: &fakeRedirectAPIClient{
				sgc:        &manmanpb.ServerGameConfig{ServerGameConfigId: 44, GameConfigId: 9},
				gameConfig: &manmanpb.GameConfig{ConfigId: 9, GameId: 100},
				getGameErr: context.DeadlineExceeded,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, mux := newRedirectTestApp(t, tc.api)

			w := doRedirectGet(mux, "/sgc/999")
			if w.Code != http.StatusMovedPermanently {
				t.Fatalf("status = %d, want %d -- a failed lookup must still redirect, never error", w.Code, http.StatusMovedPermanently)
			}
			loc := w.Header().Get("Location")
			if loc != "/games" {
				t.Errorf("Location = %q, want /games with no expand parameter (A1's fallback)", loc)
			}
		})
	}
}

// TestDeploymentsRoutes_MirrorSGCRedirects covers amendment A3: the
// deployment-first "/deployments" and "/deployments/<id>" paths exist and
// carry the identical redirect behaviour as their "/sgc/..." equivalents
// above.
func TestDeploymentsRoutes_MirrorSGCRedirects(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		_, mux := newRedirectTestApp(t, &fakeRedirectAPIClient{})
		w := doRedirectGet(mux, "/deployments")
		if w.Code != http.StatusMovedPermanently {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
		}
		if loc := w.Header().Get("Location"); loc != "/games" {
			t.Errorf("Location = %q, want /games", loc)
		}
	})

	t.Run("detail", func(t *testing.T) {
		api := &fakeRedirectAPIClient{
			sgc:        &manmanpb.ServerGameConfig{ServerGameConfigId: 42, GameConfigId: 7},
			gameConfig: &manmanpb.GameConfig{ConfigId: 7, GameId: 99},
			game:       &manmanpb.Game{GameId: 99, Name: "Vanilla"},
		}
		_, mux := newRedirectTestApp(t, api)
		w := doRedirectGet(mux, "/deployments/42")
		if w.Code != http.StatusMovedPermanently {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
		}
		if loc := w.Header().Get("Location"); loc != "/games?expand=99" {
			t.Errorf("Location = %q, want /games?expand=99", loc)
		}
	})

	t.Run("detail fallback on lookup failure", func(t *testing.T) {
		api := &fakeRedirectAPIClient{getSGCErr: context.DeadlineExceeded}
		_, mux := newRedirectTestApp(t, api)
		w := doRedirectGet(mux, "/deployments/999")
		if w.Code != http.StatusMovedPermanently {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
		}
		if loc := w.Header().Get("Location"); loc != "/games" {
			t.Errorf("Location = %q, want /games (no expand)", loc)
		}
	})
}

// TestSGCLibraryRoutes_RetiredIntoRedirect is this task's (M6 #2370, NFR1)
// replacement for the old A2 regression guard (TestSGCNonPageRoutes_
// NotSwallowedByRedirect / TestAddLibraryForm_StillPostsSuccessfully):
// "/sgc/add-library", "/sgc/remove-library" and
// "/sgc/api/available-libraries" no longer have their own mux
// registrations -- library attachment retired to GC scope, managed from
// the Games page panel (#2367) -- so a request to any of them now falls
// through to the "/sgc/" catch-all's redirect fallback (handleSGCRoutes ->
// handleSGCDetailRedirect) instead of 404ing or dispatching to a
// since-removed handler.
func TestSGCLibraryRoutes_RetiredIntoRedirect(t *testing.T) {
	_, mux := newRedirectTestApp(t, &fakeRedirectAPIClient{})

	for _, path := range []string{
		"/sgc/add-library",
		"/sgc/remove-library",
		"/sgc/api/available-libraries",
	} {
		t.Run(path, func(t *testing.T) {
			w := doRedirectGet(mux, path)
			if w.Code != http.StatusMovedPermanently {
				t.Fatalf("%s status = %d, want %d (redirected via the /sgc/ catch-all, not 404)", path, w.Code, http.StatusMovedPermanently)
			}
			if loc := w.Header().Get("Location"); loc != "/games" {
				t.Errorf("%s Location = %q, want /games", path, loc)
			}
		})
	}
}

// setupRoutesRegisteredPatterns parses manmanv2/ui/main.go's AST (via the
// Bazel runfiles manifest, same technique as
// nfr5_bulk_env_write_guard_test.go and
// manmanv2/host/workshop/nfr7_dependency_guard_test.go) and returns every
// string literal pattern passed to mux.HandleFunc(...) inside
// (*App).setupRoutes. This is "enumerate from the mux" the only way
// actually available here: net/http.ServeMux exposes no route-enumeration
// API on the object itself (see main_test.go's manmanv2RouteTable doc
// comment), but its *registration code* is a fixed, parseable shape -- so
// deriving the list from that source, rather than hand-duplicating it a
// second time in this test, is what makes a future accidental route
// removal actually fail this test instead of silently going unnoticed.
func setupRoutesRegisteredPatterns(t *testing.T) []string {
	t.Helper()

	anchor, err := runfiles.Rlocation("_main/manmanv2/ui/main.go")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(main.go): %v (is manmanv2/ui's ui_test data glob still present?)", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, anchor, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", anchor, err)
	}

	var patterns []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "mux" {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unquoting mux.HandleFunc pattern literal %s: %v", lit.Value, err)
		}
		patterns = append(patterns, pattern)
		return true
	})

	if len(patterns) == 0 {
		t.Fatalf("discovered zero mux.HandleFunc(...) registrations in %s -- guard is not checking anything", anchor)
	}
	return patterns
}

// probePathForPattern derives a concrete request path that
// http.ServeMux(1.22+)'s longest-pattern-wins dispatch sends to exactly
// this pattern: exact patterns (no trailing slash) are used verbatim;
// subtree patterns (trailing slash) get one extra path segment appended,
// mirroring manmanv2RouteTable's existing hand-picked probes (main_test.go)
// for the same shape of pattern.
func probePathForPattern(pattern string) string {
	if strings.HasSuffix(pattern, "/") {
		return pattern + "1"
	}
	return pattern
}

// TestAC6_EveryRegisteredRouteReachable is the AC6 reachability sweep:
// every route setupRoutes registers must resolve to its own pattern (not
// http.NotFoundHandler's empty pattern, and not some broader pattern that
// swallowed it -- the general form of the A2 regression above) when
// probed. This only resolves routing (mux.Handler, which does not invoke
// the handler), not full handler execution -- deliberately: a generic
// probe can't supply every handler's specific backend fixtures, and what
// AC6 actually guards against is a route silently disappearing from
// setupRoutes, not a specific handler's business logic.
func TestAC6_EveryRegisteredRouteReachable(t *testing.T) {
	_, mux := newRedirectTestApp(t, &fakeRedirectAPIClient{})

	for _, pattern := range setupRoutesRegisteredPatterns(t) {
		pattern := pattern
		t.Run(pattern, func(t *testing.T) {
			probe := probePathForPattern(pattern)
			req := httptest.NewRequest(http.MethodGet, probe, nil)
			_, gotPattern := mux.Handler(req)
			if gotPattern == "" {
				t.Fatalf("probe %q for registered pattern %q resolved to no route (404) -- AC6: nothing on FR16's disposition list may become unreachable", probe, pattern)
			}
			if gotPattern != pattern {
				t.Errorf("probe %q for registered pattern %q resolved to pattern %q instead -- a broader route is swallowing it", probe, pattern, gotPattern)
			}
		})
	}
}
