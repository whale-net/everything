package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file guards task #2270 (root plan #2266): the Games page's flat
// list, in-place client-side expansion, and collapsed-row run
// state/connect address. NFR7's bounded fetch is the headline concern --
// TestHandleGames_NFR7_ConstantCallCount below -- because a regression that
// moved any of the five fleet-wide list calls into a per-game or
// per-deployment loop would compile and render fine, and only show up as a
// silent RPC-count blowup at real fleet size.
//
// fakeGamesAPIClient is scoped to handleGames' call graph: ListGames,
// ListGameConfigs, ListServerGameConfigs, ListServers, ListSessions,
// ListPendingRestarts. Any
// call to an un-overridden ManManAPIClient method panics on the nil
// embedded interface, deliberately -- see handlers_sgc_test.go's
// fakeManManAPIClient / handlers_home_test.go's fakeDashboardAPIClient for
// the same convention.
type fakeGamesAPIClient struct {
	manmanpb.ManManAPIClient
	manmanpb.WorkshopServiceClient

	games       []*manmanpb.Game
	configs     []*manmanpb.GameConfig
	deployments []*manmanpb.ServerGameConfig
	servers     []*manmanpb.Server
	sessions    []*manmanpb.Session

	// pendingRestartStates backs ListPendingRestarts' positive path (task
	// #2372's validation gap: every other test in this file leaves this nil,
	// which only ever exercises the "no restart record" branch -- see
	// TestHandleGames_RestartStateBadge_RendersOnDeploymentRow below for the
	// case that actually populates it).
	pendingRestartStates []*manmanpb.PendingRestartState

	calls map[string]int
}

func newFakeGamesAPIClient() *fakeGamesAPIClient {
	return &fakeGamesAPIClient{calls: map[string]int{}}
}

func (f *fakeGamesAPIClient) ListGames(ctx context.Context, in *manmanpb.ListGamesRequest, opts ...grpc.CallOption) (*manmanpb.ListGamesResponse, error) {
	f.calls["ListGames"]++
	return &manmanpb.ListGamesResponse{Games: f.games}, nil
}

func (f *fakeGamesAPIClient) ListGameConfigs(ctx context.Context, in *manmanpb.ListGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigsResponse, error) {
	f.calls["ListGameConfigs"]++
	return &manmanpb.ListGameConfigsResponse{Configs: f.configs}, nil
}

func (f *fakeGamesAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	f.calls["ListServerGameConfigs"]++
	return &manmanpb.ListServerGameConfigsResponse{Configs: f.deployments}, nil
}

func (f *fakeGamesAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	f.calls["ListServers"]++
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeGamesAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	f.calls["ListSessions"]++
	if in.LiveOnly {
		var live []*manmanpb.Session
		for _, s := range f.sessions {
			if (in.ServerGameConfigId == 0 || s.ServerGameConfigId == in.ServerGameConfigId) && (s.Status == "running" || components.IsTransientStatus(s.Status)) {
				live = append(live, s)
			}
		}
		return &manmanpb.ListSessionsResponse{Sessions: live}, nil
	}
	return &manmanpb.ListSessionsResponse{Sessions: f.sessions}, nil
}

func (f *fakeGamesAPIClient) StartSession(ctx context.Context, in *manmanpb.StartSessionRequest, opts ...grpc.CallOption) (*manmanpb.StartSessionResponse, error) {
	f.calls["StartSession"]++
	return &manmanpb.StartSessionResponse{
		Session: &manmanpb.Session{
			SessionId:          999,
			ServerGameConfigId: in.ServerGameConfigId,
			Status:             "starting",
		},
	}, nil
}

func (f *fakeGamesAPIClient) StopSession(ctx context.Context, in *manmanpb.StopSessionRequest, opts ...grpc.CallOption) (*manmanpb.StopSessionResponse, error) {
	f.calls["StopSession"]++
	return &manmanpb.StopSessionResponse{
		Session: &manmanpb.Session{
			SessionId: in.SessionId,
			Status:    "stopping",
		},
	}, nil
}

func (f *fakeGamesAPIClient) RestartDeployment(ctx context.Context, in *manmanpb.RestartDeploymentRequest, opts ...grpc.CallOption) (*manmanpb.RestartDeploymentResponse, error) {
	f.calls["RestartDeployment"]++
	return &manmanpb.RestartDeploymentResponse{
		StoppingSession: &manmanpb.Session{
			ServerGameConfigId: in.ServerGameConfigId,
			Status:             "stopping",
		},
	}, nil
}

// ListPendingRestarts backs handleGames' FR12/#1735 batched restart-state
// fetch (task #2372's retained-capability verification). This fake never
// models an in-flight/failed/expired pending_restarts row (that is
// restart_state_test.go's job) -- it always reports "no restart record" for
// every requested sgc, matching control-api's own contract for a caller
// that queries any sgc set, and, critically, returns a non-nil response so
// handleGames' for _, state := range resp.States loop has something to
// range over instead of dereferencing a nil resp.
func (f *fakeGamesAPIClient) ListPendingRestarts(ctx context.Context, in *manmanpb.ListPendingRestartsRequest, opts ...grpc.CallOption) (*manmanpb.ListPendingRestartsResponse, error) {
	f.calls["ListPendingRestarts"]++
	return &manmanpb.ListPendingRestartsResponse{States: f.pendingRestartStates}, nil
}

func (f *fakeGamesAPIClient) GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	f.calls["GetGame"]++
	for _, g := range f.games {
		if g.GameId == in.GameId {
			return &manmanpb.GetGameResponse{Game: g}, nil
		}
	}
	return nil, status.Error(codes.NotFound, "game not found")
}

func (f *fakeGamesAPIClient) ListAddonPathPresets(ctx context.Context, in *manmanpb.ListAddonPathPresetsRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonPathPresetsResponse, error) {
	f.calls["ListAddonPathPresets"]++
	return &manmanpb.ListAddonPathPresetsResponse{}, nil
}

func (f *fakeGamesAPIClient) ListGameConfigVolumes(ctx context.Context, in *manmanpb.ListGameConfigVolumesRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigVolumesResponse, error) {
	f.calls["ListGameConfigVolumes"]++
	return &manmanpb.ListGameConfigVolumesResponse{}, nil
}

func (f *fakeGamesAPIClient) ListActionDefinitions(ctx context.Context, in *manmanpb.ListActionDefinitionsRequest, opts ...grpc.CallOption) (*manmanpb.ListActionDefinitionsResponse, error) {
	f.calls["ListActionDefinitions"]++
	return &manmanpb.ListActionDefinitionsResponse{}, nil
}

