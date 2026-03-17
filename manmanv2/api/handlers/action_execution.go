package handlers

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"text/template"
	"time"

	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ActionHandler handles game action-related RPCs
type ActionHandler struct {
	actionRepo  *postgres.ActionRepository
	sessionRepo repository.SessionRepository
	sgcRepo     repository.ServerGameConfigRepository
	gcRepo      repository.GameConfigRepository
	publisher   *CommandPublisher
}

func NewActionHandler(
	actionRepo *postgres.ActionRepository,
	sessionRepo repository.SessionRepository,
	sgcRepo repository.ServerGameConfigRepository,
	gcRepo repository.GameConfigRepository,
	publisher *CommandPublisher,
) *ActionHandler {
	return &ActionHandler{
		actionRepo:  actionRepo,
		sessionRepo: sessionRepo,
		sgcRepo:     sgcRepo,
		gcRepo:      gcRepo,
		publisher:   publisher,
	}
}

// ExecuteAction executes an action on a session
func (h *ActionHandler) ExecuteAction(ctx context.Context, req *pb.ExecuteActionRequest) (*pb.ExecuteActionResponse, error) {
	// Validate request
	if req.SessionId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.ActionId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "action_id is required")
	}

	// Get session to verify it exists and is running
	session, err := h.sessionRepo.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	if session.Status != manman.SessionStatusRunning {
		return nil, status.Errorf(codes.FailedPrecondition, "session is not running (status: %s)", session.Status)
	}

	// Get action definition with input fields
	action, fields, err := h.actionRepo.Get(ctx, req.ActionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "action not found: %v", err)
	}

	// Validate that action belongs to the session's game
	// Derive action's game_id from definition_level and entity_id
	var actionGameID int64
	switch action.DefinitionLevel {
	case "game":
		actionGameID = action.EntityID
	case "game_config":
		gc, err := h.gcRepo.Get(ctx, action.EntityID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get game config: %v", err)
		}
		actionGameID = gc.GameID
	case "server_game_config":
		sgcAction, err := h.sgcRepo.Get(ctx, action.EntityID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get server game config: %v", err)
		}
		gc, err := h.gcRepo.Get(ctx, sgcAction.GameConfigID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get game config: %v", err)
		}
		actionGameID = gc.GameID
	default:
		return nil, status.Errorf(codes.Internal, "unknown definition level: %s", action.DefinitionLevel)
	}

	// Get session's game_id
	sgc, err := h.sgcRepo.Get(ctx, session.SGCID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get server game config: %v", err)
	}

	gc, err := h.gcRepo.Get(ctx, sgc.GameConfigID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get game config: %v", err)
	}

	if actionGameID != gc.GameID {
		return nil, status.Errorf(codes.InvalidArgument, "action does not belong to session's game")
	}

	// Validate inputs
	if err := h.validateInputs(fields, req.InputValues); err != nil {
		execution := &manman.ActionExecution{
			ActionID:        req.ActionId,
			SessionID:       req.SessionId,
			InputValues:     convertToJSONB(req.InputValues),
			RenderedCommand: "",
			Status:          manman.ActionStatusValidationError,
			ErrorMessage:    strPtr(err.Error()),
		}
		_ = h.actionRepo.LogExecution(ctx, execution)

		return &pb.ExecuteActionResponse{
			Success:      false,
			ErrorMessage: err.Error(),
		}, nil
	}

	// Render command template
	renderedCommand, err := h.renderTemplate(action.CommandTemplate, req.InputValues)
	if err != nil {
		execution := &manman.ActionExecution{
			ActionID:        req.ActionId,
			SessionID:       req.SessionId,
			InputValues:     convertToJSONB(req.InputValues),
			RenderedCommand: "",
			Status:          manman.ActionStatusFailed,
			ErrorMessage:    strPtr(fmt.Sprintf("template rendering failed: %v", err)),
		}
		_ = h.actionRepo.LogExecution(ctx, execution)

		return &pb.ExecuteActionResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to render command template: %v", err),
		}, nil
	}

	// Send command to session via existing SendInput mechanism
	sendInputReq := &pb.SendInputRequest{
		SessionId: req.SessionId,
		Input:     []byte(renderedCommand + "\n"),
	}

	// We need to get the server ID for publishing the command
	sgcForCmd, err := h.sgcRepo.Get(ctx, session.SGCID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get server game config: %v", err)
	}

	cmd := map[string]interface{}{
		"type":       "send_input",
		"session_id": req.SessionId,
		"input":      sendInputReq.Input,
	}

	err = h.publisher.PublishSendInput(ctx, sgcForCmd.ServerID, cmd, 10*time.Second)
	if err != nil {
		execution := &manman.ActionExecution{
			ActionID:        req.ActionId,
			SessionID:       req.SessionId,
			InputValues:     convertToJSONB(req.InputValues),
			RenderedCommand: renderedCommand,
			Status:          manman.ActionStatusFailed,
			ErrorMessage:    strPtr(fmt.Sprintf("failed to send command: %v", err)),
		}
		_ = h.actionRepo.LogExecution(ctx, execution)

		return &pb.ExecuteActionResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to send command: %v", err),
		}, nil
	}

	// Log successful execution
	execution := &manman.ActionExecution{
		ActionID:        req.ActionId,
		SessionID:       req.SessionId,
		InputValues:     convertToJSONB(req.InputValues),
		RenderedCommand: renderedCommand,
		Status:          manman.ActionStatusSuccess,
	}

	err = h.actionRepo.LogExecution(ctx, execution)
	if err != nil {
		// Log error but don't fail the request - the command was sent successfully
		fmt.Printf("Warning: failed to log action execution: %v\n", err)
	}

	return &pb.ExecuteActionResponse{
		RenderedCommand: renderedCommand,
		Success:         true,
		ExecutionId:     execution.ExecutionID,
	}, nil
}

