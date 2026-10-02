package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Env overrides persist as a server_game_config-level patch on the game's
// env_vars strategy, in properties format (KEY=VALUE lines).
const (
	strategyTypeEnvVars = "env_vars"
	patchLevelSGC       = "server_game_config"
	patchFormatProps    = "properties"
)

// EditAPI is the slice of the control API the deployment and GameConfig edit tools call.
type EditAPI interface {
	ValidateDeployment(ctx context.Context, in *manmanpb.ValidateDeploymentRequest, opts ...grpc.CallOption) (*manmanpb.ValidateDeploymentResponse, error)
	DeployGameConfig(ctx context.Context, in *manmanpb.DeployGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.DeployGameConfigResponse, error)
	GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error)
	ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error)
	UpdateServerGameConfig(ctx context.Context, in *manmanpb.UpdateServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerGameConfigResponse, error)
	DeleteServerGameConfig(ctx context.Context, in *manmanpb.DeleteServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.DeleteServerGameConfigResponse, error)
	GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error)
	UpdateGameConfig(ctx context.Context, in *manmanpb.UpdateGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.UpdateGameConfigResponse, error)
	DeleteGameConfig(ctx context.Context, in *manmanpb.DeleteGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.DeleteGameConfigResponse, error)
	ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error)
	ListConfigurationStrategies(ctx context.Context, in *manmanpb.ListConfigurationStrategiesRequest, opts ...grpc.CallOption) (*manmanpb.ListConfigurationStrategiesResponse, error)
	CreateConfigurationStrategy(ctx context.Context, in *manmanpb.CreateConfigurationStrategyRequest, opts ...grpc.CallOption) (*manmanpb.CreateConfigurationStrategyResponse, error)
	ListConfigurationPatches(ctx context.Context, in *manmanpb.ListConfigurationPatchesRequest, opts ...grpc.CallOption) (*manmanpb.ListConfigurationPatchesResponse, error)
	CreateConfigurationPatch(ctx context.Context, in *manmanpb.CreateConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.CreateConfigurationPatchResponse, error)
	UpdateConfigurationPatch(ctx context.Context, in *manmanpb.UpdateConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.UpdateConfigurationPatchResponse, error)
	DeleteConfigurationPatch(ctx context.Context, in *manmanpb.DeleteConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.DeleteConfigurationPatchResponse, error)
}

// Edit tool names.
const (
	ValidateDeploymentToolName = "validate_deployment"
	DeployGameConfigToolName   = "deploy_game_config"
	UpdateDeploymentToolName   = "update_deployment"
	UpdateGameConfigToolName   = "update_game_config"
	DeleteDeploymentToolName   = "delete_deployment"
	DeleteGameConfigToolName   = "delete_game_config"
)

// EditTools returns the registry entries for the edit tools. Mutating tools
// carry a Snapshot hook so the audit entry holds the entity's full prior state.
func EditTools(api EditAPI) []Tool {
	e := &editor{api: api}
	return []Tool{
		{Name: ValidateDeploymentToolName, MinPersona: PersonaServerManager, TargetArg: "deployment_id"},
		{Name: DeployGameConfigToolName, MinPersona: PersonaServerManager, TargetArg: "game_config_id", Write: true},
		{Name: UpdateDeploymentToolName, MinPersona: PersonaServerManager, TargetArg: "deployment_id", Write: true, Snapshot: e.snapshotDeployment},
		{Name: UpdateGameConfigToolName, MinPersona: PersonaServerManager, TargetArg: "config_id", Write: true, Snapshot: e.snapshotGameConfig},
		{Name: DeleteDeploymentToolName, MinPersona: PersonaServerManager, TargetArg: "deployment_id", Write: true, Snapshot: e.snapshotDeployment},
		{Name: DeleteGameConfigToolName, MinPersona: PersonaServerManager, TargetArg: "config_id", Write: true, Snapshot: e.snapshotGameConfig},
	}
}