type fakeGamesWorkshopClient struct {
	manmanpb.WorkshopServiceClient
}

func (f *fakeGamesWorkshopClient) ListAddonPathPresets(ctx context.Context, in *manmanpb.ListAddonPathPresetsRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonPathPresetsResponse, error) {
	return &manmanpb.ListAddonPathPresetsResponse{}, nil
}
// buildFakeGamesData constructs n games, each with one config and one
// running, resolvable-address deployment -- enough to exercise the full
// join/rollup while staying cheap to generate at n=20 for the NFR7 size
// comparison.
func buildFakeGamesData(n int) *fakeGamesAPIClient {
	f := newFakeGamesAPIClient()
	f.servers = []*manmanpb.Server{
		{ServerId: 1, HostPublicAddress: "host-01"},
	}
	for i := 0; i < n; i++ {
		gameID := int64(i + 1)
		configID := int64(i + 1)
		sgcID := int64(i + 1)

		f.games = append(f.games, &manmanpb.Game{
			GameId: gameID,
			Name:   fmt.Sprintf("Game-%03d", i),
		})
		f.configs = append(f.configs, &manmanpb.GameConfig{
			ConfigId: configID,
			GameId:   gameID,
			Name:     fmt.Sprintf("Config-%03d", i),
		})
		f.deployments = append(f.deployments, &manmanpb.ServerGameConfig{
			ServerGameConfigId: sgcID,
			ServerId:           1,
			GameConfigId:       configID,
			PortBindings: []*manmanpb.PortBinding{
				{HostPort: int32(25000 + i), Protocol: "TCP"},
			},
			Status: "active",
		})
		f.sessions = append(f.sessions, &manmanpb.Session{
			SessionId:          sgcID,
			ServerGameConfigId: sgcID,
			StartedAt:          1000,
			Status:             "running",
		})
	}
	return f
}

func renderGamesHTTP(t *testing.T, api *fakeGamesAPIClient, target string) (int, string) {
	t.Helper()
	app := &App{grpc: &ControlClient{api: api}}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleGames(w, req)
	return w.Code, w.Body.String()
}

func devAuth(t *testing.T) *htmxauth.Authenticator {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "test-secret-at-least-32-bytes-long!!",
		SessionName:   "test_session",
	})
	if err != nil {
		t.Fatalf("failed to create dev authenticator: %v", err)
	}
	return auth
}

func renderGameDetailHTTP(t *testing.T, api *fakeGamesAPIClient, target string, asAdmin ...bool) (int, string) {
	t.Helper()
	admin := true
	if len(asAdmin) > 0 {
		admin = asAdmin[0]
	}
	app := &App{grpc: &ControlClient{api: api, workshop: api}}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	if admin {
		auth := devAuth(t)
		app.auth = auth
		auth.RequireAuthFunc(app.handleGameDetail)(w, req)
	} else {
		app.handleGameDetail(w, req)
	}
	return w.Code, w.Body.String()
}

// TestHandleGames_NFR7_ConstantCallCount is the headline test (NFR7): the
// number of RPC calls handleGames issues must not grow with the number of
// games, configs, or deployments rendered. Two sizes are compared directly
// -- a regression that added a call inside a per-game or per-deployment
// loop would make the 20-game call counts strictly larger than the 2-game
// ones, while today's join (buildGameRows, issuing zero RPCs itself) keeps
// them identical.
//
// The exact-method-set check below additionally guards against a
// regression that wires the Deployments section to a live per-deployment
// or per-sgc call: since fakeGamesAPIClient panics on any un-overridden
// ManManAPIClient method, that would panic this test outright; the
// len(calls) assertion further catches a hypothetical new call that
// happened to target one of the five already-faked methods' RPC family
// without panicking.
func TestHandleGames_NFR7_ConstantCallCount(t *testing.T) {
	small := buildFakeGamesData(2)
	code, _ := renderGamesHTTP(t, small, "/games")
	if code != http.StatusOK {
		t.Fatalf("2-game render status = %d, want 200", code)
	}

	large := buildFakeGamesData(20)
	code, _ = renderGamesHTTP(t, large, "/games")
	if code != http.StatusOK {
		t.Fatalf("20-game render status = %d, want 200", code)
	}

	wantMethods := []string{"ListGames", "ListGameConfigs", "ListServerGameConfigs", "ListServers", "ListSessions", "ListPendingRestarts"}
	for _, method := range wantMethods {
		if small.calls[method] != large.calls[method] {
			t.Errorf("%s call count grew with fleet size: 2 games -> %d calls, 20 games -> %d calls (NFR7 requires a constant count)", method, small.calls[method], large.calls[method])
		}
		if small.calls[method] == 0 {
			t.Errorf("%s was never called", method)
		}
	}
	if len(small.calls) != len(wantMethods) {
		t.Errorf("small.calls = %+v, want exactly the %d known methods (an extra call key would mean an unexpected RPC was added, e.g. for Configurations or Workshop Libraries)", small.calls, len(wantMethods))
	}
	if len(large.calls) != len(wantMethods) {
		t.Errorf("large.calls = %+v, want exactly the %d known methods (an extra call key would mean an unexpected RPC was added, e.g. for Configurations or Workshop Libraries)", large.calls, len(wantMethods))
	}
}

// TestHandleGames_RestartStateBadge_RendersOnDeploymentRow is task #2372's
// own validation gap closed: every other test in this file leaves
// fakeGamesAPIClient's ListPendingRestarts returning an empty response, so
// none of them ever exercised the pending-restart path -- they prove the
// batched call happens (NFR7's exact-method-set/call-count checks above),
// not that a real pending-restart entry actually threads through
// buildGameRows/buildGameDeploymentOverview into the rendered Games page
// (FR17's retained-capability clause: the restart-state badge, one of the
// two capabilities /sessions's retirement must not drop). A deployment
// with a "pending" PendingRestartState must render the Daily Ops Overview
// panel's "Restarting" state on its expanded row (computeOverviewStatus /
// overviewStatusBadge, game_detail.templ) -- a different badge than the
// old gameDeploymentRow's components.RestartBadge, not a dropped
// capability (see deployment_row.templ's DeploymentRowInner doc comment).
func TestHandleGames_RestartStateBadge_RendersOnDeploymentRow(t *testing.T) {
	const sgcID = int64(100)
	api := &fakeGamesAPIClient{
		calls:   map[string]int{},
		games:   []*manmanpb.Game{{GameId: 1, Name: "Restart Game"}},
		configs: []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1, Name: "Config"}},
		deployments: []*manmanpb.ServerGameConfig{
			{ServerGameConfigId: sgcID, ServerId: 1, GameConfigId: 10, Status: "active"},
		},
		servers: []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}},
		sessions: []*manmanpb.Session{
			{SessionId: 1, ServerGameConfigId: sgcID, StartedAt: 1000, Status: "stopping"},
		},
		pendingRestartStates: []*manmanpb.PendingRestartState{
			{
				ServerGameConfigId: sgcID,
				PendingRestartId:   7,
				Status:             "pending",
				GatingSessionId:    1,
				CreatedAtUnix:      1000,
			},
		},
	}

	code, body := renderGamesHTTP(t, api, "/games")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	if !strings.Contains(body, "Restarting") {
		t.Errorf("expected the restart-state badge (\"Restarting\") to render on the deployment row for a pending restart, got none in body: %s", body)
	}
}

