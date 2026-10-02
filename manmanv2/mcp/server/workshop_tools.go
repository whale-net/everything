package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Registry entries for the Workshop tools; all need server-manager.
var (
	ListAddonsTool         = Tool{Name: "list_addons", MinPersona: PersonaServerManager}
	ListLibrariesTool      = Tool{Name: "list_libraries", MinPersona: PersonaServerManager}
	ListInstallationsTool  = Tool{Name: "list_installations", MinPersona: PersonaServerManager, TargetArg: "deployment_id"}
	InstallAddonTool       = Tool{Name: "install_addon", MinPersona: PersonaServerManager, TargetArg: "deployment_id", Write: true}
	RemoveInstallationTool = Tool{Name: "remove_installation", MinPersona: PersonaServerManager, TargetArg: "installation_id", Write: true}
	ResetInstallationTool  = Tool{Name: "reset_installation", MinPersona: PersonaServerManager, TargetArg: "installation_id", Write: true}
)

// WorkshopTools lists the Workshop tools for registry construction.
var WorkshopTools = []Tool{ListAddonsTool, ListLibrariesTool, ListInstallationsTool, InstallAddonTool, RemoveInstallationTool, ResetInstallationTool}

// WorkshopAPI is the subset of WorkshopService the Workshop tools call.
type WorkshopAPI interface {
	ListAddons(ctx context.Context, in *manmanpb.ListAddonsRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonsResponse, error)
	ListLibraries(ctx context.Context, in *manmanpb.ListLibrariesRequest, opts ...grpc.CallOption) (*manmanpb.ListLibrariesResponse, error)
	ListInstallations(ctx context.Context, in *manmanpb.ListInstallationsRequest, opts ...grpc.CallOption) (*manmanpb.ListInstallationsResponse, error)
	GetInstallation(ctx context.Context, in *manmanpb.GetInstallationRequest, opts ...grpc.CallOption) (*manmanpb.GetInstallationResponse, error)
	InstallAddon(ctx context.Context, in *manmanpb.InstallAddonRequest, opts ...grpc.CallOption) (*manmanpb.InstallAddonResponse, error)
	RemoveInstallation(ctx context.Context, in *manmanpb.RemoveInstallationRequest, opts ...grpc.CallOption) (*manmanpb.RemoveInstallationResponse, error)
	ResetInstallation(ctx context.Context, in *manmanpb.ResetInstallationRequest, opts ...grpc.CallOption) (*manmanpb.ResetInstallationResponse, error)
}

type addonOut struct {
	ID             int64   `json:"id"`
	GameID         int64   `json:"game_id"`
	WorkshopID     string  `json:"workshop_id"`
	Name           string  `json:"name"`
	Platform       string  `json:"platform"`
	IsCollection   bool    `json:"is_collection"`
	IsDeprecated   bool    `json:"is_deprecated"`
	FileSizeBytes  int64   `json:"file_size_bytes"`
	InstallPath    string  `json:"installation_path,omitempty"`
}