// AddEditTools registers the edit tools on srv; mutating tools run through gate.
func AddEditTools(srv *mcp.Server, gate *Gate, api EditAPI) {
	e := &editor{api: api}
	ids := map[string]any{"deployment_id": intProp("the deployment (server game config) id")}
	ports := map[string]any{"type": "array", "description": "full replacement list of port bindings", "items": map[string]any{
		"type": "object", "required": []string{"container_port", "host_port", "protocol"},
		"properties": map[string]any{"container_port": intProp("container port"), "host_port": intProp("host port"), "protocol": map[string]any{"type": "string", "enum": []string{"TCP", "UDP"}}},
	}}
	strMap := func(d string) map[string]any {
		return map[string]any{"type": "object", "description": d, "additionalProperties": map[string]any{"type": "string"}}
	}

	mcp.AddTool(srv, &mcp.Tool{Name: ValidateDeploymentToolName, Description: "Validate a proposed deployment (server_id, game_config_id, port_bindings) or an existing one (deployment_id). Changes nothing."}, e.validate)

	add := func(name, desc string, props map[string]any, required []string, t GatedTool) {
		srv.AddTool(&mcp.Tool{Name: name, Description: desc, InputSchema: editSchema(props, required)}, gate.Handler(t))
	}
	add(DeployGameConfigToolName, "Create a deployment of a GameConfig on a host. Without confirmation_token returns a preview including validation and a token; call again with the token to create.",
		map[string]any{"server_id": intProp("host server id"), "game_config_id": intProp("GameConfig id"), "port_bindings": ports},
		[]string{"server_id", "game_config_id"}, e.deployTool())
	add(UpdateDeploymentToolName, "Change a deployment's env overrides (env, remove_env) and port bindings. Without confirmation_token returns each changed field's current and proposed value and whether a restart is needed; call again with the token to apply.",
		map[string]any{"deployment_id": ids["deployment_id"], "env": strMap("env vars to set as deployment-level overrides"),
			"remove_env": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "env override names to remove"}, "port_bindings": ports},
		[]string{"deployment_id"}, e.updateDeploymentTool())
	add(UpdateGameConfigToolName, "Edit a GameConfig's name, image, args_template, or env_template (replaces the whole map). Preview is a field-level diff naming the affected deployments.",
		map[string]any{"config_id": intProp("GameConfig id"), "name": map[string]any{"type": "string"}, "image": map[string]any{"type": "string"},
			"args_template": map[string]any{"type": "string"}, "env_template": strMap("replacement env template")},
		[]string{"config_id"}, e.updateGameConfigTool())
	add(DeleteDeploymentToolName, "Delete a deployment. Preview names what will be removed; refused while it has a live session.",
		map[string]any{"deployment_id": ids["deployment_id"]}, []string{"deployment_id"}, e.deleteDeploymentTool())
	add(DeleteGameConfigToolName, "Delete a GameConfig. Preview names what will be removed; refused while deployments still use it.",
		map[string]any{"config_id": intProp("GameConfig id")}, []string{"config_id"}, e.deleteGameConfigTool())
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func editSchema(props map[string]any, required []string) map[string]any {
	p := map[string]any{
		ArgConfirmationToken: map[string]any{"type": "string", "description": "token from the preview call; supply it to apply the previewed change"},
		ArgIdempotencyKey:    map[string]any{"type": "string", "description": "optional key making retries safe"},
	}
	for k, v := range props {
		p[k] = v
	}
	return map[string]any{"type": "object", "properties": p, "required": required}
}

type editor struct{ api EditAPI }

// fingerprintOf hashes v's canonical JSON; it detects changes between preview and confirm.
func fingerprintOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FieldChange is one changed field in an edit preview.
type FieldChange struct {
	Field    string `json:"field"`
	Current  any    `json:"current"`
	Proposed any    `json:"proposed"`
}

type portIn struct {
	ContainerPort int32  `json:"container_port"`
	HostPort      int32  `json:"host_port"`
	Protocol      string `json:"protocol"`
}

func portsToPB(in []portIn) []*manmanpb.PortBinding {
	out := make([]*manmanpb.PortBinding, 0, len(in))
	for _, p := range in {
		out = append(out, &manmanpb.PortBinding{ContainerPort: p.ContainerPort, HostPort: p.HostPort, Protocol: strings.ToUpper(p.Protocol)})
	}
	return out
}

