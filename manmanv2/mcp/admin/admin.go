// Package admin holds the admin-only MCP tools: game catalog edits and host
// drain/undrain. Every tool is confirmation-gated and idempotency-wrapped.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"

	"github.com/whale-net/everything/manmanv2/mcp/server"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// API is the slice of the control API the admin tools call.
type API interface {
	GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error)
	CreateGame(ctx context.Context, in *manmanpb.CreateGameRequest, opts ...grpc.CallOption) (*manmanpb.CreateGameResponse, error)
	UpdateGame(ctx context.Context, in *manmanpb.UpdateGameRequest, opts ...grpc.CallOption) (*manmanpb.UpdateGameResponse, error)
	DeleteGame(ctx context.Context, in *manmanpb.DeleteGameRequest, opts ...grpc.CallOption) (*manmanpb.DeleteGameResponse, error)
	ListGameConfigs(ctx context.Context, in *manmanpb.ListGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigsResponse, error)
	GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error)
	ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error)
	DrainServer(ctx context.Context, in *manmanpb.DrainServerRequest, opts ...grpc.CallOption) (*manmanpb.DrainServerResponse, error)
	UndrainServer(ctx context.Context, in *manmanpb.UndrainServerRequest, opts ...grpc.CallOption) (*manmanpb.UndrainServerResponse, error)
}

// Tool names.
const (
	CreateGame    = "create_game"
	UpdateGame    = "update_game"
	DeleteGame    = "delete_game"
	DrainServer   = "drain_server"
	UndrainServer = "undrain_server"
)

// Tools returns the registry entries; update/delete/drain tools record a
// before-image through the audit Snapshot hook.
func Tools(api API) []server.Tool {
	w := func(name, target string) server.Tool {
		return server.Tool{Name: name, MinPersona: server.PersonaAdmin, TargetArg: target, Write: true}
	}
	create := w(CreateGame, "")
	update := w(UpdateGame, "game_id")
	update.Snapshot = func(c server.CallContext, a json.RawMessage) (any, error) { return gameSnapshot(c, api, a) }
	del := w(DeleteGame, "game_id")
	del.Snapshot = update.Snapshot
	drain := w(DrainServer, "server_id")
	drain.Snapshot = func(c server.CallContext, a json.RawMessage) (any, error) { return serverSnapshot(c, api, a) }
	undrain := w(UndrainServer, "server_id")
	undrain.Snapshot = drain.Snapshot
	return []server.Tool{create, update, del, drain, undrain}
}

// Register adds the admin tools to srv, each behind gate.
func Register(srv *mcp.Server, api API, gate *server.Gate) {
	for _, gt := range gatedTools(api) {
		srv.AddTool(&mcp.Tool{Name: gt.tool.Name, Description: gt.desc, InputSchema: gt.schema}, gate.Handler(gt.tool))
	}
}

