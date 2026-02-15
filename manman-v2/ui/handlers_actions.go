package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manman/protos"
)

// ActionsPageData holds data for the actions management page
type ActionsPageData struct {
	Title           string
	Active          string
	User            *htmxauth.UserInfo
	DefinitionLevel string // "game", "game_config", or "server_game_config"
	EntityID        int64
	EntityName      string // Name of the game/config/sgc for display
	LocalActions    []*ActionWithFields
	InheritedActions []*ActionWithFields
	FieldTypes      []string
	ButtonStyles    []string
	IconOptions     []IconOption
}

// ActionWithFields combines action with its input fields
type ActionWithFields struct {
	Action *manmanpb.ActionDefinition
	Fields []*FieldWithOptions
}

// FieldWithOptions combines input field with its options
type FieldWithOptions struct {
	Field   *manmanpb.ActionInputField
	Options []*manmanpb.ActionInputOption
}

// IconOption represents a Font Awesome icon choice
type IconOption struct {
	Class string
	Label string
}

// getIconOptions returns common Font Awesome icons for action buttons
func getIconOptions() []IconOption {
	return []IconOption{
		{Class: "", Label: "None"},
		{Class: "fa-play", Label: "Play"},
		{Class: "fa-stop", Label: "Stop"},
		{Class: "fa-pause", Label: "Pause"},
		{Class: "fa-save", Label: "Save"},
		{Class: "fa-trash", Label: "Trash"},
		{Class: "fa-power-off", Label: "Power Off"},
		{Class: "fa-refresh", Label: "Refresh"},
		{Class: "fa-redo", Label: "Redo"},
		{Class: "fa-bomb", Label: "Bomb"},
		{Class: "fa-wrench", Label: "Wrench"},
		{Class: "fa-cog", Label: "Settings"},
		{Class: "fa-terminal", Label: "Terminal"},
		{Class: "fa-server", Label: "Server"},
		{Class: "fa-database", Label: "Database"},
		{Class: "fa-upload", Label: "Upload"},
		{Class: "fa-download", Label: "Download"},
		{Class: "fa-warning", Label: "Warning"},
		{Class: "fa-info-circle", Label: "Info"},
		{Class: "fa-bell", Label: "Bell"},
		{Class: "fa-comment", Label: "Comment"},
		{Class: "fa-users", Label: "Users"},
		{Class: "fa-user", Label: "User"},
		{Class: "fa-map", Label: "Map"},
		{Class: "fa-gamepad", Label: "Gamepad"},
	}
}

