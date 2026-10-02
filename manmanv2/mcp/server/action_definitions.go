package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Registry entries for Action definition tools; all need server-manager.
func ActionDefinitionTools(api ActionDefinitionAPI) []Tool {
	snap := func(ctx CallContext, args json.RawMessage) (any, error) {
		var a struct {
			ID int64 `json:"action_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		cur, err := fetchActionDefinition(ctx, api, a.ID)
		if err != nil {
			return nil, err
		}
		return cur, nil
	}
	return []Tool{
		{Name: "list_action_definitions", MinPersona: PersonaServerManager},
		{Name: "get_action_definition", MinPersona: PersonaServerManager, TargetArg: "action_id"},
		{Name: "create_action_definition", MinPersona: PersonaServerManager, Write: true},
		{Name: "update_action_definition", MinPersona: PersonaServerManager, TargetArg: "action_id", Write: true, Snapshot: snap},
		{Name: "delete_action_definition", MinPersona: PersonaServerManager, TargetArg: "action_id", Write: true, Snapshot: snap},
	}
}

// ActionDefinitionAPI is the subset of the control API these tools call.
type ActionDefinitionAPI interface {
	ListActionDefinitions(ctx context.Context, in *manmanpb.ListActionDefinitionsRequest, opts ...grpc.CallOption) (*manmanpb.ListActionDefinitionsResponse, error)
	GetActionDefinition(ctx context.Context, in *manmanpb.GetActionDefinitionRequest, opts ...grpc.CallOption) (*manmanpb.GetActionDefinitionResponse, error)
	CreateActionDefinition(ctx context.Context, in *manmanpb.CreateActionDefinitionRequest, opts ...grpc.CallOption) (*manmanpb.CreateActionDefinitionResponse, error)
	UpdateActionDefinition(ctx context.Context, in *manmanpb.UpdateActionDefinitionRequest, opts ...grpc.CallOption) (*manmanpb.UpdateActionDefinitionResponse, error)
	DeleteActionDefinition(ctx context.Context, in *manmanpb.DeleteActionDefinitionRequest, opts ...grpc.CallOption) (*manmanpb.DeleteActionDefinitionResponse, error)
}

type optionDef struct {
	Value        string `json:"value"`
	Label        string `json:"label"`
	DisplayOrder int32  `json:"display_order,omitempty"`
	IsDefault    bool   `json:"is_default,omitempty"`
}

type parameterDef struct {
	Name         string      `json:"name"`
	Label        string      `json:"label"`
	FieldType    string      `json:"field_type"`
	Placeholder  string      `json:"placeholder,omitempty"`
	HelpText     string      `json:"help_text,omitempty"`
	Required     bool        `json:"required,omitempty"`
	DefaultValue string      `json:"default_value,omitempty"`
	DisplayOrder int32       `json:"display_order,omitempty"`
	Pattern      string      `json:"pattern,omitempty"`
	MinValue     float64     `json:"min_value,omitempty"`
	MaxValue     float64     `json:"max_value,omitempty"`
	MinLength    int32       `json:"min_length,omitempty"`
	MaxLength    int32       `json:"max_length,omitempty"`
	Options      []optionDef `json:"options,omitempty"`
}

// actionDef is the tool-facing shape of an Action definition.
type actionDef struct {
	ActionID             int64          `json:"action_id,omitempty"`
	DefinitionLevel      string         `json:"definition_level"`
	EntityID             int64          `json:"entity_id"`
	Name                 string         `json:"name"`
	Label                string         `json:"label"`
	Description          string         `json:"description,omitempty"`
	CommandTemplate      string         `json:"command_template"`
	DisplayOrder         int32          `json:"display_order,omitempty"`
	GroupName            string         `json:"group_name,omitempty"`
	ButtonStyle          string         `json:"button_style,omitempty"`
	Icon                 string         `json:"icon,omitempty"`
	RequiresConfirmation bool           `json:"requires_confirmation,omitempty"`
	ConfirmationMessage  string         `json:"confirmation_message,omitempty"`
	Enabled              bool           `json:"enabled"`
	Parameters           []parameterDef `json:"parameters"`
}

func toActionDef(a *manmanpb.ActionDefinition, fields []*manmanpb.ActionInputField) actionDef {
	d := actionDef{
		ActionID: a.ActionId, DefinitionLevel: a.DefinitionLevel, EntityID: a.EntityId, Name: a.Name, Label: a.Label,
		Description: a.Description, CommandTemplate: a.CommandTemplate, DisplayOrder: a.DisplayOrder, GroupName: a.GroupName,
		ButtonStyle: a.ButtonStyle, Icon: a.Icon, RequiresConfirmation: a.RequiresConfirmation,
		ConfirmationMessage: a.ConfirmationMessage, Enabled: a.Enabled, Parameters: []parameterDef{},
	}
	for _, f := range fields {
		p := parameterDef{
			Name: f.Name, Label: f.Label, FieldType: f.FieldType, Placeholder: f.Placeholder, HelpText: f.HelpText,
			Required: f.Required, DefaultValue: f.DefaultValue, DisplayOrder: f.DisplayOrder, Pattern: f.Pattern,
			MinValue: f.MinValue, MaxValue: f.MaxValue, MinLength: f.MinLength, MaxLength: f.MaxLength,
		}
		for _, o := range f.Options {
			p.Options = append(p.Options, optionDef{o.Value, o.Label, o.DisplayOrder, o.IsDefault})
		}
		d.Parameters = append(d.Parameters, p)
	}
	return d
}

// toRequest flattens parameters into the API's field list; options are keyed
// by their field's 0-based position.
func (d actionDef) toRequest() (*manmanpb.ActionDefinition, []*manmanpb.ActionInputField, []*manmanpb.ActionInputOption) {
	a := &manmanpb.ActionDefinition{
		ActionId: d.ActionID, DefinitionLevel: d.DefinitionLevel, EntityId: d.EntityID, Name: d.Name, Label: d.Label,
		Description: d.Description, CommandTemplate: d.CommandTemplate, DisplayOrder: d.DisplayOrder, GroupName: d.GroupName,
		ButtonStyle: d.ButtonStyle, Icon: d.Icon, RequiresConfirmation: d.RequiresConfirmation,
		ConfirmationMessage: d.ConfirmationMessage, Enabled: d.Enabled,
	}
	var fields []*manmanpb.ActionInputField
	var opts []*manmanpb.ActionInputOption
	for i, p := range d.Parameters {
		fields = append(fields, &manmanpb.ActionInputField{
			Name: p.Name, Label: p.Label, FieldType: p.FieldType, Placeholder: p.Placeholder, HelpText: p.HelpText,
			Required: p.Required, DefaultValue: p.DefaultValue, DisplayOrder: p.DisplayOrder, Pattern: p.Pattern,
			MinValue: p.MinValue, MaxValue: p.MaxValue, MinLength: p.MinLength, MaxLength: p.MaxLength,
		})
		for _, o := range p.Options {
			opts = append(opts, &manmanpb.ActionInputOption{FieldId: int64(i), Value: o.Value, Label: o.Label, DisplayOrder: o.DisplayOrder, IsDefault: o.IsDefault})
		}
	}
	return a, fields, opts
}

var errActionNotFound = func(id int64) error { return fmt.Errorf("action definition %d not found", id) }

func fetchActionDefinition(ctx context.Context, api ActionDefinitionAPI, id int64) (actionDef, error) {
	resp, err := api.GetActionDefinition(ctx, &manmanpb.GetActionDefinitionRequest{ActionId: id})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return actionDef{}, errActionNotFound(id)
		}
		return actionDef{}, fmt.Errorf("get action definition: %w", err)
	}
	if resp.GetAction() == nil {
		return actionDef{}, errActionNotFound(id)
	}
	return toActionDef(resp.Action, resp.InputFields), nil
}

func fingerprintOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type fieldChange struct {
	Current  any `json:"current"`
	Proposed any `json:"proposed"`
}

// diffActionDefs lists each field whose value differs between cur and next.
func diffActionDefs(cur, next actionDef) map[string]fieldChange {
	out := map[string]fieldChange{}
	cv, nv := reflect.ValueOf(cur), reflect.ValueOf(next)
	t := cv.Type()
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "action_id" || reflect.DeepEqual(cv.Field(i).Interface(), nv.Field(i).Interface()) {
			continue
		}
		out[name] = fieldChange{cv.Field(i).Interface(), nv.Field(i).Interface()}
	}
	return out
}

// updateIn is a partial update: only provided fields change.
type updateIn struct {
	ActionID             int64           `json:"action_id"`
	Label                *string         `json:"label"`
	Description          *string         `json:"description"`
	CommandTemplate      *string         `json:"command_template"`
	DisplayOrder         *int32          `json:"display_order"`
	GroupName            *string         `json:"group_name"`
	ButtonStyle          *string         `json:"button_style"`
	Icon                 *string         `json:"icon"`
	RequiresConfirmation *bool           `json:"requires_confirmation"`
	ConfirmationMessage  *string         `json:"confirmation_message"`
	Enabled              *bool           `json:"enabled"`
	Parameters           *[]parameterDef `json:"parameters"`
}

func (u updateIn) apply(cur actionDef) actionDef {
	n := cur
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&n.Label, u.Label)
	set(&n.Description, u.Description)
	set(&n.CommandTemplate, u.CommandTemplate)
	set(&n.GroupName, u.GroupName)
	set(&n.ButtonStyle, u.ButtonStyle)
	set(&n.Icon, u.Icon)
	set(&n.ConfirmationMessage, u.ConfirmationMessage)
	if u.DisplayOrder != nil {
		n.DisplayOrder = *u.DisplayOrder
	}
	if u.RequiresConfirmation != nil {
		n.RequiresConfirmation = *u.RequiresConfirmation
	}
	if u.Enabled != nil {
		n.Enabled = *u.Enabled
	}
	if u.Parameters != nil {
		n.Parameters = *u.Parameters
		if n.Parameters == nil {
			n.Parameters = []parameterDef{}
		}
	}
	return n
}

type listActionsIn struct {
	GameID   *int64 `json:"game_id,omitempty" jsonschema:"list definitions at game level"`
	ConfigID *int64 `json:"config_id,omitempty" jsonschema:"list definitions at game-config level"`
	SGCID    *int64 `json:"deployment_id,omitempty" jsonschema:"list definitions at deployment level"`
}
type listActionsOut struct {
	Actions []actionDef `json:"actions"`
}
type getActionIn struct {
	ActionID int64 `json:"action_id" jsonschema:"the Action definition id"`
}

// AddActionDefinitionTools registers the read tools and the gated write tools.
func AddActionDefinitionTools(srv *mcp.Server, api ActionDefinitionAPI, gate *Gate) {
	mcp.AddTool(srv, &mcp.Tool{Name: "list_action_definitions", Description: "List Action definitions (with command template and parameters) for one game, game config, or deployment. Exactly one of game_id, config_id, deployment_id is required."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in listActionsIn) (*mcp.CallToolResult, listActionsOut, error) {
			resp, err := api.ListActionDefinitions(ctx, &manmanpb.ListActionDefinitionsRequest{GameId: in.GameID, ConfigId: in.ConfigID, SgcId: in.SGCID})
			if err != nil {
				return nil, listActionsOut{}, fmt.Errorf("list action definitions: %w", err)
			}
			out := listActionsOut{Actions: []actionDef{}}
			for _, a := range resp.Actions {
				out.Actions = append(out.Actions, toActionDef(a, a.InputFields))
			}
			return nil, out, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "get_action_definition", Description: "Get one Action definition with its command template and parameters."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in getActionIn) (*mcp.CallToolResult, actionDef, error) {
			d, err := fetchActionDefinition(ctx, api, in.ActionID)
			return nil, d, err
		})

	create := GatedTool{
		Name: "create_action_definition",
		Preview: func(_ context.Context, args json.RawMessage) (any, string, error) {
			d, err := parseCreate(args)
			if err != nil {
				return nil, "", err
			}
			return map[string]any{"effect": "creates this Action definition", "definition": d}, "", nil
		},
		Apply: func(ctx context.Context, args json.RawMessage) (any, error) {
			d, err := parseCreate(args)
			if err != nil {
				return nil, err
			}
			a, f, o := d.toRequest()
			resp, err := api.CreateActionDefinition(ctx, &manmanpb.CreateActionDefinitionRequest{Action: a, InputFields: f, InputOptions: o})
			if err != nil {
				return nil, fmt.Errorf("create action definition: %w", err)
			}
			return map[string]any{"action_id": resp.ActionId}, nil
		},
	}
	update := GatedTool{
		Name: "update_action_definition",
		Preview: func(ctx context.Context, args json.RawMessage) (any, string, error) {
			_, cur, next, err := planUpdate(ctx, api, args)
			if err != nil {
				return nil, "", err
			}
			return map[string]any{"effect": "updates this Action definition", "action_id": cur.ActionID, "changes": diffActionDefs(cur, next)}, fingerprintOf(cur), nil
		},
		Apply: func(ctx context.Context, args json.RawMessage) (any, error) {
			_, _, next, err := planUpdate(ctx, api, args)
			if err != nil {
				return nil, err
			}
			a, f, o := next.toRequest()
			if _, err := api.UpdateActionDefinition(ctx, &manmanpb.UpdateActionDefinitionRequest{Action: a, InputFields: f, InputOptions: o}); err != nil {
				return nil, fmt.Errorf("update action definition: %w", err)
			}
			return map[string]any{"action_id": next.ActionID, "updated": true}, nil
		},
	}
	del := GatedTool{
		Name: "delete_action_definition",
		Preview: func(ctx context.Context, args json.RawMessage) (any, string, error) {
			var in getActionIn
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, "", err
			}
			cur, err := fetchActionDefinition(ctx, api, in.ActionID)
			if err != nil {
				return nil, "", err
			}
			return map[string]any{"effect": "permanently deletes this Action definition", "definition": cur}, fingerprintOf(cur), nil
		},
		Apply: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in getActionIn
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			if _, err := api.DeleteActionDefinition(ctx, &manmanpb.DeleteActionDefinitionRequest{ActionId: in.ActionID}); err != nil {
				return nil, fmt.Errorf("delete action definition: %w", err)
			}
			return map[string]any{"action_id": in.ActionID, "deleted": true}, nil
		},
	}

	paramSchema := map[string]any{"type": "array", "description": "input parameters; each may carry select/radio options", "items": map[string]any{"type": "object"}}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	common := func() map[string]any {
		return map[string]any{
			"label": str("display name"), "description": str("description"), "command_template": str("Go template, e.g. \"changelevel {{.map}}\""),
			"display_order": map[string]any{"type": "integer"}, "group_name": str("button group"), "button_style": str("primary, success, danger, ..."),
			"icon": str("icon class"), "requires_confirmation": map[string]any{"type": "boolean"}, "confirmation_message": str("shown before running"),
			"enabled": map[string]any{"type": "boolean"}, "parameters": paramSchema,
			ArgConfirmationToken: str("token from the preview call; omit to get a preview"), ArgIdempotencyKey: str("optional retry key"),
		}
	}
	cp := common()
	cp["definition_level"] = str("game, game_config, or server_game_config")
	cp["entity_id"] = map[string]any{"type": "integer", "description": "game, config, or deployment id per definition_level"}
	cp["name"] = str("unique identifier, e.g. save_game")
	up := common()
	up["action_id"] = map[string]any{"type": "integer"}
	idOnly := map[string]any{"action_id": map[string]any{"type": "integer"}, ArgConfirmationToken: str("token from the preview call; omit to get a preview"), ArgIdempotencyKey: str("optional retry key")}
	obj := func(props map[string]any, req ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	srv.AddTool(&mcp.Tool{Name: create.Name, Description: "Create an Action definition. Without confirmation_token returns a preview and token; call again with the token to apply.", InputSchema: obj(cp, "definition_level", "entity_id", "name", "label", "command_template")}, gate.Handler(create))
	srv.AddTool(&mcp.Tool{Name: update.Name, Description: "Update an Action definition; only provided fields change. Without confirmation_token returns each changed field's current and proposed value plus a token; call again with the token to apply.", InputSchema: obj(up, "action_id")}, gate.Handler(update))
	srv.AddTool(&mcp.Tool{Name: del.Name, Description: "Delete an Action definition. Without confirmation_token returns a preview and token; call again with the token to apply.", InputSchema: obj(idOnly, "action_id")}, gate.Handler(del))
}

func parseCreate(args json.RawMessage) (actionDef, error) {
	var d actionDef
	if err := json.Unmarshal(args, &d); err != nil {
		return d, err
	}
	d.ActionID = 0
	switch {
	case d.Name == "", d.Label == "", d.CommandTemplate == "":
		return d, errors.New("name, label and command_template are required")
	case d.DefinitionLevel == "" || d.EntityID <= 0:
		return d, errors.New("definition_level and entity_id are required")
	}
	if d.Parameters == nil {
		d.Parameters = []parameterDef{}
	}
	return d, nil
}

func planUpdate(ctx context.Context, api ActionDefinitionAPI, args json.RawMessage) (updateIn, actionDef, actionDef, error) {
	var in updateIn
	if err := json.Unmarshal(args, &in); err != nil {
		return in, actionDef{}, actionDef{}, err
	}
	cur, err := fetchActionDefinition(ctx, api, in.ActionID)
	if err != nil {
		return in, actionDef{}, actionDef{}, err
	}
	return in, cur, in.apply(cur), nil
}