func gameRowByID(t *testing.T, rows []pages.GameRow, gameID int64) pages.GameRow {
	t.Helper()
	for _, r := range rows {
		if r.GameID == gameID {
			return r
		}
	}
	t.Fatalf("no row with GameID %d in %+v", gameID, rows)
	return pages.GameRow{}
}

// TestBuildGameRows_RunStateRollup covers the run-state rollup: a game
// with one running and one stopped deployment rolls up to running, and --
// guarding the exact confusion the issue's ground-truth table warns about
// -- a deployment whose ServerGameConfig.status is "inactive" (the
// unrelated active/inactive lifecycle flag) while its latest session is
// running still rolls up to running.
func TestBuildGameRows_RunStateRollup(t *testing.T) {
	games := []*manmanpb.Game{{GameId: 1, Name: "Multi"}}
	configs := []*manmanpb.GameConfig{
		{ConfigId: 10, GameId: 1},
		{ConfigId: 11, GameId: 1},
	}
	deployments := []*manmanpb.ServerGameConfig{
		{ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10, Status: "active"},
		// Deliberately "inactive" lifecycle status but a running latest
		// session -- must still roll up to running (FR5 ground truth).
		{ServerGameConfigId: 101, ServerId: 1, GameConfigId: 11, Status: "inactive"},
	}
	servers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}}
	sessions := []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: "stopped"},
		{SessionId: 2, ServerGameConfigId: 101, StartedAt: 1000, Status: "running"},
	}

	rows := buildGameRows(games, configs, deployments, servers, sessions, nil)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].RunState != components.DeploymentRunning {
		t.Errorf("RunState = %q, want %q (an inactive-lifecycle SGC with a running session must still roll up to running)", rows[0].RunState, components.DeploymentRunning)
	}

	// Sanity: an all-stopped game rolls up to stopped.
	allStopped := buildGameRows(
		[]*manmanpb.Game{{GameId: 2, Name: "Stopped"}},
		[]*manmanpb.GameConfig{{ConfigId: 20, GameId: 2}},
		[]*manmanpb.ServerGameConfig{{ServerGameConfigId: 200, ServerId: 1, GameConfigId: 20, Status: "active"}},
		servers,
		[]*manmanpb.Session{{SessionId: 3, ServerGameConfigId: 200, StartedAt: 1000, Status: "stopped"}},
		nil,
	)
	if allStopped[0].RunState != components.DeploymentStopped {
		t.Errorf("RunState = %q, want %q for an all-stopped game", allStopped[0].RunState, components.DeploymentStopped)
	}
}

// TestBuildGameRows_ConnectAddress covers AC2: a game with a running,
// resolvable-address deployment gets a Connect view with the exact
// components.ComputeConnectAddresses string; a game with no resolvable
// address gets the shared Unavailable state, never a blank.
func TestBuildGameRows_ConnectAddress(t *testing.T) {
	games := []*manmanpb.Game{
		{GameId: 1, Name: "Resolvable"},
		{GameId: 2, Name: "Unresolvable"},
	}
	configs := []*manmanpb.GameConfig{
		{ConfigId: 10, GameId: 1},
		{ConfigId: 20, GameId: 2},
	}
	deployments := []*manmanpb.ServerGameConfig{
		{
			ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10,
			PortBindings: []*manmanpb.PortBinding{{HostPort: 25565, Protocol: "TCP"}},
		},
		{
			// server 2 has no host_public_address configured.
			ServerGameConfigId: 200, ServerId: 2, GameConfigId: 20,
			PortBindings: []*manmanpb.PortBinding{{HostPort: 25566, Protocol: "TCP"}},
		},
	}
	servers := []*manmanpb.Server{
		{ServerId: 1, HostPublicAddress: "host-01"},
		{ServerId: 2, HostPublicAddress: ""},
	}
	sessions := []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: "running"},
		{SessionId: 2, ServerGameConfigId: 200, StartedAt: 1000, Status: "running"},
	}

	rows := buildGameRows(games, configs, deployments, servers, sessions, nil)

	want := components.BuildConnectAddressView("host-01", deployments[0].PortBindings)
	got := gameRowByID(t, rows, 1).Connect
	if got.Unavailable || len(got.Addresses) != 1 || got.Addresses[0].Address != want.Addresses[0].Address {
		t.Errorf("resolvable game Connect = %+v, want %+v", got, want)
	}

	unresolvable := gameRowByID(t, rows, 2).Connect
	if !unresolvable.Unavailable {
		t.Errorf("unresolvable game Connect.Unavailable = false, want true (never a blank)")
	}
}

// TestBuildGameRows_DeterministicSort covers the deterministic-sort
// requirement: shuffled input yields identical output order across runs,
// sorted by name and tie-broken by game_id.
func TestBuildGameRows_DeterministicSort(t *testing.T) {
	names := map[int64]string{1: "Alpha", 2: "Beta", 3: "Beta", 4: "Gamma"}
	makeGames := func(order []int64) []*manmanpb.Game {
		var games []*manmanpb.Game
		for _, id := range order {
			games = append(games, &manmanpb.Game{GameId: id, Name: names[id]})
		}
		return games
	}

	wantOrder := []int64{1, 2, 3, 4} // Alpha(1), Beta(2), Beta(3) tie-broken by id, Gamma(4)
	for _, order := range [][]int64{
		{1, 2, 3, 4},
		{4, 3, 2, 1},
		{3, 1, 4, 2},
	} {
		rows := buildGameRows(makeGames(order), nil, nil, nil, nil, nil)
		got := make([]int64, len(rows))
		for i, r := range rows {
			got[i] = r.GameID
		}
		if len(got) != len(wantOrder) {
			t.Fatalf("input order %v: got %v, want order %v", order, got, wantOrder)
		}
		for i := range wantOrder {
			if got[i] != wantOrder[i] {
				t.Errorf("input order %v produced output order %v, want %v (deterministic sort)", order, got, wantOrder)
				break
			}
		}
	}
}