// handleGameActions displays the actions management page for a game
func (app *App) handleGameActions(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	// Extract game ID from URL
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/games/"), "/")
	if len(parts) < 2 || parts[1] != "actions" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	gameID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}

	// Handle POST requests for create/update/delete
	if r.Method == http.MethodPost {
		app.handleActionMutation(w, r, "game", gameID)
		return
	}

	// GET request - show the actions page
	game, err := app.grpc.GetGame(ctx, gameID)
	if err != nil {
		log.Printf("Error fetching game: %v", err)
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	// Fetch actions for this game
	actions, err := app.grpc.ListActionDefinitions(ctx, &gameID, nil, nil)
	if err != nil {
		log.Printf("Error fetching actions: %v", err)
		http.Error(w, "Failed to fetch actions", http.StatusInternalServerError)
		return
	}

	localActions, inheritedActions := app.categorizeActions(ctx, actions, "game", gameID)

	data := ActionsPageData{
		Title:            "Manage Actions - " + game.Name,
		Active:           "games",
		User:             user,
		DefinitionLevel:  "game",
		EntityID:         gameID,
		EntityName:       game.Name,
		LocalActions:     localActions,
		InheritedActions: inheritedActions,
		FieldTypes:       []string{"text", "number", "select", "textarea", "checkbox", "radio", "email", "url"},
		ButtonStyles:     []string{"primary", "secondary", "success", "danger", "warning", "info", "light", "dark"},
		IconOptions:      getIconOptions(),
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "actions_manage_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleConfigActions displays the actions management page for a config
func (app *App) handleConfigActions(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	// Extract config ID from URL
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/configs/"), "/")
	if len(parts) < 2 || parts[1] != "actions" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	configID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}

	// Handle POST requests for create/update/delete
	if r.Method == http.MethodPost {
		app.handleActionMutation(w, r, "game_config", configID)
		return
	}

	// GET request - show the actions page
	config, err := app.grpc.GetGameConfig(ctx, configID)
	if err != nil {
		log.Printf("Error fetching config: %v", err)
		http.Error(w, "Config not found", http.StatusNotFound)
		return
	}

	game, err := app.grpc.GetGame(ctx, config.GameId)
	if err != nil {
		log.Printf("Error fetching game: %v", err)
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	// Fetch actions for this config and its parent game
	gameActions, _ := app.grpc.ListActionDefinitions(ctx, &config.GameId, nil, nil)
	configActions, _ := app.grpc.ListActionDefinitions(ctx, nil, &configID, nil)

	allActions := append(gameActions, configActions...)
	localActions, inheritedActions := app.categorizeActions(ctx, allActions, "game_config", configID)

	data := ActionsPageData{
		Title:            "Manage Actions - " + config.Name,
		Active:           "games",
		User:             user,
		DefinitionLevel:  "game_config",
		EntityID:         configID,
		EntityName:       fmt.Sprintf("%s / %s", game.Name, config.Name),
		LocalActions:     localActions,
		InheritedActions: inheritedActions,
		FieldTypes:       []string{"text", "number", "select", "textarea", "checkbox", "radio", "email", "url"},
		ButtonStyles:     []string{"primary", "secondary", "success", "danger", "warning", "info", "light", "dark"},
		IconOptions:      getIconOptions(),
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "actions_manage_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleSGCActions displays the actions management page for an SGC
func (app *App) handleSGCActions(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	// Extract SGC ID from URL
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/sgcs/"), "/")
	if len(parts) < 2 || parts[1] != "actions" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	sgcID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}

	// Handle POST requests for create/update/delete
	if r.Method == http.MethodPost {
		app.handleActionMutation(w, r, "server_game_config", sgcID)
		return
	}

	// GET request - show the actions page
	sgcResp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		log.Printf("Error fetching SGC: %v", err)
		http.Error(w, "SGC not found", http.StatusNotFound)
		return
	}
	sgc := sgcResp.Config

	config, err := app.grpc.GetGameConfig(ctx, sgc.GameConfigId)
	if err != nil {
		log.Printf("Error fetching config: %v", err)
		http.Error(w, "Config not found", http.StatusNotFound)
		return
	}

	game, err := app.grpc.GetGame(ctx, config.GameId)
	if err != nil {
		log.Printf("Error fetching game: %v", err)
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	// Fetch actions for all levels
	gameActions, _ := app.grpc.ListActionDefinitions(ctx, &config.GameId, nil, nil)
	configActions, _ := app.grpc.ListActionDefinitions(ctx, nil, &sgc.GameConfigId, nil)
	sgcActions, _ := app.grpc.ListActionDefinitions(ctx, nil, nil, &sgcID)

	allActions := append(append(gameActions, configActions...), sgcActions...)
	localActions, inheritedActions := app.categorizeActions(ctx, allActions, "server_game_config", sgcID)

	data := ActionsPageData{
		Title:            "Manage Actions - SGC",
		Active:           "servers",
		User:             user,
		DefinitionLevel:  "server_game_config",
		EntityID:         sgcID,
		EntityName:       fmt.Sprintf("%s / %s / SGC #%d", game.Name, config.Name, sgcID),
		LocalActions:     localActions,
		InheritedActions: inheritedActions,
		FieldTypes:       []string{"text", "number", "select", "textarea", "checkbox", "radio", "email", "url"},
		ButtonStyles:     []string{"primary", "secondary", "success", "danger", "warning", "info", "light", "dark"},
		IconOptions:      getIconOptions(),
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "actions_manage_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// categorizeActions separates actions into local (defined at this level) and inherited (from parent levels)
func (app *App) categorizeActions(ctx context.Context, actions []*manmanpb.ActionDefinition, currentLevel string, currentEntityID int64) ([]*ActionWithFields, []*ActionWithFields) {
	var local, inherited []*ActionWithFields

	for _, action := range actions {
		// Fetch fields for this action
		actionDetail, fields, err := app.grpc.GetActionDefinition(ctx, action.ActionId)
		if err != nil {
			log.Printf("Error fetching action details: %v", err)
			continue
		}

		// Group fields with their options
		fieldsWithOptions := make([]*FieldWithOptions, 0)
		for _, field := range fields {
			// Collect options for this field (filter from action detail if available)
			options := make([]*manmanpb.ActionInputOption, 0)
			// Note: GetActionDefinition doesn't return options, so we'll fetch them separately if needed
			// For now, we'll work with what we have

			fieldsWithOptions = append(fieldsWithOptions, &FieldWithOptions{
				Field:   field,
				Options: options,
			})
		}

		actionWithFields := &ActionWithFields{
			Action: actionDetail,
			Fields: fieldsWithOptions,
		}

		// Check if this action is defined at the current level
		if action.DefinitionLevel == currentLevel && action.EntityId == currentEntityID {
			local = append(local, actionWithFields)
		} else {
			inherited = append(inherited, actionWithFields)
		}
	}

	return local, inherited
}

// handleActionMutation handles create/update/delete operations for actions
func (app *App) handleActionMutation(w http.ResponseWriter, r *http.Request, level string, entityID int64) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	operation := r.FormValue("operation")

	switch operation {
	case "create":
		app.handleActionCreate(w, r, level, entityID)
	case "update":
		app.handleActionUpdate(w, r, level, entityID)
	case "delete":
		app.handleActionDelete(w, r)
	default:
		http.Error(w, "Invalid operation", http.StatusBadRequest)
	}
}

// handleActionCreate creates a new action
func (app *App) handleActionCreate(w http.ResponseWriter, r *http.Request, level string, entityID int64) {
	ctx := context.Background()

	// Parse action fields from form
	action := &manmanpb.ActionDefinition{
		DefinitionLevel:      level,
		EntityId:             entityID,
		Name:                 r.FormValue("name"),
		Label:                r.FormValue("label"),
		Description:          r.FormValue("description"),
		CommandTemplate:      r.FormValue("command_template"),
		DisplayOrder:         int32(parseIntOrZero(r.FormValue("display_order"))),
		GroupName:            r.FormValue("group_name"),
		ButtonStyle:          r.FormValue("button_style"),
		Icon:                 r.FormValue("icon"),
		RequiresConfirmation: r.FormValue("requires_confirmation") == "true",
		ConfirmationMessage:  r.FormValue("confirmation_message"),
		Enabled:              r.FormValue("enabled") != "false", // default to true
	}

	// Parse input fields from JSON
	var fields []*manmanpb.ActionInputField
	if fieldsJSON := r.FormValue("input_fields"); fieldsJSON != "" {
		if err := json.Unmarshal([]byte(fieldsJSON), &fields); err != nil {
			log.Printf("Error parsing input fields: %v", err)
			http.Error(w, "Invalid input fields format", http.StatusBadRequest)
			return
		}
	}

	// Parse options from JSON
	var options []*manmanpb.ActionInputOption
	if optionsJSON := r.FormValue("input_options"); optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil {
			log.Printf("Error parsing options: %v", err)
			http.Error(w, "Invalid options format", http.StatusBadRequest)
			return
		}
	}

	// Create the action
	actionID, err := app.grpc.CreateActionDefinition(ctx, action, fields, options)
	if err != nil {
		log.Printf("Error creating action: %v", err)
		http.Error(w, fmt.Sprintf("Failed to create action: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Created action #%d: %s", actionID, action.Name)

	// Return success (HTMX will handle redirect/refresh)
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// handleActionUpdate updates an existing action
func (app *App) handleActionUpdate(w http.ResponseWriter, r *http.Request, level string, entityID int64) {
	ctx := context.Background()

	actionID := parseIntOrZero(r.FormValue("action_id"))
	if actionID == 0 {
		http.Error(w, "Missing action_id", http.StatusBadRequest)
		return
	}

	// Parse action fields from form
	action := &manmanpb.ActionDefinition{
		ActionId:             int64(actionID),
		DefinitionLevel:      level,
		EntityId:             entityID,
		Name:                 r.FormValue("name"),
		Label:                r.FormValue("label"),
		Description:          r.FormValue("description"),
		CommandTemplate:      r.FormValue("command_template"),
		DisplayOrder:         int32(parseIntOrZero(r.FormValue("display_order"))),
		GroupName:            r.FormValue("group_name"),
		ButtonStyle:          r.FormValue("button_style"),
		Icon:                 r.FormValue("icon"),
		RequiresConfirmation: r.FormValue("requires_confirmation") == "true",
		ConfirmationMessage:  r.FormValue("confirmation_message"),
		Enabled:              r.FormValue("enabled") != "false",
	}

	// Parse input fields from JSON
	var fields []*manmanpb.ActionInputField
	if fieldsJSON := r.FormValue("input_fields"); fieldsJSON != "" {
		if err := json.Unmarshal([]byte(fieldsJSON), &fields); err != nil {
			log.Printf("Error parsing input fields: %v", err)
			http.Error(w, "Invalid input fields format", http.StatusBadRequest)
			return
		}
	}

	// Parse options from JSON
	var options []*manmanpb.ActionInputOption
	if optionsJSON := r.FormValue("input_options"); optionsJSON != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil {
			log.Printf("Error parsing options: %v", err)
			http.Error(w, "Invalid options format", http.StatusBadRequest)
			return
		}
	}

	// Update the action
	err := app.grpc.UpdateActionDefinition(ctx, action, fields, options)
	if err != nil {
		log.Printf("Error updating action: %v", err)
		http.Error(w, fmt.Sprintf("Failed to update action: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Updated action #%d: %s", actionID, action.Name)

	// Return success (HTMX will handle redirect/refresh)
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// handleActionDelete deletes an action
func (app *App) handleActionDelete(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	actionID := parseIntOrZero(r.FormValue("action_id"))
	if actionID == 0 {
		http.Error(w, "Missing action_id", http.StatusBadRequest)
		return
	}

	err := app.grpc.DeleteActionDefinition(ctx, int64(actionID))
	if err != nil {
		log.Printf("Error deleting action: %v", err)
		http.Error(w, fmt.Sprintf("Failed to delete action: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("Deleted action #%d", actionID)

	// Return success (HTMX will handle redirect/refresh)
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// parseIntOrZero parses a string to int, returning 0 on error
func parseIntOrZero(s string) int {
	val, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return val
}