// validateInputs validates user-provided inputs against field definitions
func (h *ActionHandler) validateInputs(fields []*postgres.ActionInputFieldWithOptions, inputValues map[string]string) error {
	for _, fieldWithOptions := range fields {
		field := fieldWithOptions.Field
		value, provided := inputValues[field.Name]

		// Check required fields
		if field.Required && (!provided || value == "") {
			return fmt.Errorf("field '%s' is required", field.Label)
		}

		// Skip validation if not provided and not required
		if !provided || value == "" {
			continue
		}

		// Validate pattern
		if field.Pattern != nil && *field.Pattern != "" {
			matched, err := regexp.MatchString(*field.Pattern, value)
			if err != nil {
				return fmt.Errorf("invalid pattern for field '%s': %v", field.Label, err)
			}
			if !matched {
				return fmt.Errorf("field '%s' does not match required pattern", field.Label)
			}
		}

		// Validate numeric fields
		if field.FieldType == manman.FieldTypeNumber {
			numValue, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("field '%s' must be a number", field.Label)
			}

			if field.MinValue != nil && numValue < *field.MinValue {
				return fmt.Errorf("field '%s' must be at least %v", field.Label, *field.MinValue)
			}

			if field.MaxValue != nil && numValue > *field.MaxValue {
				return fmt.Errorf("field '%s' must be at most %v", field.Label, *field.MaxValue)
			}
		}

		// Validate string length
		if field.MinLength != nil && len(value) < *field.MinLength {
			return fmt.Errorf("field '%s' must be at least %d characters", field.Label, *field.MinLength)
		}

		if field.MaxLength != nil && len(value) > *field.MaxLength {
			return fmt.Errorf("field '%s' must be at most %d characters", field.Label, *field.MaxLength)
		}

		// Validate select/radio options
		if field.FieldType == manman.FieldTypeSelect || field.FieldType == manman.FieldTypeRadio {
			validOption := false
			for _, option := range fieldWithOptions.Options {
				if option.Value == value {
					validOption = true
					break
				}
			}
			if !validOption {
				return fmt.Errorf("field '%s' has an invalid value", field.Label)
			}
		}
	}

	return nil
}