func portsFromPB(in []*manmanpb.PortBinding) []portIn {
	out := make([]portIn, 0, len(in))
	for _, p := range in {
		out = append(out, portIn{p.ContainerPort, p.HostPort, p.Protocol})
	}
	return out
}

// --- validate_deployment ---

type validateIn struct {
	DeploymentID int64    `json:"deployment_id,omitempty" jsonschema:"existing deployment to validate"`
	ServerID     int64    `json:"server_id,omitempty" jsonschema:"host server id for a proposed deployment"`
	GameConfigID int64    `json:"game_config_id,omitempty" jsonschema:"GameConfig id for a proposed deployment"`
	PortBindings []portIn `json:"port_bindings,omitempty"`
}

type issueOut struct {
	Severity   string `json:"severity"`
	Field      string `json:"field"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

type validationOut struct {
	Valid  bool       `json:"valid"`
	Issues []issueOut `json:"issues"`
}

func (e *editor) runValidation(ctx context.Context, serverID, configID int64, ports []*manmanpb.PortBinding) (validationOut, error) {
	resp, err := e.api.ValidateDeployment(ctx, &manmanpb.ValidateDeploymentRequest{ServerId: serverID, GameConfigId: configID, PortBindings: ports})
	if err != nil {
		return validationOut{}, fmt.Errorf("validate deployment: %w", err)
	}
	out := validationOut{Valid: resp.Valid, Issues: []issueOut{}}
	for _, i := range resp.Issues {
		out.Issues = append(out.Issues, issueOut{Severity: i.Severity.String(), Field: i.Field, Message: i.Message, Suggestion: i.Suggestion})
	}
	return out, nil
}

func (e *editor) validate(ctx context.Context, _ *mcp.CallToolRequest, in validateIn) (*mcp.CallToolResult, validationOut, error) {
	serverID, configID, ports := in.ServerID, in.GameConfigID, portsToPB(in.PortBindings)
	if in.DeploymentID != 0 {
		sgc, err := e.getSGC(ctx, in.DeploymentID)
		if err != nil {
			return nil, validationOut{}, err
		}
		serverID, configID, ports = sgc.ServerId, sgc.GameConfigId, sgc.PortBindings
	} else if serverID == 0 || configID == 0 {
		return nil, validationOut{}, fmt.Errorf("provide deployment_id, or server_id and game_config_id")
	}
	out, err := e.runValidation(ctx, serverID, configID, ports)
	return nil, out, err
}

// --- shared lookups ---

func (e *editor) getSGC(ctx context.Context, id int64) (*manmanpb.ServerGameConfig, error) {
	resp, err := e.api.GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{ServerGameConfigId: id})
	if err != nil {
		return nil, backendErr("deployment", id, err)
	}
	if resp.GetConfig() == nil {
		return nil, fmt.Errorf("deployment %d not found", id)
	}
	return resp.Config, nil
}

func (e *editor) getGC(ctx context.Context, id int64) (*manmanpb.GameConfig, error) {
	resp, err := e.api.GetGameConfig(ctx, &manmanpb.GetGameConfigRequest{ConfigId: id})
	if err != nil {
		return nil, backendErr("game config", id, err)
	}
	if resp.GetConfig() == nil {
		return nil, fmt.Errorf("game config %d not found", id)
	}
	return resp.Config, nil
}

func (e *editor) hasLiveSession(ctx context.Context, sgcID int64) (bool, error) {
	resp, err := e.api.ListSessions(ctx, &manmanpb.ListSessionsRequest{ServerGameConfigId: sgcID, LiveOnly: true, PageSize: 1})
	if err != nil {
		return false, fmt.Errorf("list sessions: %w", err)
	}
	return len(resp.Sessions) > 0, nil
}

func (e *editor) deploymentsOf(ctx context.Context, configID int64) ([]*manmanpb.ServerGameConfig, error) {
	var out []*manmanpb.ServerGameConfig
	for token := ""; ; {
		resp, err := e.api.ListServerGameConfigs(ctx, &manmanpb.ListServerGameConfigsRequest{PageSize: 100, PageToken: token})
		if err != nil {
			return nil, fmt.Errorf("list deployments: %w", err)
		}
		for _, c := range resp.Configs {
			if c.GameConfigId == configID {
				out = append(out, c)
			}
		}
		if token = resp.NextPageToken; token == "" {
			return out, nil
		}
	}
}

// envPatch returns the game's env_vars strategy id (0 if none) and the
// deployment's env override patch (nil if none).
func (e *editor) envPatch(ctx context.Context, gameID, sgcID int64) (int64, *manmanpb.ConfigurationPatch, error) {
	sr, err := e.api.ListConfigurationStrategies(ctx, &manmanpb.ListConfigurationStrategiesRequest{GameId: gameID})
	if err != nil {
		return 0, nil, fmt.Errorf("list configuration strategies: %w", err)
	}
	var strategyID int64
	for _, s := range sr.Strategies {
		if s.StrategyType == strategyTypeEnvVars {
			strategyID = s.StrategyId
			break
		}
	}
	if strategyID == 0 {
		return 0, nil, nil
	}
	level := patchLevelSGC
	pr, err := e.api.ListConfigurationPatches(ctx, &manmanpb.ListConfigurationPatchesRequest{StrategyId: &strategyID, PatchLevel: &level, EntityId: &sgcID})
	if err != nil {
		return 0, nil, fmt.Errorf("list env override patches: %w", err)
	}
	if len(pr.Patches) == 0 {
		return strategyID, nil, nil
	}
	return strategyID, pr.Patches[0], nil
}

func parseProps(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func serializeProps(m map[string]string) string {
	keys := sortedKeys(m)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+m[k])
	}
	return strings.Join(lines, "\n")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func validEnvKV(k, v string) error {
	if k == "" || strings.ContainsAny(k, "=#\r\n \t") {
		return fmt.Errorf("invalid env name %q: must not be empty or contain '=', '#', or whitespace", k)
	}
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("env value for %q must not contain newlines", k)
	}
	return nil
}

func unmarshalArgs(args json.RawMessage, into any) error {
	if err := json.Unmarshal(args, into); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// --- deploy_game_config ---

type deployIn struct {
	ServerID     int64    `json:"server_id"`
	GameConfigID int64    `json:"game_config_id"`
	PortBindings []portIn `json:"port_bindings"`
}

func (e *editor) deployTool() GatedTool {
	return GatedTool{
		Name: DeployGameConfigToolName,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			var in deployIn
			if err := unmarshalArgs(raw, &in); err != nil {
				return nil, "", err
			}
			v, err := e.runValidation(ctx, in.ServerID, in.GameConfigID, portsToPB(in.PortBindings))
			if err != nil {
				return nil, "", err
			}
			return map[string]any{"action": "create_deployment", "server_id": in.ServerID, "game_config_id": in.GameConfigID,
				"port_bindings": in.PortBindings, "validation": v}, fingerprintOf(v), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in deployIn
			if err := unmarshalArgs(raw, &in); err != nil {
				return nil, err
			}
			v, err := e.runValidation(ctx, in.ServerID, in.GameConfigID, portsToPB(in.PortBindings))
			if err != nil {
				return nil, err
			}
			if !v.Valid {
				return nil, fmt.Errorf("deployment is not valid; nothing was created: %s", issueSummary(v))
			}
			resp, err := e.api.DeployGameConfig(ctx, &manmanpb.DeployGameConfigRequest{ServerId: in.ServerID, GameConfigId: in.GameConfigID, PortBindings: portsToPB(in.PortBindings)})
			if err != nil {
				return nil, fmt.Errorf("deploy game config: %w", err)
			}
			return map[string]any{"deployment_id": resp.GetConfig().GetServerGameConfigId()}, nil
		},
	}
}

func issueSummary(v validationOut) string {
	var msgs []string
	for _, i := range v.Issues {
		msgs = append(msgs, i.Field+": "+i.Message)
	}
	return strings.Join(msgs, "; ")
}

// --- update_deployment ---

type updateDeploymentIn struct {
	DeploymentID int64             `json:"deployment_id"`
	Env          map[string]string `json:"env"`
	RemoveEnv    []string          `json:"remove_env"`
	PortBindings *[]portIn         `json:"port_bindings"`
}

// deploymentState is the editable state a preview diffs against and fingerprints.
type deploymentState struct {
	SGC        *manmanpb.ServerGameConfig
	GC         *manmanpb.GameConfig
	StrategyID int64
	Patch      *manmanpb.ConfigurationPatch
	Overrides  map[string]string
}

func (s *deploymentState) fingerprint() string {
	pv := map[string]any{"ports": portsFromPB(s.SGC.PortBindings), "status": s.SGC.Status, "overrides": s.Overrides}
	if s.Patch != nil {
		pv["patch_id"], pv["patch_updated_at"] = s.Patch.PatchId, s.Patch.UpdatedAt
	}
	return fingerprintOf(pv)
}

func (e *editor) loadDeployment(ctx context.Context, id int64) (*deploymentState, error) {
	sgc, err := e.getSGC(ctx, id)
	if err != nil {
		return nil, err
	}
	gc, err := e.getGC(ctx, sgc.GameConfigId)
	if err != nil {
		return nil, err
	}
	sid, patch, err := e.envPatch(ctx, gc.GameId, id)
	if err != nil {
		return nil, err
	}
	ov := map[string]string{}
	if patch != nil {
		ov = parseProps(patch.PatchContent)
	}
	return &deploymentState{SGC: sgc, GC: gc, StrategyID: sid, Patch: patch, Overrides: ov}, nil
}

// planUpdate computes the changed fields and the resulting override map.
func planUpdate(st *deploymentState, in updateDeploymentIn) (changes []FieldChange, newOverrides map[string]string, portsChanged bool, err error) {
	newOverrides = map[string]string{}
	for k, v := range st.Overrides {
		newOverrides[k] = v
	}
	effective := func(k string) any {
		if v, ok := st.Overrides[k]; ok {
			return v
		}
		if v, ok := st.GC.EnvTemplate[k]; ok {
			return v
		}
		return nil
	}
	for k, v := range in.Env {
		if err := validEnvKV(k, v); err != nil {
			return nil, nil, false, err
		}
		newOverrides[k] = v
	}
	for _, k := range in.RemoveEnv {
		if _, set := in.Env[k]; set {
			return nil, nil, false, fmt.Errorf("env name %q is both set and removed", k)
		}
		delete(newOverrides, k)
	}
	keys := map[string]bool{}
	for k := range st.Overrides {
		keys[k] = true
	}
	for k := range newOverrides {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		var proposed any
		if v, ok := newOverrides[k]; ok {
			proposed = v
		} else if v, ok := st.GC.EnvTemplate[k]; ok {
			proposed = v // override removed: reverts to the GameConfig template
		}
		if cur := effective(k); cur != proposed {
			changes = append(changes, FieldChange{Field: "env." + k, Current: cur, Proposed: proposed})
		}
	}
	if in.PortBindings != nil {
		cur, prop := portsFromPB(st.SGC.PortBindings), portsFromPB(portsToPB(*in.PortBindings))
		if fingerprintOf(cur) != fingerprintOf(prop) {
			portsChanged = true
			changes = append(changes, FieldChange{Field: "port_bindings", Current: cur, Proposed: prop})
		}
	}
	return changes, newOverrides, portsChanged, nil
}

func (e *editor) updateDeploymentTool() GatedTool {
	return GatedTool{
		Name: UpdateDeploymentToolName,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			var in updateDeploymentIn
			if err := unmarshalArgs(raw, &in); err != nil {
				return nil, "", err
			}
			st, err := e.loadDeployment(ctx, in.DeploymentID)
			if err != nil {
				return nil, "", err
			}
			changes, _, portsChanged, err := planUpdate(st, in)
			if err != nil {
				return nil, "", err
			}
			live, err := e.hasLiveSession(ctx, in.DeploymentID)
			if err != nil {
				return nil, "", err
			}
			p := map[string]any{"deployment_id": in.DeploymentID, "changes": nonNilChanges(changes),
				"restart_needed": len(changes) > 0 && live,
				"note":           "env and port changes take effect at the next session start"}
			if portsChanged {
				v, err := e.runValidation(ctx, st.SGC.ServerId, st.SGC.GameConfigId, portsToPB(*in.PortBindings))
				if err != nil {
					return nil, "", err
				}
				p["validation"] = v
			}
			return p, st.fingerprint(), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in updateDeploymentIn
			if err := unmarshalArgs(raw, &in); err != nil {
				return nil, err
			}
			st, err := e.loadDeployment(ctx, in.DeploymentID)
			if err != nil {
				return nil, err
			}
			changes, newOv, portsChanged, err := planUpdate(st, in)
			if err != nil {
				return nil, err
			}
			if err := e.applyEnv(ctx, st, newOv); err != nil {
				return nil, err
			}
			if portsChanged {
				_, err := e.api.UpdateServerGameConfig(ctx, &manmanpb.UpdateServerGameConfigRequest{
					ServerGameConfigId: in.DeploymentID, PortBindings: portsToPB(*in.PortBindings), UpdatePaths: []string{"port_bindings"}})
				if err != nil {
					return nil, fmt.Errorf("update port bindings: %w", err)
				}
			}
			live, err := e.hasLiveSession(ctx, in.DeploymentID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"deployment_id": in.DeploymentID, "changes": nonNilChanges(changes), "restart_needed": len(changes) > 0 && live}, nil
		},
	}
}

func nonNilChanges(c []FieldChange) []FieldChange {
	if c == nil {
		return []FieldChange{}
	}
	return c
}

// applyEnv writes the override map as the deployment-level patch, creating or
// deleting the patch as the map becomes non-empty or empty.
func (e *editor) applyEnv(ctx context.Context, st *deploymentState, newOv map[string]string) error {
	if fingerprintOf(st.Overrides) == fingerprintOf(newOv) {
		return nil
	}
	content := serializeProps(newOv)
	switch {
	case st.Patch != nil && len(newOv) == 0:
		if _, err := e.api.DeleteConfigurationPatch(ctx, &manmanpb.DeleteConfigurationPatchRequest{PatchId: st.Patch.PatchId}); err != nil {
			return fmt.Errorf("remove env overrides: %w", err)
		}
	case st.Patch != nil:
		if _, err := e.api.UpdateConfigurationPatch(ctx, &manmanpb.UpdateConfigurationPatchRequest{
			PatchId: st.Patch.PatchId, PatchContent: content, PatchFormat: patchFormatProps, PatchOrder: st.Patch.PatchOrder}); err != nil {
			return fmt.Errorf("update env overrides: %w", err)
		}
	default:
		strategyID := st.StrategyID
		if strategyID == 0 {
			resp, err := e.api.CreateConfigurationStrategy(ctx, &manmanpb.CreateConfigurationStrategyRequest{
				GameId: st.GC.GameId, Name: "Environment Variables", Description: "Deployment-level environment variable overrides", StrategyType: strategyTypeEnvVars})
			if err != nil {
				return fmt.Errorf("create env_vars strategy: %w", err)
			}
			strategyID = resp.GetStrategy().GetStrategyId()
		}
		if _, err := e.api.CreateConfigurationPatch(ctx, &manmanpb.CreateConfigurationPatchRequest{
			StrategyId: strategyID, PatchLevel: patchLevelSGC, EntityId: st.SGC.ServerGameConfigId, PatchContent: content, PatchFormat: patchFormatProps}); err != nil {
			return fmt.Errorf("create env overrides: %w", err)
		}
	}
	return nil
}

// --- update_game_config ---

type updateGameConfigIn struct {
	ConfigID     int64             `json:"config_id"`
	Name         *string           `json:"name"`
	Image        *string           `json:"image"`
	ArgsTemplate *string           `json:"args_template"`
	EnvTemplate  map[string]string `json:"env_template"`
}

func gcFingerprint(gc *manmanpb.GameConfig) string {
	return fingerprintOf(map[string]any{"name": gc.Name, "image": gc.Image, "args": gc.ArgsTemplate, "env": gc.EnvTemplate,
		"entrypoint": gc.Entrypoint, "command": gc.Command})
}

func planGameConfig(gc *manmanpb.GameConfig, in updateGameConfigIn) (changes []FieldChange, paths []string) {
	str := func(field string, cur string, prop *string) {
		if prop != nil && *prop != cur {
			changes = append(changes, FieldChange{Field: field, Current: cur, Proposed: *prop})
			paths = append(paths, field)
		}
	}
	str("name", gc.Name, in.Name)
	str("image", gc.Image, in.Image)
	str("args_template", gc.ArgsTemplate, in.ArgsTemplate)
	if in.EnvTemplate != nil {
		keys := map[string]bool{}
		for k := range gc.EnvTemplate {
			keys[k] = true
		}
		for k := range in.EnvTemplate {
			keys[k] = true
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		differs := false
		for _, k := range names {
			var cur, prop any
			if v, ok := gc.EnvTemplate[k]; ok {
				cur = v
			}
			if v, ok := in.EnvTemplate[k]; ok {
				prop = v
			}
			if cur != prop {
				differs = true
				changes = append(changes, FieldChange{Field: "env_template." + k, Current: cur, Proposed: prop})
			}
		}
		if differs {
			paths = append(paths, "env_template")
		}
	}
	return changes, paths
}

func (e *editor) updateGameConfigTool() GatedTool {
	return GatedTool{
		Name: UpdateGameConfigToolName,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			var in updateGameConfigIn
			if err := unmarshalArgs(raw, &in); err != nil {
				return nil, "", err
			}
			gc, err := e.getGC(ctx, in.ConfigID)
			if err != nil {
				return nil, "", err
			}
			deps, err := e.deploymentsOf(ctx, in.ConfigID)
			if err != nil {
				return nil, "", err
			}
			changes, _ := planGameConfig(gc, in)
			return map[string]any{"config_id": in.ConfigID, "changes": nonNilChanges(changes), "affected_deployments": deploymentRefs(deps),
				"note": "running sessions keep the old config until restarted"}, gcFingerprint(gc), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var in updateGameConfigIn
			if err := unmarshalArgs(raw, &in); err != nil {
				return nil, err
			}
			gc, err := e.getGC(ctx, in.ConfigID)
			if err != nil {
				return nil, err
			}
			changes, paths := planGameConfig(gc, in)
			if len(paths) == 0 {
				return map[string]any{"config_id": in.ConfigID, "changes": []FieldChange{}}, nil
			}
			req := &manmanpb.UpdateGameConfigRequest{ConfigId: in.ConfigID, UpdatePaths: paths}
			// Fields outside update_paths are ignored, so the proposed values can be sent unconditionally.
			if in.Name != nil {
				req.Name = *in.Name
			}
			if in.Image != nil {
				req.Image = *in.Image
			}
			if in.ArgsTemplate != nil {
				req.ArgsTemplate = *in.ArgsTemplate
			}
			req.EnvTemplate = in.EnvTemplate
			if _, err := e.api.UpdateGameConfig(ctx, req); err != nil {
				return nil, fmt.Errorf("update game config: %w", err)
			}
			return map[string]any{"config_id": in.ConfigID, "changes": changes}, nil
		},
	}
}

func deploymentRefs(deps []*manmanpb.ServerGameConfig) []map[string]any {
	out := make([]map[string]any, 0, len(deps))
	for _, d := range deps {
		out = append(out, map[string]any{"deployment_id": d.ServerGameConfigId, "host_id": d.ServerId, "status": d.Status})
	}
	return out
}

// --- deletes ---

type deleteDeploymentIn struct {
	DeploymentID int64 `json:"deployment_id"`
}
type deleteGameConfigIn struct {
	ConfigID int64 `json:"config_id"`
}

func (e *editor) deleteDeploymentTool() GatedTool {
	check := func(ctx context.Context, raw json.RawMessage) (*deploymentState, error) {
		var in deleteDeploymentIn
		if err := unmarshalArgs(raw, &in); err != nil {
			return nil, err
		}
		st, err := e.loadDeployment(ctx, in.DeploymentID)
		if err != nil {
			return nil, err
		}
		live, err := e.hasLiveSession(ctx, in.DeploymentID)
		if err != nil {
			return nil, err
		}
		if live {
			return nil, fmt.Errorf("deployment %d has a live session; stop it before deleting", in.DeploymentID)
		}
		return st, nil
	}
	return GatedTool{
		Name: DeleteDeploymentToolName,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			st, err := check(ctx, raw)
			if err != nil {
				return nil, "", err
			}
			removes := []string{fmt.Sprintf("deployment %d (%s on host %d)", st.SGC.ServerGameConfigId, st.GC.Name, st.SGC.ServerId)}
			if st.Patch != nil {
				removes = append(removes, fmt.Sprintf("its env overrides (%d variables)", len(st.Overrides)))
			}
			return map[string]any{"will_remove": removes, "port_bindings": portsFromPB(st.SGC.PortBindings)}, st.fingerprint(), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			st, err := check(ctx, raw)
			if err != nil {
				return nil, err
			}
			if st.Patch != nil {
				if _, err := e.api.DeleteConfigurationPatch(ctx, &manmanpb.DeleteConfigurationPatchRequest{PatchId: st.Patch.PatchId}); err != nil {
					return nil, fmt.Errorf("remove env overrides: %w", err)
				}
			}
			if _, err := e.api.DeleteServerGameConfig(ctx, &manmanpb.DeleteServerGameConfigRequest{ServerGameConfigId: st.SGC.ServerGameConfigId}); err != nil {
				return nil, fmt.Errorf("delete deployment: %w", err)
			}
			return map[string]any{"deleted_deployment_id": st.SGC.ServerGameConfigId}, nil
		},
	}
}

func (e *editor) deleteGameConfigTool() GatedTool {
	check := func(ctx context.Context, raw json.RawMessage) (*manmanpb.GameConfig, error) {
		var in deleteGameConfigIn
		if err := unmarshalArgs(raw, &in); err != nil {
			return nil, err
		}
		gc, err := e.getGC(ctx, in.ConfigID)
		if err != nil {
			return nil, err
		}
		deps, err := e.deploymentsOf(ctx, in.ConfigID)
		if err != nil {
			return nil, err
		}
		if len(deps) > 0 {
			ids := make([]string, 0, len(deps))
			for _, d := range deps {
				ids = append(ids, fmt.Sprint(d.ServerGameConfigId))
			}
			return nil, fmt.Errorf("game config %d is still used by deployments %s; delete them first", in.ConfigID, strings.Join(ids, ", "))
		}
		return gc, nil
	}
	return GatedTool{
		Name: DeleteGameConfigToolName,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			gc, err := check(ctx, raw)
			if err != nil {
				return nil, "", err
			}
			return map[string]any{"will_remove": []string{fmt.Sprintf("game config %d (%s, image %s)", gc.ConfigId, gc.Name, gc.Image)}}, gcFingerprint(gc), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			gc, err := check(ctx, raw)
			if err != nil {
				return nil, err
			}
			if _, err := e.api.DeleteGameConfig(ctx, &manmanpb.DeleteGameConfigRequest{ConfigId: gc.ConfigId}); err != nil {
				return nil, fmt.Errorf("delete game config: %w", err)
			}
			return map[string]any{"deleted_config_id": gc.ConfigId}, nil
		},
	}
}

// --- audit snapshots ---

func (e *editor) snapshotDeployment(c CallContext, raw json.RawMessage) (any, error) {
	var in struct {
		DeploymentID int64 `json:"deployment_id"`
	}
	if err := unmarshalArgs(raw, &in); err != nil {
		return nil, err
	}
	st, err := e.loadDeployment(c, in.DeploymentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deployment": st.SGC, "game_config_id": st.GC.ConfigId, "env_overrides": st.Overrides}, nil
}

func (e *editor) snapshotGameConfig(c CallContext, raw json.RawMessage) (any, error) {
	var in deleteGameConfigIn
	if err := unmarshalArgs(raw, &in); err != nil {
		return nil, err
	}
	return e.getGC(c, in.ConfigID)
}
