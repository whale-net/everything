package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Registry entries for the session Action tools. execute_action is a write
// tool whose idempotency_key is mandatory: console commands are not safe to repeat.
var (
	GetSessionActionsTool = Tool{Name: "get_session_actions", MinPersona: PersonaGamer, TargetArg: "session_id"}
	ExecuteActionTool     = Tool{Name: "execute_action", MinPersona: PersonaGamer, TargetArg: "session_id", Write: true, KeyRequired: true}
)

// SessionActionTools lists the session Action tools for registry construction.
var SessionActionTools = []Tool{GetSessionActionsTool, ExecuteActionTool}

// ActionAllowlist reports whether gamers may run an Action on a deployment.
type ActionAllowlist interface {
	Allowed(ctx context.Context, deploymentID int64, actionName string) (bool, error)
}

// SQLActionAllowlist reads current grants from mcp_gamer_action_allowlist.
type SQLActionAllowlist struct{ DB *sql.DB }

func (s SQLActionAllowlist) Allowed(ctx context.Context, deploymentID int64, actionName string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM mcp_gamer_action_allowlist WHERE deployment_id = $1 AND action_name = $2 AND valid_to IS NULL`,
		deploymentID, actionName).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SessionActionsAPI is the subset of the control API the Action tools call.
type SessionActionsAPI interface {
	GetSession(ctx context.Context, in *manmanpb.GetSessionRequest, opts ...grpc.CallOption) (*manmanpb.GetSessionResponse, error)
	ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error)
	GetSessionActions(ctx context.Context, in *manmanpb.GetSessionActionsRequest, opts ...grpc.CallOption) (*manmanpb.GetSessionActionsResponse, error)
	ExecuteAction(ctx context.Context, in *manmanpb.ExecuteActionRequest, opts ...grpc.CallOption) (*manmanpb.ExecuteActionResponse, error)
}

type actionParamOut struct {
	Name         string `json:"name"`
	Label        string `json:"label,omitempty"`
	Type         string `json:"type,omitempty"`
	Required     bool   `json:"required"`
	DefaultValue string `json:"default_value,omitempty"`
}

type actionOut struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Parameters  []actionParamOut `json:"parameters"`
}

type getSessionActionsIn struct {
	SessionID int64 `json:"session_id" jsonschema:"the session id"`
}

type getSessionActionsOut struct {
	Actions []actionOut `json:"actions"`
}

type executeActionIn struct {
	SessionID      int64             `json:"session_id,omitempty" jsonschema:"the running session id; give this or deployment_id"`
	DeploymentID   int64             `json:"deployment_id,omitempty" jsonschema:"the deployment id, resolved to its running session; give this or session_id"`
	ActionName     string            `json:"action_name" jsonschema:"the Action name from get_session_actions"`
	Params         map[string]string `json:"params,omitempty" jsonschema:"Action parameter values by field name"`
	IdempotencyKey string            `json:"idempotency_key" jsonschema:"required; a retry with the same key and arguments returns the original dispatch"`
}

type executeActionOut struct {
	SessionID       int64  `json:"session_id"`
	ExecutionID     int64  `json:"execution_id"`
	RenderedCommand string `json:"rendered_command"`
}

// AddSessionActionTools registers get_session_actions and execute_action.
// Gamers may only execute Actions on the allowlist; server managers and admins
// are not subject to it.
func AddSessionActionTools(srv *mcp.Server, api SessionActionsAPI, allow ActionAllowlist) {
	a := &sessionActions{api: api, allow: allow}
	mcp.AddTool(srv, &mcp.Tool{Name: GetSessionActionsTool.Name, Description: "List the console Actions available on a session, each with its name and parameters."}, a.list)
	mcp.AddTool(srv, &mcp.Tool{Name: ExecuteActionTool.Name, Description: "Run a named console Action on a running session."}, a.execute)
}

type sessionActions struct {
	api   SessionActionsAPI
	allow ActionAllowlist
}

func (a *sessionActions) list(ctx context.Context, _ *mcp.CallToolRequest, in getSessionActionsIn) (*mcp.CallToolResult, getSessionActionsOut, error) {
	resp, err := a.api.GetSessionActions(ctx, &manmanpb.GetSessionActionsRequest{SessionId: in.SessionID})
	if err != nil {
		return nil, getSessionActionsOut{}, toolErr("session", in.SessionID, err)
	}
	out := getSessionActionsOut{Actions: []actionOut{}}
	for _, d := range resp.Actions {
		out.Actions = append(out.Actions, toActionOut(d))
	}
	return nil, out, nil
}

func toActionOut(d *manmanpb.ActionDefinition) actionOut {
	o := actionOut{Name: d.Name, Description: d.Description, Parameters: []actionParamOut{}}
	for _, f := range d.InputFields {
		o.Parameters = append(o.Parameters, actionParamOut{Name: f.Name, Label: f.Label, Type: f.FieldType, Required: f.Required, DefaultValue: f.DefaultValue})
	}
	return o
}

func (a *sessionActions) execute(ctx context.Context, _ *mcp.CallToolRequest, in executeActionIn) (*mcp.CallToolResult, executeActionOut, error) {
	caller := CallerFromContext(ctx)
	if caller == nil {
		return nil, executeActionOut{}, ErrUnauthenticated
	}
	if in.ActionName == "" {
		return nil, executeActionOut{}, errors.New("action_name is required")
	}
	if in.IdempotencyKey == "" {
		return nil, executeActionOut{}, ErrIdempotencyRequired
	}
	sess, err := a.resolveSession(ctx, in)
	if err != nil {
		return nil, executeActionOut{}, err
	}
	if sess.Status != "running" {
		return nil, executeActionOut{}, fmt.Errorf("session %d is not running (status %q); no command dispatched", sess.SessionId, sess.Status)
	}
	if caller.Persona < PersonaServerManager {
		ok, err := a.allow.Allowed(ctx, sess.ServerGameConfigId, in.ActionName)
		if err != nil {
			return nil, executeActionOut{}, fmt.Errorf("checking action allowlist: %w", err)
		}
		if !ok {
			return nil, executeActionOut{}, fmt.Errorf("%w: action %q is not allowlisted for gamers on deployment %d", ErrPermissionDenied, in.ActionName, sess.ServerGameConfigId)
		}
	}
	actions, err := a.api.GetSessionActions(ctx, &manmanpb.GetSessionActionsRequest{SessionId: sess.SessionId})
	if err != nil {
		return nil, executeActionOut{}, toolErr("session", sess.SessionId, err)
	}
	var def *manmanpb.ActionDefinition
	for _, d := range actions.Actions {
		if d.Name == in.ActionName {
			def = d
			break
		}
	}
	if def == nil {
		return nil, executeActionOut{}, fmt.Errorf("action %q is not available on session %d", in.ActionName, sess.SessionId)
	}
	resp, err := a.api.ExecuteAction(ctx, &manmanpb.ExecuteActionRequest{SessionId: sess.SessionId, ActionId: def.ActionId, InputValues: in.Params})
	if err != nil {
		return nil, executeActionOut{}, fmt.Errorf("executing action: %w", err)
	}
	if !resp.Success {
		return nil, executeActionOut{}, fmt.Errorf("action %q was not dispatched: %s", in.ActionName, resp.ErrorMessage)
	}
	return nil, executeActionOut{SessionID: sess.SessionId, ExecutionID: resp.ExecutionId, RenderedCommand: resp.RenderedCommand}, nil
}

func (a *sessionActions) resolveSession(ctx context.Context, in executeActionIn) (*manmanpb.Session, error) {
	switch {
	case in.SessionID != 0 && in.DeploymentID != 0:
		return nil, errors.New("give session_id or deployment_id, not both")
	case in.SessionID != 0:
		resp, err := a.api.GetSession(ctx, &manmanpb.GetSessionRequest{SessionId: in.SessionID})
		if err != nil {
			return nil, toolErr("session", in.SessionID, err)
		}
		if resp.GetSession() == nil {
			return nil, fmt.Errorf("session %d not found", in.SessionID)
		}
		return resp.Session, nil
	case in.DeploymentID != 0:
		resp, err := a.api.ListSessions(ctx, &manmanpb.ListSessionsRequest{ServerGameConfigId: in.DeploymentID, LiveOnly: true, PageSize: 100})
		if err != nil {
			return nil, toolErr("deployment", in.DeploymentID, err)
		}
		for _, s := range resp.Sessions {
			if s.Status == "running" {
				return s, nil
			}
		}
		return nil, fmt.Errorf("deployment %d has no running session; no command dispatched", in.DeploymentID)
	}
	return nil, errors.New("session_id or deployment_id is required")
}