// TestBuildGameRows_DeploymentsResolveViaConfigJoin covers the
// deployment -> game resolution buildGameRows relies on: ServerGameConfig
// carries no game_id, only game_config_id, so a deployment must resolve to
// its game via game_config_id -> GameConfig.game_id; a config belonging to
// a different game must never leak a deployment into this game's row.
func TestBuildGameRows_DeploymentsResolveViaConfigJoin(t *testing.T) {
	games := []*manmanpb.Game{
		{GameId: 1, Name: "Alpha"},
		{GameId: 2, Name: "Beta"},
	}
	configs := []*manmanpb.GameConfig{
		{ConfigId: 10, GameId: 1, Name: "Survival", Image: "itzg/minecraft-server:latest"},
		{ConfigId: 20, GameId: 2, Name: "Dedicated", Image: "valheim:latest"},
	}
	deployments := []*manmanpb.ServerGameConfig{
		{ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10, Status: "active"},
		{ServerGameConfigId: 101, ServerId: 1, GameConfigId: 10, Status: "active"},
		{ServerGameConfigId: 200, ServerId: 1, GameConfigId: 20, Status: "active"},
	}
	servers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}}
	sessions := []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: "running"},
		{SessionId: 2, ServerGameConfigId: 101, StartedAt: 1000, Status: "running"},
		{SessionId: 3, ServerGameConfigId: 200, StartedAt: 1000, Status: "running"},
	}

	rows := buildGameRows(games, configs, deployments, servers, sessions, nil)

	alpha := gameRowByID(t, rows, 1)
	if len(alpha.Overview.Deployments) != 2 {
		t.Fatalf("game 1 Overview.Deployments = %+v, want 2 entries (no leak from game 2)", alpha.Overview.Deployments)
	}

	beta := gameRowByID(t, rows, 2)
	if len(beta.Overview.Deployments) != 1 {
		t.Fatalf("game 2 Overview.Deployments = %+v, want exactly 1 entry (no leak from game 1)", beta.Overview.Deployments)
	}
}

// TestHandleGames_WD1WD6_NoPlayerCountOrLastPlayed guards WD1 and WD6: the
// collapsed row must never render a player count, a "last played" value,
// or an aggregate rollup string -- documented, intentional divergences
// from 90-v2-games.
// TestHandleGames_WD1WD6_NoPlayerCountOrLastPlayed guards WD1/WD6: no
// player *count* or "last played" value anywhere on the collapsed or
// expanded row. This deliberately checks for those specific phrases, not a
// bare "player" substring: the Daily Ops Overview panel (GameOverview,
// now shared by /games and /games/{id}) already carries incidental,
// pre-existing copy like "Formatted host and port for players to join" in
// its Connection Info caption, which is not a player-count/last-played
// value and is not what WD1/WD6 forbids.
func TestHandleGames_WD1WD6_NoPlayerCountOrLastPlayed(t *testing.T) {
	api := buildFakeGamesData(3)
	code, body := renderGamesHTTP(t, api, "/games")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	lower := strings.ToLower(body)
	for _, forbidden := range []string{"player count", "players online", "last played"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("rendered Games page contains forbidden text %q (WD1/WD6)", forbidden)
		}
	}
}

// TestHandleGames_FR2_NoSGCTerminology guards FR2: no user-facing display
// text on the Games page may contain "SGC" or "server game config".
// sgcWordRe matches a standalone "sgc" -- case-insensitive, not part of a
// longer identifier like the ops panel's internal hidden form field
// name="sgc_id" (GameOverview/overviewDeploymentContent, shared by /games
// and /games/{id} -- not display text, never seen by a user).
var sgcWordRe = regexp.MustCompile(`(?i)\bsgc\b`)

// TestHandleGames_FR2_NoSGCTerminology guards FR2: no user-facing display
// text on the Games page may contain "SGC" or "server game config". This
// deliberately excludes the ops panel's internal hidden form field
// (name="sgc_id", never rendered as visible text) via sgcWordRe's word
// boundary, rather than a bare substring match -- see WD1/WD6's guard
// above for the same "pre-existing non-display markup now also renders on
// /games" situation.
func TestHandleGames_FR2_NoSGCTerminology(t *testing.T) {
	api := buildFakeGamesData(3)
	code, body := renderGamesHTTP(t, api, "/games")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	lower := strings.ToLower(body)
	if sgcWordRe.MatchString(lower) {
		t.Errorf("rendered Games page contains standalone %q (FR2 forbids SGC terminology in display text)", "sgc")
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf("rendered Games page contains %q (FR2 forbids raw entity terminology in display text)", "server game config")
	}
}

// TestHandleGames_ExpandParam covers the `expand` query parameter (spec
// amendment A1): a valid id expands that row and only that row; an unknown
// id renders normally with nothing expanded and no error; an absent
// parameter renders normally.
func TestHandleGames_ExpandParam(t *testing.T) {
	api := buildFakeGamesData(3) // game_id 1, 2, 3

	t.Run("valid id expands that row only", func(t *testing.T) {
		code, body := renderGamesHTTP(t, api, "/games?expand=2")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", code, body)
		}
		assertRowExpanded(t, body, 2, true)
		assertRowExpanded(t, body, 1, false)
		assertRowExpanded(t, body, 3, false)
	})

	t.Run("unknown id renders normally with nothing expanded, no error", func(t *testing.T) {
		code, body := renderGamesHTTP(t, api, "/games?expand=9999")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (unknown expand id must never error); body: %s", code, body)
		}
		assertRowExpanded(t, body, 1, false)
		assertRowExpanded(t, body, 2, false)
		assertRowExpanded(t, body, 3, false)
	})

	t.Run("absent parameter renders normally", func(t *testing.T) {
		code, body := renderGamesHTTP(t, api, "/games")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", code, body)
		}
		assertRowExpanded(t, body, 1, false)
		assertRowExpanded(t, body, 2, false)
		assertRowExpanded(t, body, 3, false)
	})
}

