package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	pb "github.com/whale-net/everything/manman/protos"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// ActionsPageData holds data for the actions management page
type ActionsPageData struct {
	User *htmxauth.UserInfo

	// Current level being viewed
	Level             string // "game", "game_config", "server_game_config"
	EntityID          int64
	EntityName        string
	CurrentLevelLabel string

	// Hierarchy IDs for tab navigation
	GameID   int64
	ConfigID int64
	SGCID    int64

	// Parent names for breadcrumbs
	GameName string

	// Navigation
	BackLink      string
	BackLinkLabel string

	// Actions at the current level
	CurrentActions []*pb.ActionDefinition

	// Inherited actions from higher levels (displayed greyed out)
	InheritedSections []ActionSection
}

// ActionSection represents a group of actions from a specific level
type ActionSection struct {
	Title      string
	Level      string
	LevelLabel string
	Actions    []*pb.ActionDefinition
}

// handleActionsPage renders the actions management page for any level
func (app *App) handleActionsPage(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if app.grpc == nil {
		http.Error(w, "Actions API not configured", http.StatusServiceUnavailable)
		return
	}

	// Parse URL: /actions/{level}/{id}
	path := strings.TrimPrefix(r.URL.Path, "/actions/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid URL format. Expected /actions/{level}/{id}", http.StatusBadRequest)
		return
	}

	levelKey := parts[0]
	entityIDStr := parts[1]
	entityID, err := strconv.ParseInt(entityIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid entity ID", http.StatusBadRequest)
		return
	}

	// Map URL level key to definition_level
	var level string
	switch levelKey {
	case "game":
		level = "game"
	case "config":
		level = "game_config"
	case "sgc":
		level = "server_game_config"
	default:
		http.Error(w, "Invalid level. Must be game, config, or sgc", http.StatusBadRequest)
		return
	}

	data := ActionsPageData{
		User:     user,
		Level:    level,
		EntityID: entityID,
	}

	// Resolve entity hierarchy based on level
	switch level {
	case "game":
		game, err := app.grpc.GetGame(r.Context(), entityID)
		if err != nil {
			slog.Error("failed to get game", "error", err)
			http.Error(w, "Game not found", http.StatusNotFound)
			return
		}
		data.EntityName = game.Name
		data.GameID = game.GameId
		data.GameName = game.Name
		data.CurrentLevelLabel = "Game"
		data.BackLink = "/gameservers"
		data.BackLinkLabel = "Game Server Types"

	case "game_config":
		config, err := app.grpc.GetGameConfig(r.Context(), entityID)
		if err != nil {
			slog.Error("failed to get game config", "error", err)
			http.Error(w, "Game config not found", http.StatusNotFound)
			return
		}
		data.EntityName = config.Name
		data.ConfigID = config.ConfigId
		data.GameID = config.GameId
		data.CurrentLevelLabel = "Config"
		data.BackLink = fmt.Sprintf("/gameserver/%d", config.GameId)
		data.BackLinkLabel = "Game Server"

		// Get parent game name
		game, err := app.grpc.GetGame(r.Context(), config.GameId)
		if err == nil && game != nil {
			data.GameName = game.Name
		}

		// Fetch inherited game-level actions
		gameActions, err := app.grpc.ListActionDefinitions(r.Context(), "game", config.GameId)
		if err != nil {
			slog.Error("failed to get game actions", "error", err)
		} else if len(gameActions) > 0 {
			data.InheritedSections = append(data.InheritedSections, ActionSection{
				Title:      fmt.Sprintf("Game: %s", data.GameName),
				Level:      "game",
				LevelLabel: "Game",
				Actions:    gameActions,
			})
		}

	case "server_game_config":
		sgc, err := app.grpc.GetServerGameConfig(r.Context(), entityID)
		if err != nil {
			slog.Error("failed to get server game config", "error", err)
			http.Error(w, "Server game config not found", http.StatusNotFound)
			return
		}
		data.SGCID = sgc.ServerGameConfigId
		data.ConfigID = sgc.GameConfigId
		data.EntityName = fmt.Sprintf("SGC #%d", sgc.ServerGameConfigId)
		data.CurrentLevelLabel = "Server Config"
		data.BackLink = "/gameservers"
		data.BackLinkLabel = "Game Server Types"

		// Get parent config
		config, err := app.grpc.GetGameConfig(r.Context(), sgc.GameConfigId)
		if err == nil && config != nil {
			data.EntityName = fmt.Sprintf("SGC #%d (%s)", sgc.ServerGameConfigId, config.Name)
			data.GameID = config.GameId
			data.BackLink = fmt.Sprintf("/gameserver/%d", config.GameId)
			data.BackLinkLabel = "Game Server"

			// Get parent game name
			game, err := app.grpc.GetGame(r.Context(), config.GameId)
			if err == nil && game != nil {
				data.GameName = game.Name
			}

			// Fetch inherited game-level actions
			gameActions, err := app.grpc.ListActionDefinitions(r.Context(), "game", config.GameId)
			if err != nil {
				slog.Error("failed to get game actions", "error", err)
			} else if len(gameActions) > 0 {
				data.InheritedSections = append(data.InheritedSections, ActionSection{
					Title:      fmt.Sprintf("Game: %s", data.GameName),
					Level:      "game",
					LevelLabel: "Game",
					Actions:    gameActions,
				})
			}

			// Fetch inherited config-level actions
			configActions, err := app.grpc.ListActionDefinitions(r.Context(), "game_config", config.ConfigId)
			if err != nil {
				slog.Error("failed to get config actions", "error", err)
			} else if len(configActions) > 0 {
				data.InheritedSections = append(data.InheritedSections, ActionSection{
					Title:      fmt.Sprintf("Config: %s", config.Name),
					Level:      "game_config",
					LevelLabel: "Config",
					Actions:    configActions,
				})
			}
		}
	}

	// Fetch current level actions
	currentActions, err := app.grpc.ListActionDefinitions(r.Context(), level, entityID)
	if err != nil {
		slog.Error("failed to get current level actions", "error", err, "level", level, "entity_id", entityID)
		currentActions = []*pb.ActionDefinition{}
	}
	data.CurrentActions = currentActions

	if err := templates.ExecuteTemplate(w, "actions.html", data); err != nil {
		slog.Error("template error", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleCreateAction handles creating a new action definition
func (app *App) handleCreateAction(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if app.grpc == nil {
		http.Error(w, "Actions API not configured", http.StatusServiceUnavailable)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	level := r.FormValue("level")
	entityIDStr := r.FormValue("entity_id")
	entityID, err := strconv.ParseInt(entityIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid entity ID", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	label := r.FormValue("label")
	if name == "" || label == "" {
		http.Error(w, "Name and label are required", http.StatusBadRequest)
		return
	}

	displayOrder, _ := strconv.Atoi(r.FormValue("display_order"))

	action := &pb.ActionDefinition{
		DefinitionLevel:      level,
		EntityId:             entityID,
		Name:                 name,
		Label:                label,
		Description:          r.FormValue("description"),
		CommandTemplate:      r.FormValue("command_template"),
		DisplayOrder:         int32(displayOrder),
		GroupName:            r.FormValue("group_name"),
		ButtonStyle:          r.FormValue("button_style"),
		Icon:                 r.FormValue("icon"),
		RequiresConfirmation: r.FormValue("requires_confirmation") == "on",
		ConfirmationMessage:  r.FormValue("confirmation_message"),
		Enabled:              r.FormValue("enabled") == "on",
	}

	// Parse input fields
	fieldCountStr := r.FormValue("field_count")
	fieldCount, _ := strconv.Atoi(fieldCountStr)

	var fields []*pb.ActionInputField
	for i := 1; i <= fieldCount; i++ {
		fieldName := r.FormValue(fmt.Sprintf("field_name_%d", i))
		if fieldName == "" {
			continue // Field was removed
		}

		field := &pb.ActionInputField{
			Name:         fieldName,
			Label:        r.FormValue(fmt.Sprintf("field_label_%d", i)),
			FieldType:    r.FormValue(fmt.Sprintf("field_type_%d", i)),
			Placeholder:  r.FormValue(fmt.Sprintf("field_placeholder_%d", i)),
			DefaultValue: r.FormValue(fmt.Sprintf("field_default_%d", i)),
			Required:     r.FormValue(fmt.Sprintf("field_required_%d", i)) == "on",
			DisplayOrder: int32(i),
		}
		fields = append(fields, field)
	}

	slog.Info("user creating action", "user", user.Email, "level", level, "entity_id", entityID, "name", name)
	_, err = app.grpc.CreateActionDefinition(r.Context(), action, fields, nil)
	if err != nil {
		slog.Error("failed to create action", "error", err, "user", user.Email)
		http.Error(w, fmt.Sprintf("Failed to create action: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("action created", "level", level, "entity_id", entityID, "name", name, "user", user.Email)
	w.Header().Set("HX-Trigger", "actionCreated")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Action created successfully")
}

// handleDeleteAction handles deleting an action definition
func (app *App) handleDeleteAction(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if app.grpc == nil {
		http.Error(w, "Actions API not configured", http.StatusServiceUnavailable)
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract action ID from URL: /api/actions/{id}
	actionIDStr := strings.TrimPrefix(r.URL.Path, "/api/actions/")
	actionID, err := strconv.ParseInt(actionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid action ID", http.StatusBadRequest)
		return
	}

	slog.Info("user deleting action", "user", user.Email, "action_id", actionID)
	err = app.grpc.DeleteActionDefinition(r.Context(), actionID)
	if err != nil {
		slog.Error("failed to delete action", "error", err, "action_id", actionID, "user", user.Email)
		http.Error(w, fmt.Sprintf("Failed to delete action: %v", err), http.StatusInternalServerError)
		return
	}

	slog.Info("action deleted", "action_id", actionID, "user", user.Email)
	w.Header().Set("HX-Trigger", "actionDeleted")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<div class="success-message">Action deleted successfully</div>`)
}
