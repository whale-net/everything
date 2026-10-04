package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Registry entries for the lifecycle tools. start_deployment is open to
// gamers but, for them, gated by the per-deployment start allowlist.
var (
	StartDeploymentTool = Tool{Name: "start_deployment", MinPersona: PersonaGamer, TargetArg: "deployment_id", Write: true}
	StopDeploymentTool  = Tool{Name: "stop_deployment", MinPersona: PersonaServerManager, TargetArg: "deployment_id", Write: true}
	RestartDeployTool   = Tool{Name: "restart_deployment", MinPersona: PersonaServerManager, TargetArg: "deployment_id", Write: true}
	PendingRestartsTool = Tool{Name: "list_pending_restarts", MinPersona: PersonaServerManager}
)

// LifecycleTools lists the lifecycle tools for registry construction.
var LifecycleTools = []Tool{StartDeploymentTool, StopDeploymentTool, RestartDeployTool, PendingRestartsTool}

// ErrNotAllowlisted: a gamer tried to start a deployment without a current grant.
var ErrNotAllowlisted = errors.New("permission denied: deployment is not on the gamer start allowlist; nothing was started")

// LifecycleAPI is the subset of the control API the lifecycle tools call.
type LifecycleAPI interface {
	ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error)
	StartSession(ctx context.Context, in *manmanpb.StartSessionRequest, opts ...grpc.CallOption) (*manmanpb.StartSessionResponse, error)
	StopSession(ctx context.Context, in *manmanpb.StopSessionRequest, opts ...grpc.CallOption) (*manmanpb.StopSessionResponse, error)
	RestartDeployment(ctx context.Context, in *manmanpb.RestartDeploymentRequest, opts ...grpc.CallOption) (*manmanpb.RestartDeploymentResponse, error)
	ListPendingRestarts(ctx context.Context, in *manmanpb.ListPendingRestartsRequest, opts ...grpc.CallOption) (*manmanpb.ListPendingRestartsResponse, error)
}

// StartAllowlist reports whether a deployment currently has a gamer start grant.
type StartAllowlist interface {
	Allowed(ctx context.Context, deploymentID int64) (bool, error)
}

// SQLStartAllowlist reads mcp_gamer_start_allowlist; only rows with
// valid_to IS NULL count.
type SQLStartAllowlist struct{ DB *sql.DB }

func (s SQLStartAllowlist) Allowed(ctx context.Context, deploymentID int64) (bool, error) {
	var ok bool
	err := s.DB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM mcp_gamer_start_allowlist WHERE deployment_id = $1 AND valid_to IS NULL)`,
		deploymentID).Scan(&ok)
	return ok, err
}

type deploymentIn struct {
	DeploymentID   int64  `json:"deployment_id" jsonschema:"deployment id"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional key making retries of this call safe"`
}

type startOut struct {
	SessionID      int64  `json:"session_id"`
	Status         string `json:"status"`
	AlreadyRunning bool   `json:"already_running"`
}

type sessionPreview struct {
	DeploymentID int64  `json:"deployment_id"`
	Action       string `json:"action"`
	SessionID    int64  `json:"session_id,omitempty"`
	Status       string `json:"status,omitempty"`
	Note         string `json:"note,omitempty"`
}

type pendingRestartOut struct {
	DeploymentID    int64 `json:"deployment_id"`
	PendingID       int64 `json:"pending_restart_id"`
	GatingSessionID int64 `json:"gating_session_id,omitempty"`
	CreatedAtUnix   int64 `json:"created_at_unix"`
}

type pendingRestartsOut struct {
	Deployments []pendingRestartOut `json:"deployments"`
}

type lifecycle struct {
	api  LifecycleAPI
	allp StartAllowlist
}

// AddLifecycleTools registers start/stop/restart_deployment and
// list_pending_restarts on srv. stop and restart go through gate.
func AddLifecycleTools(srv *mcp.Server, api LifecycleAPI, allowlist StartAllowlist, gate *Gate) {
	l := &lifecycle{api: api, allp: allowlist}
	mcp.AddTool(srv, &mcp.Tool{Name: StartDeploymentTool.Name,
		Description: "Start a session for a deployment. If one is already running it is returned and no second is started."}, l.start)
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"deployment_id":      map[string]any{"type": "integer", "description": "deployment id"},
		"confirmation_token": map[string]any{"type": "string", "description": "token from the preview call; omit on the first call"},
		"idempotency_key":    map[string]any{"type": "string", "description": "optional key making retries safe"},
	}, "required": []string{"deployment_id"}}
	srv.AddTool(&mcp.Tool{Name: StopDeploymentTool.Name, InputSchema: schema,
		Description: "Stop a deployment's running session. The first call previews and returns a confirmation_token; repeat with it to apply."},
		gate.Handler(l.stopGated()))
	srv.AddTool(&mcp.Tool{Name: RestartDeployTool.Name, InputSchema: schema,
		Description: "Restart a deployment. The first call previews and returns a confirmation_token; repeat with it to apply."},
		gate.Handler(l.restartGated()))
	mcp.AddTool(srv, &mcp.Tool{Name: PendingRestartsTool.Name,
		Description: "List deployments whose configuration changed since their session started and still await a restart."}, l.pending)
}

// live returns the deployment's live session, or nil.
func (l *lifecycle) live(ctx context.Context, id int64) (*manmanpb.Session, error) {
	resp, err := l.api.ListSessions(ctx, &manmanpb.ListSessionsRequest{ServerGameConfigId: id, LiveOnly: true, PageSize: 10})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	for _, s := range resp.Sessions {
		if s.ServerGameConfigId == id {
			return s, nil
		}
	}
	return nil, nil
}