// TestBuildGameRows_Deployments_ListsAllDeployments guards task #2272
// (FR6, AC1): the expanded row's Deployments section lists every
// deployment of that game, joined via game_config_id -> GameConfig.game_id
// -- and, just as importantly, deployments belonging to a *different*
// game must never leak into this game's Deployments list.
func TestBuildGameRows_Deployments_ListsAllDeployments(t *testing.T) {
	games := []*manmanpb.Game{
		{GameId: 1, Name: "Alpha"},
		{GameId: 2, Name: "Beta"},
	}
	configs := []*manmanpb.GameConfig{
		{ConfigId: 10, GameId: 1, Name: "Alpha-Config-A"},
		{ConfigId: 11, GameId: 1, Name: "Alpha-Config-B"},
		{ConfigId: 20, GameId: 2, Name: "Beta-Config"},
	}
	deployments := []*manmanpb.ServerGameConfig{
		{ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10, Status: "active"},
		{ServerGameConfigId: 101, ServerId: 1, GameConfigId: 11, Status: "active"},
		{ServerGameConfigId: 200, ServerId: 1, GameConfigId: 20, Status: "active"},
	}
	servers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}}
	sessions := []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: "running"},
		{SessionId: 2, ServerGameConfigId: 101, StartedAt: 1000, Status: "stopped"},
		{SessionId: 3, ServerGameConfigId: 200, StartedAt: 1000, Status: "running"},
	}

	rows := buildGameRows(games, configs, deployments, servers, sessions, nil)

	alpha := gameRowByID(t, rows, 1)
	if len(alpha.Overview.Deployments) != 2 {
		t.Fatalf("Alpha Overview.Deployments = %d rows, want 2 (100 and 101): %+v", len(alpha.Overview.Deployments), alpha.Overview.Deployments)
	}
	gotSGCIDs := map[int64]bool{}
	for _, dep := range alpha.Overview.Deployments {
		gotSGCIDs[dep.SGCID] = true
		if dep.SGCID == 200 {
			t.Errorf("Alpha's Overview.Deployments contains SGC 200, which belongs to Beta (leaked across games)")
		}
	}
	for _, want := range []int64{100, 101} {
		if !gotSGCIDs[want] {
			t.Errorf("Alpha Overview.Deployments missing SGC %d: got %+v", want, alpha.Overview.Deployments)
		}
	}

	beta := gameRowByID(t, rows, 2)
	if len(beta.Overview.Deployments) != 1 || beta.Overview.Deployments[0].SGCID != 200 {
		t.Fatalf("Beta Overview.Deployments = %+v, want exactly [SGC 200]", beta.Overview.Deployments)
	}
}

// TestBuildGameRows_Overview_MatchesBuildGameDeploymentOverview is this
// restructure's anti-drift guard: the Games row's Daily Ops Overview panel
// must derive a deployment's status and Start/Stop/Restart availability
// identically to what buildGameOverviewData computes for the detail page's
// Overview tab -- both call buildGameDeploymentOverview. This test drives
// buildGameRows and a direct buildGameDeploymentOverview call against the
// identical fixture session and asserts they agree, across running,
// stopped, and every transitional/error state computeOverviewStatus's own
// table documents (pending, starting, stopping, crashed, lost, and no
// session at all).
func TestBuildGameRows_Overview_MatchesBuildGameDeploymentOverview(t *testing.T) {
	game := &manmanpb.Game{GameId: 1, Name: "Drift"}
	config := &manmanpb.GameConfig{ConfigId: 10, GameId: 1, Name: "Cfg"}
	servers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}}

	cases := []struct {
		name          string
		sessionStatus string // "" means no session at all
	}{
		{"no session", ""},
		{"pending", "pending"},
		{"starting", "starting"},
		{"running", "running"},
		{"stopping", "stopping"},
		{"stopped", "stopped"},
		{"crashed", "crashed"},
		{"lost", "lost"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deployment := &manmanpb.ServerGameConfig{ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10, Status: "active"}
			var sessions []*manmanpb.Session
			var latest *manmanpb.Session
			if tc.sessionStatus != "" {
				latest = &manmanpb.Session{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: tc.sessionStatus}
				sessions = append(sessions, latest)
			}

			rows := buildGameRows(
				[]*manmanpb.Game{game},
				[]*manmanpb.GameConfig{config},
				[]*manmanpb.ServerGameConfig{deployment},
				servers,
				sessions,
				nil,
			)
			row := gameRowByID(t, rows, 1)
			if len(row.Overview.Deployments) != 1 {
				t.Fatalf("Overview.Deployments = %d rows, want 1", len(row.Overview.Deployments))
			}
			got := row.Overview.Deployments[0]

			// The independent, same-fixture derivation: exactly what
			// buildGameOverviewData calls for the detail page's Overview
			// tab, called directly against the identical latest session.
			want := buildGameDeploymentOverview(1, config, servers[0], deployment, latest, nil)

			if !reflect.DeepEqual(got, want) {
				t.Errorf("state %q: Games row Overview = %+v, buildGameDeploymentOverview = %+v (anti-drift violation)", tc.sessionStatus, got, want)
			}

			// End-to-end: render the panel exactly as the page does
			// (GameOverview -> overviewDeploymentContent) and confirm
			// button presence matches want, not just the struct field.
			var buf bytes.Buffer
			if err := pages.GameOverview(row.Overview).Render(context.Background(), &buf); err != nil {
				t.Fatalf("GameOverview.Render: %v", err)
			}
			html := buf.String()
			if strings.Contains(html, `value="start"`) != want.CanStart {
				t.Errorf("state %q: rendered Start form presence = %v, want %v", tc.sessionStatus, strings.Contains(html, `value="start"`), want.CanStart)
			}
			if strings.Contains(html, `value="stop"`) != want.CanStop {
				t.Errorf("state %q: rendered Stop form presence = %v, want %v", tc.sessionStatus, strings.Contains(html, `value="stop"`), want.CanStop)
			}
			if strings.Contains(html, `value="restart"`) != want.CanRestart {
				t.Errorf("state %q: rendered Restart form presence = %v, want %v", tc.sessionStatus, strings.Contains(html, `value="restart"`), want.CanRestart)
			}
		})
	}
}

