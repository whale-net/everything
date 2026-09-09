package main

import (
	"bytes"
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

	rows := buildGameRows(games, configs, deployments, servers, sessions)

	alpha := gameRowByID(t, rows, 1)
	if len(alpha.Deployments) != 2 {
		t.Fatalf("Alpha Deployments = %d rows, want 2 (100 and 101): %+v", len(alpha.Deployments), alpha.Deployments)
	}
	gotSGCIDs := map[int64]bool{}
	for _, dep := range alpha.Deployments {
		gotSGCIDs[dep.Row.ServerGameConfigID] = true
		if dep.Row.ServerGameConfigID == 200 {
			t.Errorf("Alpha's Deployments section contains SGC 200, which belongs to Beta (leaked across games)")
		}
	}
	for _, want := range []int64{100, 101} {
		if !gotSGCIDs[want] {
			t.Errorf("Alpha Deployments missing SGC %d: got %+v", want, alpha.Deployments)
		}
	}

	beta := gameRowByID(t, rows, 2)
	if len(beta.Deployments) != 1 || beta.Deployments[0].Row.ServerGameConfigID != 200 {
		t.Fatalf("Beta Deployments = %+v, want exactly [SGC 200]", beta.Deployments)
	}
}

// TestBuildGameRows_Deployments_AntiDrift is FR6's central anti-drift
// guard: the Games row's control availability must be identical to what
// /sessions renders for the same deployment state. Both surfaces reuse
// components.ComputeDeploymentActions(latest) verbatim (buildGameDeploymentRow
// here, buildDeploymentRowData on /sessions), so this test drives both
// paths -- buildGameRows and a direct components.ComputeDeploymentActions
// call against the identical fixture session -- and asserts they agree,
// across running, stopped, and every transitional/error state
// ComputeDeploymentActions' own table documents (pending, starting,
// stopping, crashed, lost, and no session at all).
func TestBuildGameRows_Deployments_AntiDrift(t *testing.T) {
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
			)
			row := gameRowByID(t, rows, 1)
			if len(row.Deployments) != 1 {
				t.Fatalf("Deployments = %d rows, want 1", len(row.Deployments))
			}
			got := row.Deployments[0].Row.Actions

			// The independent, same-fixture derivation: exactly what
			// buildDeploymentRowData computes for /sessions, called
			// directly against the identical latest session.
			want := components.ComputeDeploymentActions(latest)

			if got != want {
				t.Errorf("state %q: Games row Actions = %+v, /sessions-equivalent Actions = %+v (FR6 anti-drift violation)", tc.sessionStatus, got, want)
			}

			// End-to-end: render the row exactly as the page does
			// (gameDeploymentRow -> DeploymentRow(dep.Row)) and confirm
			// button presence matches want, not just the struct field.
			var buf bytes.Buffer
			if err := pages.DeploymentRow(row.Deployments[0].Row).Render(context.Background(), &buf); err != nil {
				t.Fatalf("DeploymentRow.Render: %v", err)
			}
			html := buf.String()
			if strings.Contains(html, ">Start<") != want.CanStart {
				t.Errorf("state %q: rendered Start button presence = %v, want %v", tc.sessionStatus, strings.Contains(html, ">Start<"), want.CanStart)
			}
			if strings.Contains(html, ">Stop<") != want.CanStop {
				t.Errorf("state %q: rendered Stop button presence = %v, want %v", tc.sessionStatus, strings.Contains(html, ">Stop<"), want.CanStop)
			}
			if strings.Contains(html, ">Restart<") != want.CanRestart {
				t.Errorf("state %q: rendered Restart button presence = %v, want %v", tc.sessionStatus, strings.Contains(html, ">Restart<"), want.CanRestart)
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

	rows := buildGameRows(games, configs, deployments, servers, sessions)
	row := gameRowByID(t, rows, 1)
	if len(row.Deployments) != 1 {
		t.Fatalf("Deployments = %d rows, want 1", len(row.Deployments))
	}
	dep := row.Deployments[0]
	if dep.Row.SGCStatus != "inactive" {
		t.Fatalf("fixture setup: SGCStatus = %q, want inactive", dep.Row.SGCStatus)
	}
	if dep.Row.LatestSession == nil || components.ComputeDeploymentStatus(dep.Row.LatestSession) != components.DeploymentRunning {
		t.Errorf("deployment with inactive SGC.status but a running latest session must show running: LatestSession = %+v", dep.Row.LatestSession)
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

	rows := buildGameRows(games, configs, deployments, servers, sessions)
	row := gameRowByID(t, rows, 1)
	dep := row.Deployments[0]

	if dep.Connect.Unavailable || len(dep.Connect.Addresses) != 1 {
		t.Fatalf("dep.Connect = %+v, want resolvable", dep.Connect)
	}
	if dep.Connect.Addresses[0].Address != row.Connect.Addresses[0].Address {
		t.Errorf("expanded-row Connect %+v diverges from collapsed-row Connect %+v for the same deployment", dep.Connect, row.Connect)
	}

	// Unresolvable case: no host_public_address configured.
	unresolvableServers := []*manmanpb.Server{{ServerId: 1, HostPublicAddress: ""}}
	rows = buildGameRows(games, configs, deployments, unresolvableServers, sessions)
	dep = gameRowByID(t, rows, 1).Deployments[0]
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

	rows := buildGameRows(games, configs, deployments, servers, sessions)
	depByID := map[int64]pages.GameDeploymentRow{}
	for _, dep := range gameRowByID(t, rows, 1).Deployments {
		depByID[dep.Row.ServerGameConfigID] = dep
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

	// Render the section and confirm Actions is an <a> link, never an
	// inlined panel or button-triggered fragment swap.
	var buf bytes.Buffer
	if err := pages.Games(pages.GamesPageData{Games: []pages.GameRow{gameRowByID(t, rows, 1)}}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Games.Render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, fmt.Sprintf(`href="%s"`, withSession.ActionsURL)) {
		t.Errorf("Actions link-out for SGC 100 not found as an <a href> in rendered page")
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