// renderTemplate renders a Go template with the provided input values
func (h *ActionHandler) renderTemplate(templateStr string, inputs map[string]string) (string, error) {
	tmpl, err := template.New("action").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, inputs); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// strPtr returns a pointer to a string
func strPtr(s string) *string {
	return &s
}

// convertToJSONB converts a map[string]string to JSONB (map[string]interface{})
func convertToJSONB(m map[string]string) manman.JSONB {
	result := make(manman.JSONB, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// actionToProto converts an ActionDefinition model to protobuf
func (h *ActionHandler) actionToProto(a *manman.ActionDefinition) *pb.ActionDefinition {
	pbAction := &pb.ActionDefinition{
		ActionId:             a.ActionID,
		DefinitionLevel:      a.DefinitionLevel,
		EntityId:             a.EntityID,
		Name:                 a.Name,
		Label:                a.Label,
		CommandTemplate:      a.CommandTemplate,
		DisplayOrder:         int32(a.DisplayOrder),
		ButtonStyle:          a.ButtonStyle,
		RequiresConfirmation: a.RequiresConfirmation,
		Enabled:              a.Enabled,
		CreatedAt:            a.CreatedAt.Unix(),
		UpdatedAt:            a.UpdatedAt.Unix(),
	}
	if a.Description != nil {
		pbAction.Description = *a.Description
	}
	if a.GroupName != nil {
		pbAction.GroupName = *a.GroupName
	}
	if a.Icon != nil {
		pbAction.Icon = *a.Icon
	}
	if a.ConfirmationMessage != nil {
		pbAction.ConfirmationMessage = *a.ConfirmationMessage
	}
	return pbAction
}

// inputFieldToProto converts an ActionInputField model to protobuf
func (h *ActionHandler) inputFieldToProto(f *manman.ActionInputField) *pb.ActionInputField {
	pbField := &pb.ActionInputField{
		FieldId:      f.FieldID,
		ActionId:     f.ActionID,
		Name:         f.Name,
		Label:        f.Label,
		FieldType:    f.FieldType,
		Required:     f.Required,
		DisplayOrder: int32(f.DisplayOrder),
		CreatedAt:    f.CreatedAt.Unix(),
		UpdatedAt:    f.UpdatedAt.Unix(),
	}
	if f.Placeholder != nil {
		pbField.Placeholder = *f.Placeholder
	}
	if f.HelpText != nil {
		pbField.HelpText = *f.HelpText
	}
	if f.DefaultValue != nil {
		pbField.DefaultValue = *f.DefaultValue
	}
	if f.Pattern != nil {
		pbField.Pattern = *f.Pattern
	}
	if f.MinValue != nil {
		pbField.MinValue = *f.MinValue
	}
	if f.MaxValue != nil {
		pbField.MaxValue = *f.MaxValue
	}
	if f.MinLength != nil {
		pbField.MinLength = int32(*f.MinLength)
	}
	if f.MaxLength != nil {
		pbField.MaxLength = int32(*f.MaxLength)
	}
	return pbField
}

// inputOptionToProto converts an ActionInputOption model to protobuf
func (h *ActionHandler) inputOptionToProto(o *manman.ActionInputOption) *pb.ActionInputOption {
	return &pb.ActionInputOption{
		OptionId:     o.OptionID,
		FieldId:      o.FieldID,
		Value:        o.Value,
		Label:        o.Label,
		DisplayOrder: int32(o.DisplayOrder),
		IsDefault:    o.IsDefault,
		CreatedAt:    o.CreatedAt.Unix(),
		UpdatedAt:    o.UpdatedAt.Unix(),
	}
}