// TestBuildGameRows_Deployments_RunStateUsesComputeDeploymentStatus guards
// the ground-truth table: a deployment row's run state comes from
// components.ComputeDeploymentStatus(latest session), never
// ServerGameConfig.status -- including the case where SGC.status is
// "inactive" but the latest session is running.
func TestBuildGameRows_Deployments_RunStateUsesComputeDeploymentStatus(t *testing.T) {
	games := []*manmanpb.Game{{GameId: 1, Name: "G"}}
	configs := []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1}}
	deployments := []*manmanpb.ServerGameConfig{
		{ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10, Status: "inactive"},
	}
	servers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}}
	sessions := []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: "running"},
	}

	rows := buildGameRows(games, configs, deployments, servers, sessions, nil)
	row := gameRowByID(t, rows, 1)
	if len(row.Overview.Deployments) != 1 {
		t.Fatalf("Overview.Deployments = %d rows, want 1", len(row.Overview.Deployments))
	}
	dep := row.Overview.Deployments[0]
	if dep.Status != "Online" {
		t.Errorf("deployment with inactive SGC.status but a running latest session must show Online status: got %q", dep.Status)
	}
	if row.RunState != components.DeploymentRunning {
		t.Errorf("collapsed row RunState = %q, want running (an inactive-lifecycle SGC with a running session must still roll up to running)", row.RunState)
	}
}

// TestBuildGameRows_Deployments_ConnectAddress guards AC2: the expanded
// row's per-deployment connect address matches the collapsed row's value
// for the same deployment, and falls back to the shared unavailable state
// (never a blank) when unresolvable.
func TestBuildGameRows_Deployments_ConnectAddress(t *testing.T) {
	games := []*manmanpb.Game{{GameId: 1, Name: "G"}}
	configs := []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1}}
	deployments := []*manmanpb.ServerGameConfig{
		{
			ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10,
			PortBindings: []*manmanpb.PortBinding{{HostPort: 25565, Protocol: "TCP"}},
		},
	}
	servers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}}
	sessions := []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: "running"},
	}

	rows := buildGameRows(games, configs, deployments, servers, sessions, nil)
	row := gameRowByID(t, rows, 1)
	dep := row.Overview.Deployments[0]

	if dep.Connect.Unavailable || len(dep.Connect.Addresses) != 1 {
		t.Fatalf("dep.Connect = %+v, want resolvable", dep.Connect)
	}
	if dep.Connect.Addresses[0].Address != row.Connect.Addresses[0].Address {
		t.Errorf("expanded-row Connect %+v diverges from collapsed-row Connect %+v for the same deployment", dep.Connect, row.Connect)
	}

	// Unresolvable case: no host_public_address configured.
	unresolvableServers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: ""}}
	rows = buildGameRows(games, configs, deployments, unresolvableServers, sessions, nil)
	dep = gameRowByID(t, rows, 1).Overview.Deployments[0]
	if !dep.Connect.Unavailable {
		t.Errorf("unresolvable deployment Connect.Unavailable = false, want true (never a blank)")
	}
}

// TestBuildGameRows_Deployments_LinkOuts guards the logs and Actions
// link-outs: Actions renders as a link (decision 8: not reshaped, not
// inlined, not a Config Editor tab), never an inlined panel, and a
// deployment with a session gets a logs link while one with no session
// yet does not claim to have one.
func TestBuildGameRows_Deployments_LinkOuts(t *testing.T) {
	games := []*manmanpb.Game{{GameId: 1, Name: "G"}}
	configs := []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1}}
	deployments := []*manmanpb.ServerGameConfig{
		{ServerGameConfigId: 100, ServerId: 1, GameConfigId: 10, Status: "active"},
		{ServerGameConfigId: 101, ServerId: 1, GameConfigId: 10, Status: "active"},
	}
	servers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: "host-01"}}
	sessions := []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 100, StartedAt: 1000, Status: "running"},
		// SGC 101 has never had a session.
	}

	rows := buildGameRows(games, configs, deployments, servers, sessions, nil)
	depByID := map[int64]pages.GameDeploymentOverview{}
	for _, dep := range gameRowByID(t, rows, 1).Overview.Deployments {
		depByID[dep.SGCID] = dep
	}

	withSession := depByID[100]
	if withSession.LogsURL == "" {
		t.Errorf("deployment with a session has no LogsURL")
	}
	if withSession.ActionsURL == "" {
		t.Errorf("deployment has no ActionsURL")
	}

	withoutSession := depByID[101]
	if withoutSession.LogsURL != "" {
		t.Errorf("deployment with no session has LogsURL = %q, want empty", withoutSession.LogsURL)
	}

	// Render the panel and confirm Actions is an <a> link, never an
	// inlined panel or button-triggered fragment swap.
	var buf bytes.Buffer
	if err := pages.Games(pages.GamesPageData{Games: []pages.GameRow{gameRowByID(t, rows, 1)}}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Games.Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, fmt.Sprintf(`href="%s"`, withSession.ActionsURL)) {
		t.Errorf("Actions link-out for SGC 100 not found as an <a href> in rendered page")
	}
	if !strings.Contains(html, ">Console Commands<") {
		t.Errorf("expected 'Console Commands' link text in rendered page")
	}
	if strings.Contains(html, fmt.Sprintf(`href="%s">Actions<`, withSession.ActionsURL)) {
		t.Errorf("expected no ambiguous '>Actions<' link for ActionsURL")
	}
	if !strings.Contains(html, "View Live Output / Logs") {
		t.Errorf("expected the 'View Live Output / Logs' diagnostics link for the deployment with a session, in rendered page")
	}
	if !strings.Contains(html, "No Logs Available") {
		t.Errorf("expected 'No Logs Available' for the deployment with no session, in rendered page")
	}
}

// TestHandleGames_Deployments_NoAdditionalRequestOnExpand extends #2270's
// NFR7 guard to this task's Deployments section: the section's data is
// baked into the initial /games render (server + config name, connect
// address, Actions availability all come from the same five fleet-wide
// calls handleGames already makes), so a request carrying `expand` must
// not change any RPC call count versus one without it -- expanding a row
// remains a client-side disclosure, never a per-row fetch.
func TestHandleGames_Deployments_NoAdditionalRequestOnExpand(t *testing.T) {
	api := buildFakeGamesData(5)

	code, _ := renderGamesHTTP(t, api, "/games")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	baseline := map[string]int{}
	for k, v := range api.calls {
		baseline[k] = v
	}

	code, _ = renderGamesHTTP(t, api, "/games?expand=3")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for method, before := range baseline {
		after := api.calls[method] - before
		if after != baseline[method] {
			// api.calls accumulates across both renderGamesHTTP calls, so
			// the second render's own contribution must equal the first
			// render's (i.e. issuing `expand` adds nothing beyond a normal
			// render).
			t.Errorf("%s: expand=3 render issued %d calls, non-expand render issued %d (expanding a row must add zero requests)", method, after, before)
		}
	}
}