func (l *lifecycle) start(ctx context.Context, _ *mcp.CallToolRequest, in deploymentIn) (*mcp.CallToolResult, startOut, error) {
	if c := CallerFromContext(ctx); c != nil && c.Persona < PersonaServerManager {
		ok, err := l.allp.Allowed(ctx, in.DeploymentID)
		if err != nil {
			return nil, startOut{}, fmt.Errorf("checking start allowlist: %w", err)
		}
		if !ok {
			return nil, startOut{}, ErrNotAllowlisted
		}
	}
	if s, err := l.live(ctx, in.DeploymentID); err != nil {
		return nil, startOut{}, err
	} else if s != nil {
		return nil, startOut{SessionID: s.SessionId, Status: s.Status, AlreadyRunning: true}, nil
	}
	resp, err := l.api.StartSession(ctx, &manmanpb.StartSessionRequest{ServerGameConfigId: in.DeploymentID})
	if err != nil {
		return nil, startOut{}, toolErr("start deployment", in.DeploymentID, err)
	}
	return nil, startOut{SessionID: resp.GetSession().GetSessionId(), Status: resp.GetSession().GetStatus()}, nil
}

func parseDeploymentID(args json.RawMessage) (int64, error) {
	var in deploymentIn
	if err := json.Unmarshal(args, &in); err != nil || in.DeploymentID == 0 {
		return 0, errors.New("deployment_id is required")
	}
	return in.DeploymentID, nil
}

func fingerprint(s *manmanpb.Session) string {
	if s == nil {
		return "none"
	}
	return fmt.Sprintf("%d:%s", s.SessionId, s.Status)
}

func (l *lifecycle) stopGated() GatedTool {
	return GatedTool{
		Name: StopDeploymentTool.Name,
		Preview: func(ctx context.Context, args json.RawMessage) (any, string, error) {
			id, err := parseDeploymentID(args)
			if err != nil {
				return nil, "", err
			}
			s, err := l.live(ctx, id)
			if err != nil {
				return nil, "", err
			}
			if s == nil {
				return nil, "", fmt.Errorf("deployment %d has no running session to stop", id)
			}
			return sessionPreview{DeploymentID: id, Action: "stop", SessionID: s.SessionId, Status: s.Status}, fingerprint(s), nil
		},
		Apply: func(ctx context.Context, args json.RawMessage) (any, error) {
			id, err := parseDeploymentID(args)
			if err != nil {
				return nil, err
			}
			s, err := l.live(ctx, id)
			if err != nil {
				return nil, err
			}
			if s == nil {
				return nil, fmt.Errorf("deployment %d has no running session to stop", id)
			}
			resp, err := l.api.StopSession(ctx, &manmanpb.StopSessionRequest{SessionId: s.SessionId})
			if err != nil {
				return nil, toolErr("stop deployment", id, err)
			}
			return startOut{SessionID: s.SessionId, Status: resp.GetSession().GetStatus()}, nil
		},
	}
}

type restartOut struct {
	StoppingSessionID int64 `json:"stopping_session_id,omitempty"`
	StartedSessionID  int64 `json:"started_session_id,omitempty"`
	AlreadyInFlight   bool  `json:"already_in_flight,omitempty"`
}

func (l *lifecycle) restartGated() GatedTool {
	return GatedTool{
		Name: RestartDeployTool.Name,
		Preview: func(ctx context.Context, args json.RawMessage) (any, string, error) {
			id, err := parseDeploymentID(args)
			if err != nil {
				return nil, "", err
			}
			s, err := l.live(ctx, id)
			if err != nil {
				return nil, "", err
			}
			p := sessionPreview{DeploymentID: id, Action: "restart"}
			if s == nil {
				p.Note = "no session is running; restart will start one"
			} else {
				p.SessionID, p.Status = s.SessionId, s.Status
			}
			return p, fingerprint(s), nil
		},
		Apply: func(ctx context.Context, args json.RawMessage) (any, error) {
			id, err := parseDeploymentID(args)
			if err != nil {
				return nil, err
			}
			resp, err := l.api.RestartDeployment(ctx, &manmanpb.RestartDeploymentRequest{ServerGameConfigId: id})
			if err != nil {
				return nil, toolErr("restart deployment", id, err)
			}
			return restartOut{
				StoppingSessionID: resp.GetStoppingSession().GetSessionId(),
				StartedSessionID:  resp.GetStartedSession().GetSessionId(),
				AlreadyInFlight:   resp.GetAlreadyInFlight(),
			}, nil
		},
	}
}

func (l *lifecycle) pending(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, pendingRestartsOut, error) {
	resp, err := l.api.ListPendingRestarts(ctx, &manmanpb.ListPendingRestartsRequest{})
	if err != nil {
		return nil, pendingRestartsOut{}, fmt.Errorf("list pending restarts: %w", err)
	}
	out := pendingRestartsOut{Deployments: []pendingRestartOut{}}
	for _, s := range resp.States {
		if s.Status != "pending" {
			continue
		}
		out.Deployments = append(out.Deployments, pendingRestartOut{
			DeploymentID: s.ServerGameConfigId, PendingID: s.PendingRestartId,
			GatingSessionID: s.GatingSessionId, CreatedAtUnix: s.CreatedAtUnix,
		})
	}
	return nil, out, nil
}
