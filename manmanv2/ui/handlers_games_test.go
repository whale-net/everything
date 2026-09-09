package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	"google.golang.org/grpc"
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
// ListGameConfigs, ListServerGameConfigs, ListServers, ListSessions. Any
// call to an un-overridden ManManAPIClient method panics on the nil
// embedded interface, deliberately -- see handlers_sgc_test.go's
// fakeManManAPIClient / handlers_home_test.go's fakeDashboardAPIClient for
// the same convention.
type fakeGamesAPIClient struct {
	manmanpb.ManManAPIClient

	games       []*manmanpb.Game
	configs     []*manmanpb.GameConfig
	deployments []*manmanpb.ServerGameConfig
	servers     []*manmanpb.Server
	sessions    []*manmanpb.Session

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
	return &manmanpb.ListSessionsResponse{Sessions: f.sessions}, nil
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

// TestHandleGames_NFR7_ConstantCallCount is the headline test (NFR7): the
// number of RPC calls handleGames issues must not grow with the number of
// games, configs, or deployments rendered. Two sizes are compared directly
// -- a regression that added a call inside a per-game or per-deployment
// loop would make the 20-game call counts strictly larger than the 2-game
// ones, while today's join (buildGameRows, issuing zero RPCs itself) keeps
// them identical.
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

	for _, method := range []string{"ListGames", "ListGameConfigs", "ListServerGameConfigs", "ListServers", "ListSessions"} {
		if small.calls[method] != large.calls[method] {
			t.Errorf("%s call count grew with fleet size: 2 games -> %d calls, 20 games -> %d calls (NFR7 requires a constant count)", method, small.calls[method], large.calls[method])
		}
		if small.calls[method] == 0 {
			t.Errorf("%s was never called", method)
		}
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

	rows := buildGameRows(games, configs, deployments, servers, sessions)
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

	rows := buildGameRows(games, configs, deployments, servers, sessions)

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
		rows := buildGameRows(makeGames(order), nil, nil, nil, nil)
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

// TestHandleGames_WD1WD6_NoPlayerCountOrLastPlayed guards WD1 and WD6: the
// collapsed row must never render a player count, a "last played" value,
// or an aggregate rollup string -- documented, intentional divergences
// from 90-v2-games.
func TestHandleGames_WD1WD6_NoPlayerCountOrLastPlayed(t *testing.T) {
	api := buildFakeGamesData(3)
	code, body := renderGamesHTTP(t, api, "/games")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	lower := strings.ToLower(body)
	for _, forbidden := range []string{"player", "last played"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("rendered Games page contains forbidden text %q (WD1/WD6)", forbidden)
		}
	}
}

// TestHandleGames_FR2_NoSGCTerminology guards FR2: no user-facing display
// text on the Games page may contain "SGC" or "server game config".
func TestHandleGames_FR2_NoSGCTerminology(t *testing.T) {
	api := buildFakeGamesData(3)
	code, body := renderGamesHTTP(t, api, "/games")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("rendered Games page contains %q (FR2 forbids SGC terminology in display text)", "sgc")
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