// assertRowExpanded finds the game-row-<id> container and checks its
// Alpine x-data seed matches want, guarding FR4's client-side-only
// expansion: it asserts against the seeded Alpine state, not against a
// server-rendered visible/hidden class, since expansion itself must be a
// client-side disclosure (games.templ's gameRow / gameRowExpanded).
func assertRowExpanded(t *testing.T, body string, gameID int64, want bool) {
	t.Helper()
	marker := fmt.Sprintf(`id="game-row-%d"`, gameID)
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("no row found for game_id %d in body", gameID)
	}
	// x-data is the next attribute rendered after id on the same element;
	// a small window is enough to find it without spilling into the next
	// row's own id/x-data pair.
	window := body[idx : idx+200]
	wantText := fmt.Sprintf("expanded: %t", want)
	if !strings.Contains(window, wantText) {
		t.Errorf("game_id %d row: expected Alpine seed %q near %q, got window %q", gameID, wantText, marker, window)
	}
}

// TestHandleGameDetail_DedicatedLandingPage covers the dedicated game detail
// route (/games/<game_id>): header breadcrumbs and context scoped to that
// specific game, status and connect info, deployments with action controls,
// configurations, and isolation from other games in the catalog.
func TestHandleGameDetail_DedicatedLandingPage(t *testing.T) {
	api := buildFakeGamesData(3) // Game-000 (ID 1), Game-001 (ID 2), Game-002 (ID 3)

	code, body := renderGameDetailHTTP(t, api, "/games/1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	// Page header and breadcrumbs scoped to Game-000
	if !strings.Contains(body, "Game-000") {
		t.Errorf("expected page to contain game name %q, got body: %s", "Game-000", body)
	}
	if !strings.Contains(body, "Dashboard") {
		t.Errorf("expected breadcrumbs to contain Dashboard, got body: %s", body)
	}
	if !strings.Contains(body, "Games") {
		t.Errorf("expected breadcrumbs to contain Games, got body: %s", body)
	}

	// Status badge and connect address
	if !strings.Contains(body, "running") {
		t.Errorf("expected status badge 'running' in header, got body: %s", body)
	}
	if !strings.Contains(body, "host-01:25000") {
		t.Errorf("expected connect address 'host-01:25000' in header, got body: %s", body)
	}

	// Deployments section with actions
	if !strings.Contains(body, "Deployments") {
		t.Errorf("expected Deployments section heading, got body: %s", body)
	}
	if !strings.Contains(body, "Config-000 on server 1") {
		t.Errorf("expected deployment display name in table, got body: %s", body)
	}
	if !strings.Contains(body, "Restart") {
		t.Errorf("expected Restart action button in deployment row, got body: %s", body)
	}

	// Configurations section
	if !strings.Contains(body, "Configurations") {
		t.Errorf("expected Configurations section heading, got body: %s", body)
	}
	if !strings.Contains(body, "Config-000") {
		t.Errorf("expected Config-000 in configurations table, got body: %s", body)
	}

	// Workshop Libraries section placeholder
	if !strings.Contains(body, "game-workshop-1") {
		t.Errorf("expected workshop libraries container for game 1, got body: %s", body)
	}

	// Isolated view: other games are NOT visible
	if strings.Contains(body, "Game-001") {
		t.Errorf("expected Game-001 not to be present on dedicated Game-000 page, got body: %s", body)
	}
	if strings.Contains(body, "Game-002") {
		t.Errorf("expected Game-002 not to be present on dedicated Game-000 page, got body: %s", body)
	}
}

// TestHandleGames_RowDirectLinksToGameDetail guards the navigation update:
// clicking a game card on the /games catalog navigates directly to /games/<game_id>.
func TestHandleGames_RowDirectLinksToGameDetail(t *testing.T) {
	api := buildFakeGamesData(2)

	code, body := renderGamesHTTP(t, api, "/games")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	if !strings.Contains(body, `href="/games/1"`) {
		t.Errorf("expected game row to link directly to /games/1, got body: %s", body)
	}
	if !strings.Contains(body, `href="/games/2"`) {
		t.Errorf("expected game row to link directly to /games/2, got body: %s", body)
	}
}

func newGamesDetailTestApp(api *fakeGamesAPIClient) *App {
	return &App{grpc: &ControlClient{api: api, workshop: api}}
}

func TestHandleGameDetail_OverviewHeroCard(t *testing.T) {
	api := buildFakeGamesData(1)
	app := newGamesDetailTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/games/1", nil)
	w := httptest.NewRecorder()
	app.handleGameDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	// Prominent hero card header
	if !strings.Contains(body, "Daily Ops Overview") {
		t.Errorf("expected 'Daily Ops Overview' hero card, got: %s", body)
	}
	// High-visibility status badge
	if !strings.Contains(body, "ONLINE") {
		t.Errorf("expected ONLINE badge, got: %s", body)
	}
	// Runtime / Uptime counter
	if !strings.Contains(body, "Runtime / Uptime") {
		t.Errorf("expected Runtime / Uptime section, got: %s", body)
	}
	// Connection info
	if !strings.Contains(body, "host-01:25000") {
		t.Errorf("expected connect address host-01:25000, got: %s", body)
	}
	// Action bar with active Stop and Restart, disabled Start
	if !strings.Contains(body, "Stop") || !strings.Contains(body, "Restart") {
		t.Errorf("expected Stop and Restart buttons, got: %s", body)
	}
	// One-click diagnostics button linking to live session
	if !strings.Contains(body, "/sessions/1") || !strings.Contains(body, "View Live Output / Logs") {
		t.Errorf("expected View Live Output / Logs linking to /sessions/1, got: %s", body)
	}
}

func TestHandleGameOverview_GET(t *testing.T) {
	api := buildFakeGamesData(1)
	app := newGamesDetailTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/games/1/overview", nil)
	w := httptest.NewRecorder()
	app.handleGameDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="daily-ops-overview-1"`) {
		t.Errorf("expected overview fragment with id='daily-ops-overview-1' (scoped per game_id), got: %s", body)
	}
	if !strings.Contains(body, "ONLINE") {
		t.Errorf("expected ONLINE badge, got: %s", body)
	}
}

func TestHandleGameOverview_Action_Start(t *testing.T) {
	api := buildFakeGamesData(1)
	api.sessions[0].Status = "stopped"
	app := newGamesDetailTestApp(api)

	form := strings.NewReader("sgc_id=1&action=start")
	req := httptest.NewRequest(http.MethodPost, "/games/1/overview/action", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handleGameDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if api.calls["StartSession"] != 1 {
		t.Errorf("expected 1 StartSession call, got: %d", api.calls["StartSession"])
	}
}

func TestHandleGameOverview_Action_Stop(t *testing.T) {
	api := buildFakeGamesData(1)
	api.sessions[0].Status = "running"
	app := newGamesDetailTestApp(api)

	form := strings.NewReader("sgc_id=1&action=stop")
	req := httptest.NewRequest(http.MethodPost, "/games/1/overview/action", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handleGameDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if api.calls["StopSession"] != 1 {
		t.Errorf("expected 1 StopSession call, got: %d", api.calls["StopSession"])
	}
}

func TestHandleGameOverview_Action_Restart(t *testing.T) {
	api := buildFakeGamesData(1)
	api.sessions[0].Status = "running"
	app := newGamesDetailTestApp(api)

	form := strings.NewReader("sgc_id=1&action=restart")
	req := httptest.NewRequest(http.MethodPost, "/games/1/overview/action", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handleGameDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if api.calls["RestartDeployment"] != 1 {
		t.Errorf("expected 1 RestartDeployment call, got: %d", api.calls["RestartDeployment"])
	}
}

func TestHandleGameOverview_Action_NonHTMX_Redirect(t *testing.T) {
	api := buildFakeGamesData(1)
	api.sessions[0].Status = "running"
	app := newGamesDetailTestApp(api)

	form := strings.NewReader("sgc_id=1&action=restart")
	req := httptest.NewRequest(http.MethodPost, "/games/1/overview/action", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleGameDetail(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 See Other", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/games/1" {
		t.Errorf("Location = %q, want /games/1", loc)
	}
}

func TestHandleGameDetail_NotFound(t *testing.T) {
	api := buildFakeGamesData(1)
	code, _ := renderGameDetailHTTP(t, api, "/games/999", true)
	if code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent game, got %d", code)
	}
}

// TestHandleGameDetail_TabbedLayout_Admin guards the post-restructure tab
// set: "Configuration" is no longer its own tab (its content folded into
// Overview's collapsed View More section, id="game-detail-view-more") --
// only Overview, Console & Logs, and Advanced remain as tabs for admins.
func TestHandleGameDetail_TabbedLayout_Admin(t *testing.T) {
	api := buildFakeGamesData(1)
	code, body := renderGameDetailHTTP(t, api, "/games/1", true)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	for _, tab := range []string{"Overview", "Console &amp; Logs", "Advanced"} {
		if !strings.Contains(body, tab) {
			t.Errorf("expected tab %q in response, got body: %s", tab, body)
		}
	}
	if strings.Contains(body, `id="tab-btn-configuration"`) {
		t.Errorf("Configuration tab button must no longer exist, got body: %s", body)
	}
	if strings.Contains(body, `id="tab-panel-configuration"`) {
		t.Errorf("Configuration tab panel must no longer exist, got body: %s", body)
	}

	for _, panel := range []string{"tab-panel-overview", "tab-panel-logs", "tab-panel-advanced"} {
		if !strings.Contains(body, `id="`+panel+`"`) {
			t.Errorf("expected panel %q in response, got body: %s", panel, body)
		}
	}
	if !strings.Contains(body, `id="game-detail-view-more"`) {
		t.Errorf("expected the collapsed View More section for admin, got body: %s", body)
	}
}

func TestHandleGameDetail_TabbedLayout_NonAdmin(t *testing.T) {
	api := buildFakeGamesData(1)
	code, body := renderGameDetailHTTP(t, api, "/games/1", false)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	if !strings.Contains(body, "Overview") {
		t.Errorf("expected Overview tab for non-admin, got body: %s", body)
	}
	if !strings.Contains(body, "Console &amp; Logs") {
		t.Errorf("expected Console & Logs tab for non-admin, got body: %s", body)
	}

	if strings.Contains(body, `id="tab-btn-configuration"`) {
		t.Errorf("non-admin must NOT see Configuration tab button, got body: %s", body)
	}
	if strings.Contains(body, `id="tab-btn-advanced"`) {
		t.Errorf("non-admin must NOT see Advanced tab button, got body: %s", body)
	}
	if strings.Contains(body, `id="tab-panel-configuration"`) {
		t.Errorf("non-admin must NOT see Configuration tab panel, got body: %s", body)
	}
	if strings.Contains(body, `id="tab-panel-advanced"`) {
		t.Errorf("non-admin must NOT see Advanced tab panel, got body: %s", body)
	}
}

// TestHandleGameDetail_SeparatesLowFrequencyActions guards the restructure:
// Overview shows the Daily Ops Overview panel directly, and Advanced-only
// controls (Deploy, Danger Zone) never leak into it, but the collapsed
// View More section -- which now lives inside the Overview tab panel,
// rather than a separate Configuration tab -- still carries Edit
// Configuration.
func TestHandleGameDetail_SeparatesLowFrequencyActions(t *testing.T) {
	api := buildFakeGamesData(1)
	code, body := renderGameDetailHTTP(t, api, "/games/1", true)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	startIdx := strings.Index(body, `id="tab-panel-overview"`)
	endIdx := strings.Index(body, `id="tab-panel-logs"`)
	if startIdx == -1 || endIdx == -1 {
		t.Fatalf("could not find tab panels in body")
	}
	overview := body[startIdx:endIdx]

	if !strings.Contains(overview, "Daily Ops Overview") {
		t.Errorf("expected Daily Ops Overview on overview, got: %s", overview)
	}
	if strings.Contains(overview, "Deploy to Server") {
		t.Errorf("Overview must NOT contain 'Deploy to Server' (Advanced tab only), got: %s", overview)
	}
	if strings.Contains(overview, "Danger Zone") {
		t.Errorf("Overview must NOT contain 'Danger Zone' (Advanced tab only), got: %s", overview)
	}

	viewMoreStart := strings.Index(overview, `id="game-detail-view-more"`)
	if viewMoreStart == -1 {
		t.Fatalf("could not find game-detail-view-more inside the Overview panel")
	}
	if !strings.Contains(overview[viewMoreStart:], "Edit Configuration") {
		t.Errorf("View More section must contain 'Edit Configuration', got: %s", overview[viewMoreStart:])
	}

	advancedStart := strings.Index(body, `id="tab-panel-advanced"`)
	if advancedStart == -1 {
		t.Fatalf("could not find tab-panel-advanced in body")
	}
	advancedPanel := body[advancedStart:]
	if !strings.Contains(advancedPanel, "Deploy to Server") {
		t.Errorf("Advanced tab must contain 'Deploy to Server', got: %s", advancedPanel)
	}
}