type libraryOut struct {
	ID          int64  `json:"id"`
	GameID      int64  `json:"game_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type installationOut struct {
	ID              int64  `json:"installation_id"`
	DeploymentID    int64  `json:"deployment_id"`
	AddonID         int64  `json:"addon_id"`
	Status          string `json:"status"`
	ProgressPercent int32  `json:"progress_percent"`
	Error           string `json:"error,omitempty"`
}

func toInstallationOut(i *manmanpb.WorkshopInstallation) installationOut {
	return installationOut{ID: i.InstallationId, DeploymentID: i.SgcId, AddonID: i.AddonId, Status: i.Status, ProgressPercent: i.ProgressPercent, Error: i.ErrorMessage}
}

type listAddonsIn struct {
	GameID int64 `json:"game_id,omitempty" jsonschema:"optional: only addons of this game"`
}
type listAddonsOut struct {
	Addons []addonOut `json:"addons"`
}
type listLibrariesIn struct {
	GameID int64 `json:"game_id,omitempty" jsonschema:"optional: only libraries of this game"`
}
type listLibrariesOut struct {
	Libraries []libraryOut `json:"libraries"`
}
type listInstallationsIn struct {
	DeploymentID int64 `json:"deployment_id" jsonschema:"the deployment (server game config) id"`
}
type listInstallationsOut struct {
	Installations []installationOut `json:"installations"`
}

// workshopArgs is the union of the write tools' arguments.
type workshopArgs struct {
	DeploymentID   int64  `json:"deployment_id"`
	AddonID        int64  `json:"addon_id"`
	InstallationID int64  `json:"installation_id"`
	Reinstall      bool   `json:"reinstall"`
	IdempotencyKey string `json:"idempotency_key"`
}

const workshopPage = 100

// AddWorkshopTools registers the Workshop tools on srv. Destructive paths
// (remove, reset, install with reinstall) run through gate.
func AddWorkshopTools(srv *mcp.Server, api WorkshopAPI, gate *Gate) {
	w := &workshop{api: api, gate: gate}
	mcp.AddTool(srv, &mcp.Tool{Name: ListAddonsTool.Name, Description: "List Workshop addons, optionally for one game: id, workshop id, name, game."}, w.listAddons)
	mcp.AddTool(srv, &mcp.Tool{Name: ListLibrariesTool.Name, Description: "List Workshop libraries (named addon collections), optionally for one game."}, w.listLibraries)
	mcp.AddTool(srv, &mcp.Tool{Name: ListInstallationsTool.Name, Description: "List a deployment's addon installations with their status."}, w.listInstallations)

	idem := map[string]any{"type": "string", "description": "optional: retry-safe key; a repeat with the same key and arguments returns the first result"}
	tok := map[string]any{"type": "string", "description": "confirmation_token from the preview call; omit on the first call"}
	schema := func(props map[string]any, required ...string) map[string]any {
		props["idempotency_key"] = idem
		props["confirmation_token"] = tok
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	id := map[string]any{"type": "integer"}
	srv.AddTool(&mcp.Tool{
		Name: InstallAddonTool.Name,
		Description: "Install a Workshop addon on a deployment and start the download. If it is already installed the existing installation is returned and nothing is downloaded. " +
			"With reinstall=true the downloaded content is discarded and re-fetched; that needs confirmation (call again with the returned confirmation_token).",
		InputSchema: schema(map[string]any{"deployment_id": id, "addon_id": id, "reinstall": map[string]any{"type": "boolean"}}, "deployment_id", "addon_id"),
	}, w.installHandler)
	srv.AddTool(&mcp.Tool{
		Name:        RemoveInstallationTool.Name,
		Description: "Remove an addon installation and its downloaded content. Needs confirmation: the first call returns a preview and confirmation_token.",
		InputSchema: schema(map[string]any{"installation_id": id}, "installation_id"),
	}, gate.Handler(w.removeGated()))
	srv.AddTool(&mcp.Tool{
		Name:        ResetInstallationTool.Name,
		Description: "Reset an installation: discard downloaded content and re-download. Needs confirmation: the first call returns a preview and confirmation_token.",
		InputSchema: schema(map[string]any{"installation_id": id}, "installation_id"),
	}, gate.Handler(w.resetGated()))
}

type workshop struct {
	api  WorkshopAPI
	gate *Gate
}

func (w *workshop) listAddons(ctx context.Context, _ *mcp.CallToolRequest, in listAddonsIn) (*mcp.CallToolResult, listAddonsOut, error) {
	out := listAddonsOut{Addons: []addonOut{}}
	for off := int32(0); ; off += workshopPage {
		resp, err := w.api.ListAddons(ctx, &manmanpb.ListAddonsRequest{GameId: in.GameID, Limit: workshopPage, Offset: off})
		if err != nil {
			return nil, out, fmt.Errorf("list addons: %w", err)
		}
		for _, a := range resp.Addons {
			out.Addons = append(out.Addons, addonOut{ID: a.AddonId, GameID: a.GameId, WorkshopID: a.WorkshopId, Name: a.Name, Platform: a.PlatformType,
				IsCollection: a.IsCollection, IsDeprecated: a.IsDeprecated, FileSizeBytes: a.FileSizeBytes, InstallPath: a.InstallationPath})
		}
		if len(resp.Addons) < workshopPage {
			return nil, out, nil
		}
	}
}

func (w *workshop) listLibraries(ctx context.Context, _ *mcp.CallToolRequest, in listLibrariesIn) (*mcp.CallToolResult, listLibrariesOut, error) {
	out := listLibrariesOut{Libraries: []libraryOut{}}
	for off := int32(0); ; off += workshopPage {
		resp, err := w.api.ListLibraries(ctx, &manmanpb.ListLibrariesRequest{GameId: in.GameID, Limit: workshopPage, Offset: off})
		if err != nil {
			return nil, out, fmt.Errorf("list libraries: %w", err)
		}
		for _, l := range resp.Libraries {
			out.Libraries = append(out.Libraries, libraryOut{ID: l.LibraryId, GameID: l.GameId, Name: l.Name, Description: l.Description})
		}
		if len(resp.Libraries) < workshopPage {
			return nil, out, nil
		}
	}
}

func (w *workshop) listInstallations(ctx context.Context, _ *mcp.CallToolRequest, in listInstallationsIn) (*mcp.CallToolResult, listInstallationsOut, error) {
	out := listInstallationsOut{Installations: []installationOut{}}
	for off := int32(0); ; off += workshopPage {
		resp, err := w.api.ListInstallations(ctx, &manmanpb.ListInstallationsRequest{SgcId: in.DeploymentID, Limit: workshopPage, Offset: off})
		if err != nil {
			return nil, out, fmt.Errorf("list installations: %w", err)
		}
		for _, i := range resp.Installations {
			out.Installations = append(out.Installations, toInstallationOut(i))
		}
		if len(resp.Installations) < workshopPage {
			return nil, out, nil
		}
	}
}

func toolResult(v any) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

func toolFailure(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

func parseWorkshopArgs(raw json.RawMessage) (workshopArgs, error) {
	var a workshopArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return a, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	return a, nil
}

// installHandler installs directly, or routes through the confirmation gate
// when reinstall is set.
func (w *workshop) installHandler(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a, err := parseWorkshopArgs(req.Params.Arguments)
	if err != nil {
		return toolFailure(err), nil
	}
	if a.DeploymentID == 0 || a.AddonID == 0 {
		return toolFailure(fmt.Errorf("deployment_id and addon_id are required")), nil
	}
	if a.Reinstall {
		return w.gate.Handler(w.reinstallGated())(ctx, req)
	}
	res, err := w.api.InstallAddon(ctx, &manmanpb.InstallAddonRequest{SgcId: a.DeploymentID, AddonId: a.AddonID})
	if err != nil {
		return toolFailure(fmt.Errorf("install addon: %w", err)), nil
	}
	return toolResult(toInstallationOut(res.Installation)), nil
}

// fingerprint ignores progress so a running download doesn't invalidate a preview.
func fingerprint(i *manmanpb.WorkshopInstallation) string {
	return fmt.Sprintf("%d:%d:%d:%s", i.InstallationId, i.SgcId, i.AddonId, i.Status)
}

func (w *workshop) previewInstallation(ctx context.Context, id int64, effect string) (any, string, error) {
	resp, err := w.api.GetInstallation(ctx, &manmanpb.GetInstallationRequest{InstallationId: id})
	if err != nil {
		return nil, "", toolErrFor("installation", id, err)
	}
	if resp.GetInstallation() == nil {
		return nil, "", fmt.Errorf("installation %d not found", id)
	}
	i := resp.Installation
	return map[string]any{"effect": effect, "installation": toInstallationOut(i)}, fingerprint(i), nil
}

func toolErrFor(what string, id int64, err error) error { return backendErr(what, id, err) }

func (w *workshop) removeGated() GatedTool {
	return GatedTool{
		Name: RemoveInstallationTool.Name,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			a, err := parseWorkshopArgs(raw)
			if err != nil {
				return nil, "", err
			}
			return w.previewInstallation(ctx, a.InstallationID, "removes the installation and discards its downloaded content")
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			a, err := parseWorkshopArgs(raw)
			if err != nil {
				return nil, err
			}
			if _, err := w.api.RemoveInstallation(ctx, &manmanpb.RemoveInstallationRequest{InstallationId: a.InstallationID}); err != nil {
				return nil, fmt.Errorf("remove installation: %w", err)
			}
			return map[string]any{"installation_id": a.InstallationID, "removed": true}, nil
		},
	}
}

func (w *workshop) resetGated() GatedTool {
	return GatedTool{
		Name: ResetInstallationTool.Name,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			a, err := parseWorkshopArgs(raw)
			if err != nil {
				return nil, "", err
			}
			return w.previewInstallation(ctx, a.InstallationID, "discards downloaded content and re-downloads")
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			a, err := parseWorkshopArgs(raw)
			if err != nil {
				return nil, err
			}
			res, err := w.api.ResetInstallation(ctx, &manmanpb.ResetInstallationRequest{InstallationId: a.InstallationID})
			if err != nil {
				return nil, fmt.Errorf("reset installation: %w", err)
			}
			return toInstallationOut(res.Installation), nil
		},
	}
}

// findInstallation returns the deployment's installation of addonID, or nil.
func (w *workshop) findInstallation(ctx context.Context, deploymentID, addonID int64) (*manmanpb.WorkshopInstallation, error) {
	for off := int32(0); ; off += workshopPage {
		resp, err := w.api.ListInstallations(ctx, &manmanpb.ListInstallationsRequest{SgcId: deploymentID, AddonId: addonID, Limit: workshopPage, Offset: off})
		if err != nil {
			return nil, err
		}
		for _, i := range resp.Installations {
			if i.AddonId == addonID && i.SgcId == deploymentID {
				return i, nil
			}
		}
		if len(resp.Installations) < workshopPage {
			return nil, nil
		}
	}
}

func (w *workshop) reinstallGated() GatedTool {
	return GatedTool{
		Name: InstallAddonTool.Name,
		Preview: func(ctx context.Context, raw json.RawMessage) (any, string, error) {
			a, err := parseWorkshopArgs(raw)
			if err != nil {
				return nil, "", err
			}
			cur, err := w.findInstallation(ctx, a.DeploymentID, a.AddonID)
			if err != nil {
				return nil, "", fmt.Errorf("list installations: %w", err)
			}
			if cur == nil {
				return map[string]any{"effect": "no existing installation; installs the addon"}, "none", nil
			}
			return map[string]any{"effect": "discards downloaded content and re-downloads", "installation": toInstallationOut(cur)}, fingerprint(cur), nil
		},
		Apply: func(ctx context.Context, raw json.RawMessage) (any, error) {
			a, err := parseWorkshopArgs(raw)
			if err != nil {
				return nil, err
			}
			res, err := w.api.InstallAddon(ctx, &manmanpb.InstallAddonRequest{SgcId: a.DeploymentID, AddonId: a.AddonID, ForceReinstall: true})
			if err != nil {
				return nil, fmt.Errorf("install addon: %w", err)
			}
			return toInstallationOut(res.Installation), nil
		},
	}
}