type described struct {
	tool   server.GatedTool
	desc   string
	schema map[string]any
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func schema(required []string, props map[string]any) map[string]any {
	props[server.ArgConfirmationToken] = prop("string", "Token from the preview call; supplies confirmation.")
	props[server.ArgIdempotencyKey] = prop("string", "Optional key making retries safe.")
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func gatedTools(api API) []described {
	gameProps := func() map[string]any {
		return map[string]any{
			"name":            prop("string", "Game display name"),
			"steam_app_id":    prop("string", "Steam app id"),
			"genre":           prop("string", "Genre"),
			"publisher":       prop("string", "Publisher"),
			"default_players": prop("integer", "Default player count"),
			"max_players":     prop("integer", "Maximum player count"),
		}
	}
	idProps := func(k string) map[string]any { return map[string]any{k: prop("integer", k)} }
	return []described{
		{createGame(api), "Create a game in the catalog (admin; two-call confirmation).", schema([]string{"name"}, gameProps())},
		{updateGame(api), "Update a game's fields (admin; preview shows field diffs, then confirm).", schema([]string{"game_id"}, mergeProps(idProps("game_id"), gameProps()))},
		{deleteGame(api), "Delete a game (admin; preview names the configs removed with it, then confirm).", schema([]string{"game_id"}, idProps("game_id"))},
		{drainServer(api), "Drain a host (admin; preview names the host and its running deployments, then confirm).", schema([]string{"server_id"}, idProps("server_id"))},
		{undrainServer(api), "Return a drained host to schedulable (admin; two-call confirmation).", schema([]string{"server_id"}, idProps("server_id"))},
	}
}

func mergeProps(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

type gameArgs struct {
	GameID         int64   `json:"game_id"`
	Name           *string `json:"name"`
	SteamAppID     *string `json:"steam_app_id"`
	Genre          *string `json:"genre"`
	Publisher      *string `json:"publisher"`
	DefaultPlayers *int32  `json:"default_players"`
	MaxPlayers     *int32  `json:"max_players"`
}

type serverArgs struct {
	ServerID int64 `json:"server_id"`
}

func parse(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	return json.Unmarshal(raw, v)
}

// gameFields flattens a game to the editable fields the diff compares.
func gameFields(g *manmanpb.Game) map[string]any {
	m := g.GetMetadata()
	return map[string]any{
		"name": g.GetName(), "steam_app_id": g.GetSteamAppId(), "genre": m.GetGenre(),
		"publisher": m.GetPublisher(), "default_players": m.GetDefaultPlayers(), "max_players": m.GetMaxPlayers(),
	}
}

func (a gameArgs) overrides() map[string]any {
	o := map[string]any{}
	set := func(k string, v any, ok bool) {
		if ok {
			o[k] = v
		}
	}
	if a.Name != nil {
		set("name", *a.Name, true)
	}
	if a.SteamAppID != nil {
		set("steam_app_id", *a.SteamAppID, true)
	}
	if a.Genre != nil {
		set("genre", *a.Genre, true)
	}
	if a.Publisher != nil {
		set("publisher", *a.Publisher, true)
	}
	if a.DefaultPlayers != nil {
		set("default_players", *a.DefaultPlayers, true)
	}
	if a.MaxPlayers != nil {
		set("max_players", *a.MaxPlayers, true)
	}
	return o
}

func (a gameArgs) metadata(cur *manmanpb.GameMetadata) *manmanpb.GameMetadata {
	m := &manmanpb.GameMetadata{}
	if cur != nil {
		m.Genre, m.Publisher, m.DefaultPlayers, m.MaxPlayers = cur.Genre, cur.Publisher, cur.DefaultPlayers, cur.MaxPlayers
		m.Links, m.Tags = cur.Links, cur.Tags
	}
	if a.Genre != nil {
		m.Genre = *a.Genre
	}
	if a.Publisher != nil {
		m.Publisher = *a.Publisher
	}
	if a.DefaultPlayers != nil {
		m.DefaultPlayers = *a.DefaultPlayers
	}
	if a.MaxPlayers != nil {
		m.MaxPlayers = *a.MaxPlayers
	}
	return m
}

type fieldDiff struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func getGame(ctx context.Context, api API, id int64) (*manmanpb.Game, error) {
	r, err := api.GetGame(ctx, &manmanpb.GetGameRequest{GameId: id})
	if err != nil {
		return nil, fmt.Errorf("game %d: %w", id, err)
	}
	if r.GetGame() == nil {
		return nil, fmt.Errorf("game %d not found", id)
	}
	return r.Game, nil
}

func gameSnapshot(c server.CallContext, api API, raw json.RawMessage) (any, error) {
	var a gameArgs
	if err := parse(raw, &a); err != nil {
		return nil, err
	}
	g, err := getGame(c, api, a.GameID)
	if err != nil {
		return nil, err
	}
	return gameFields(g), nil
}

func getServer(ctx context.Context, api API, id int64) (*manmanpb.Server, error) {
	r, err := api.GetServer(ctx, &manmanpb.GetServerRequest{ServerId: id})
	if err != nil {
		return nil, fmt.Errorf("host %d: %w", id, err)
	}
	if r.GetServer() == nil {
		return nil, fmt.Errorf("host %d not found", id)
	}
	return r.Server, nil
}

func serverSnapshot(c server.CallContext, api API, raw json.RawMessage) (any, error) {
	var a serverArgs
	if err := parse(raw, &a); err != nil {
		return nil, err
	}
	s, err := getServer(c, api, a.ServerID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": s.Name, "drain_state": s.DrainState}, nil
}

func createGame(api API) server.GatedTool {
	return server.GatedTool{
		Name: CreateGame,
		Preview: func(_ context.Context, raw json.RawMessage) (any, string, error) {
			var a gameArgs
			if err := parse(raw, &a); err != nil {
				return nil, "", err
			}
			if a.Name == nil || *a.Name == "" {
				return nil, "", fmt.Errorf("name is required")
			}
			return map[string]any{"effect": "creates a game", "game": a.overrides()}, "", nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a gameArgs
			if err := parse(raw, &a); err != nil {
				return nil, err
			}
			req := &manmanpb.CreateGameRequest{Name: *a.Name, Metadata: a.metadata(nil)}
			if a.SteamAppID != nil {
				req.SteamAppId = *a.SteamAppID
			}
			r, err := api.CreateGame(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("create game: %w", err)
			}
			return map[string]any{"game_id": r.GetGame().GetGameId(), "game": gameFields(r.GetGame())}, nil
		},
	}
}

func updateGame(api API) server.GatedTool {
	return server.GatedTool{
		Name: UpdateGame,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			var a gameArgs
			if err := parse(raw, &a); err != nil {
				return nil, "", err
			}
			g, err := getGame(ctx, api, a.GameID)
			if err != nil {
				return nil, "", err
			}
			cur, diffs := gameFields(g), []fieldDiff{}
			ov := a.overrides()
			keys := make([]string, 0, len(ov))
			for k := range ov {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !reflect.DeepEqual(cur[k], ov[k]) {
					diffs = append(diffs, fieldDiff{k, cur[k], ov[k]})
				}
			}
			return map[string]any{"effect": "updates game", "game_id": a.GameID, "diffs": diffs}, fingerprint(cur), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a gameArgs
			if err := parse(raw, &a); err != nil {
				return nil, err
			}
			g, err := getGame(ctx, api, a.GameID)
			if err != nil {
				return nil, err
			}
			req := &manmanpb.UpdateGameRequest{GameId: a.GameID}
			if a.Name != nil {
				req.Name = *a.Name
				req.UpdatePaths = append(req.UpdatePaths, "name")
			}
			if a.SteamAppID != nil {
				req.SteamAppId = *a.SteamAppID
				req.UpdatePaths = append(req.UpdatePaths, "steam_app_id")
			}
			if a.Genre != nil || a.Publisher != nil || a.DefaultPlayers != nil || a.MaxPlayers != nil {
				req.Metadata = a.metadata(g.Metadata)
				req.UpdatePaths = append(req.UpdatePaths, "metadata")
			}
			if len(req.UpdatePaths) == 0 {
				return nil, fmt.Errorf("no fields to update")
			}
			r, err := api.UpdateGame(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("update game: %w", err)
			}
			return map[string]any{"game_id": a.GameID, "game": gameFields(r.GetGame())}, nil
		},
	}
}

func deleteGame(api API) server.GatedTool {
	return server.GatedTool{
		Name: DeleteGame,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			var a gameArgs
			if err := parse(raw, &a); err != nil {
				return nil, "", err
			}
			g, err := getGame(ctx, api, a.GameID)
			if err != nil {
				return nil, "", err
			}
			cfgs := []map[string]any{}
			for tok := ""; ; {
				r, err := api.ListGameConfigs(ctx, &manmanpb.ListGameConfigsRequest{GameId: a.GameID, PageSize: 100, PageToken: tok})
				if err != nil {
					return nil, "", fmt.Errorf("list game configs: %w", err)
				}
				for _, c := range r.Configs {
					cfgs = append(cfgs, map[string]any{"config_id": c.ConfigId, "name": c.Name})
				}
				if tok = r.NextPageToken; tok == "" {
					break
				}
			}
			fields := gameFields(g)
			return map[string]any{"effect": "deletes the game and may orphan its configs", "game_id": a.GameID, "game": fields, "game_configs": cfgs},
				fingerprint([]any{fields, cfgs}), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a gameArgs
			if err := parse(raw, &a); err != nil {
				return nil, err
			}
			if _, err := api.DeleteGame(ctx, &manmanpb.DeleteGameRequest{GameId: a.GameID}); err != nil {
				return nil, fmt.Errorf("delete game: %w", err)
			}
			return map[string]any{"game_id": a.GameID, "deleted": true}, nil
		},
	}
}

func hostPreview(api API, effect string) func(context.Context, json.RawMessage) (any, string, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, string, error) {
		var a serverArgs
		if err := parse(raw, &a); err != nil {
			return nil, "", err
		}
		s, err := getServer(ctx, api, a.ServerID)
		if err != nil {
			return nil, "", err
		}
		running := []map[string]any{}
		for tok := ""; ; {
			r, err := api.ListSessions(ctx, &manmanpb.ListSessionsRequest{ServerId: a.ServerID, LiveOnly: true, PageSize: 100, PageToken: tok})
			if err != nil {
				return nil, "", fmt.Errorf("list sessions: %w", err)
			}
			for _, ss := range r.Sessions {
				running = append(running, map[string]any{"deployment_id": ss.ServerGameConfigId, "session_id": ss.SessionId, "status": ss.Status})
			}
			if tok = r.NextPageToken; tok == "" {
				break
			}
		}
		return map[string]any{"effect": effect, "host": map[string]any{"server_id": s.ServerId, "name": s.Name, "drain_state": s.DrainState}, "running_deployments": running},
			fingerprint([]any{s.Name, s.DrainState}), nil
	}
}

func drainServer(api API) server.GatedTool {
	return server.GatedTool{
		Name:    DrainServer,
		Preview: hostPreview(api, "marks the host draining; running deployments are not restarted"),
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a serverArgs
			if err := parse(raw, &a); err != nil {
				return nil, err
			}
			r, err := api.DrainServer(ctx, &manmanpb.DrainServerRequest{ServerId: a.ServerID})
			if err != nil {
				return nil, fmt.Errorf("drain host: %w", err)
			}
			return map[string]any{"server_id": a.ServerID, "drain_state": r.GetServer().GetDrainState()}, nil
		},
	}
}

func undrainServer(api API) server.GatedTool {
	return server.GatedTool{
		Name:    UndrainServer,
		Preview: hostPreview(api, "returns the host to schedulable; nothing is restarted"),
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var a serverArgs
			if err := parse(raw, &a); err != nil {
				return nil, err
			}
			r, err := api.UndrainServer(ctx, &manmanpb.UndrainServerRequest{ServerId: a.ServerID})
			if err != nil {
				return nil, fmt.Errorf("undrain host: %w", err)
			}
			return map[string]any{"server_id": a.ServerID, "drain_state": r.GetServer().GetDrainState()}, nil
		},
	}
}
