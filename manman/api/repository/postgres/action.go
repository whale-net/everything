package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manman"
)

type ActionRepository struct {
	db *pgxpool.Pool
}

func NewActionRepository(db *pgxpool.Pool) *ActionRepository {
	return &ActionRepository{db: db}
}

// Get retrieves an action definition with its input fields and options
func (r *ActionRepository) Get(ctx context.Context, actionID int64) (*manman.ActionDefinition, []*ActionInputFieldWithOptions, error) {
	// Get the action definition
	action := &manman.ActionDefinition{}
	query := `
		SELECT action_id, definition_level, entity_id, name, label, description, command_template,
		       display_order, group_name, button_style, icon, requires_confirmation,
		       confirmation_message, enabled, created_at, updated_at
		FROM action_definitions
		WHERE action_id = $1
	`

	err := r.db.QueryRow(ctx, query, actionID).Scan(
		&action.ActionID,
		&action.DefinitionLevel,
		&action.EntityID,
		&action.Name,
		&action.Label,
		&action.Description,
		&action.CommandTemplate,
		&action.DisplayOrder,
		&action.GroupName,
		&action.ButtonStyle,
		&action.Icon,
		&action.RequiresConfirmation,
		&action.ConfirmationMessage,
		&action.Enabled,
		&action.CreatedAt,
		&action.UpdatedAt,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get action: %w", err)
	}

	// Get input fields with options
	fields, err := r.getInputFieldsWithOptions(ctx, actionID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get input fields: %w", err)
	}

	return action, fields, nil
}

// ActionInputFieldWithOptions wraps an input field with its options
type ActionInputFieldWithOptions struct {
	Field   *manman.ActionInputField
	Options []*manman.ActionInputOption
}

// getInputFieldsWithOptions retrieves all input fields and their options for an action
func (r *ActionRepository) getInputFieldsWithOptions(ctx context.Context, actionID int64) ([]*ActionInputFieldWithOptions, error) {
	// Get input fields
	fieldsQuery := `
		SELECT field_id, action_id, name, label, field_type, required,
		       placeholder, help_text, default_value, display_order,
		       pattern, min_value, max_value, min_length, max_length,
		       created_at, updated_at
		FROM action_input_fields
		WHERE action_id = $1
		ORDER BY display_order, field_id
	`

	rows, err := r.db.Query(ctx, fieldsQuery, actionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*ActionInputFieldWithOptions
	for rows.Next() {
		field := &manman.ActionInputField{}
		err := rows.Scan(
			&field.FieldID,
			&field.ActionID,
			&field.Name,
			&field.Label,
			&field.FieldType,
			&field.Required,
			&field.Placeholder,
			&field.HelpText,
			&field.DefaultValue,
			&field.DisplayOrder,
			&field.Pattern,
			&field.MinValue,
			&field.MaxValue,
			&field.MinLength,
			&field.MaxLength,
			&field.CreatedAt,
			&field.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		// Get options for this field
		options, err := r.getFieldOptions(ctx, field.FieldID)
		if err != nil {
			return nil, err
		}

		results = append(results, &ActionInputFieldWithOptions{
			Field:   field,
			Options: options,
		})
	}

	return results, rows.Err()
}

// getFieldOptions retrieves all options for an input field
func (r *ActionRepository) getFieldOptions(ctx context.Context, fieldID int64) ([]*manman.ActionInputOption, error) {
	query := `
		SELECT option_id, field_id, value, label, display_order, is_default,
		       created_at, updated_at
		FROM action_input_options
		WHERE field_id = $1
		ORDER BY display_order, option_id
	`

	rows, err := r.db.Query(ctx, query, fieldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var options []*manman.ActionInputOption
	for rows.Next() {
		option := &manman.ActionInputOption{}
		err := rows.Scan(
			&option.OptionID,
			&option.FieldID,
			&option.Value,
			&option.Label,
			&option.DisplayOrder,
			&option.IsDefault,
			&option.CreatedAt,
			&option.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		options = append(options, option)
	}

	return options, rows.Err()
}

// ListByGame retrieves all enabled actions for a game (only game-level actions)
func (r *ActionRepository) ListByGame(ctx context.Context, gameID int64) ([]*manman.ActionDefinition, error) {
	query := `
		SELECT action_id, definition_level, entity_id, name, label, description, command_template,
		       display_order, group_name, button_style, icon, requires_confirmation,
		       confirmation_message, enabled, created_at, updated_at
		FROM action_definitions
		WHERE definition_level = 'game' AND entity_id = $1 AND enabled = true
		ORDER BY display_order, action_id
	`

	rows, err := r.db.Query(ctx, query, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actions []*manman.ActionDefinition
	for rows.Next() {
		action := &manman.ActionDefinition{}
		err := rows.Scan(
			&action.ActionID,
			&action.DefinitionLevel,
			&action.EntityID,
			&action.Name,
			&action.Label,
			&action.Description,
			&action.CommandTemplate,
			&action.DisplayOrder,
			&action.GroupName,
			&action.ButtonStyle,
			&action.Icon,
			&action.RequiresConfirmation,
			&action.ConfirmationMessage,
			&action.Enabled,
			&action.CreatedAt,
			&action.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}

	return actions, rows.Err()
}

// GetSessionActions retrieves actions for a session from all levels (game, config, sgc)
// Similar to how patches are merged: game baseline + config additions + sgc additions
func (r *ActionRepository) GetSessionActions(ctx context.Context, sessionID int64) ([]*manman.ActionDefinition, error) {
	query := `
		WITH session_info AS (
			SELECT s.session_id, s.sgc_id, sgc.game_config_id, gc.game_id
			FROM sessions s
			JOIN server_game_configs sgc ON s.sgc_id = sgc.sgc_id
			JOIN game_configs gc ON sgc.game_config_id = gc.config_id
			WHERE s.session_id = $1
		)
		SELECT
			ad.action_id, ad.definition_level, ad.entity_id, ad.name, ad.label, ad.description,
			ad.command_template, ad.display_order, ad.group_name,
			ad.button_style, ad.icon, ad.requires_confirmation,
			ad.confirmation_message, ad.enabled, ad.created_at, ad.updated_at
		FROM action_definitions ad
		JOIN session_info si ON (
			-- Game-level actions
			(ad.definition_level = 'game' AND ad.entity_id = si.game_id)
			-- GameConfig-level actions
			OR (ad.definition_level = 'game_config' AND ad.entity_id = si.game_config_id)
			-- ServerGameConfig-level actions
			OR (ad.definition_level = 'server_game_config' AND ad.entity_id = si.sgc_id)
		)
		WHERE ad.enabled = true
		ORDER BY
			-- Order by level (game first, then config, then sgc)
			CASE ad.definition_level
				WHEN 'game' THEN 1
				WHEN 'game_config' THEN 2
				WHEN 'server_game_config' THEN 3
			END,
			ad.display_order,
			ad.action_id
	`

	rows, err := r.db.Query(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session actions: %w", err)
	}
	defer rows.Close()

	var actions []*manman.ActionDefinition
	for rows.Next() {
		action := &manman.ActionDefinition{}
		err := rows.Scan(
			&action.ActionID,
			&action.DefinitionLevel,
			&action.EntityID,
			&action.Name,
			&action.Label,
			&action.Description,
			&action.CommandTemplate,
			&action.DisplayOrder,
			&action.GroupName,
			&action.ButtonStyle,
			&action.Icon,
			&action.RequiresConfirmation,
			&action.ConfirmationMessage,
			&action.Enabled,
			&action.CreatedAt,
			&action.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}

	return actions, rows.Err()
}

// LogExecution records an action execution in the audit log
func (r *ActionRepository) LogExecution(ctx context.Context, execution *manman.ActionExecution) error {
	query := `
		INSERT INTO action_executions (
			action_id, session_id, triggered_by, input_values,
			rendered_command, status, error_message
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING execution_id, executed_at
	`

	err := r.db.QueryRow(ctx, query,
		execution.ActionID,
		execution.SessionID,
		execution.TriggeredBy,
		execution.InputValues,
		execution.RenderedCommand,
		execution.Status,
		execution.ErrorMessage,
	).Scan(&execution.ExecutionID, &execution.ExecutedAt)

	if err != nil {
		return fmt.Errorf("failed to log action execution: %w", err)
	}

	return nil
}

// GetExecutionHistory retrieves execution history for a session
func (r *ActionRepository) GetExecutionHistory(ctx context.Context, sessionID int64, limit int) ([]*manman.ActionExecution, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT execution_id, action_id, session_id, triggered_by, input_values,
		       rendered_command, status, error_message, executed_at
		FROM action_executions
		WHERE session_id = $1
		ORDER BY executed_at DESC
		LIMIT $2
	`

	rows, err := r.db.Query(ctx, query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []*manman.ActionExecution
	for rows.Next() {
		execution := &manman.ActionExecution{}
		err := rows.Scan(
			&execution.ExecutionID,
			&execution.ActionID,
			&execution.SessionID,
			&execution.TriggeredBy,
			&execution.InputValues,
			&execution.RenderedCommand,
			&execution.Status,
			&execution.ErrorMessage,
			&execution.ExecutedAt,
		)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}

	return executions, rows.Err()
}

// Create creates a new action definition with its input fields and options
func (r *ActionRepository) Create(ctx context.Context, action *manman.ActionDefinition, fields []*manman.ActionInputField, options []*manman.ActionInputOption) (int64, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Insert action definition
	query := `
		INSERT INTO action_definitions (
			definition_level, entity_id, name, label, description,
			command_template, display_order, group_name, button_style,
			icon, requires_confirmation, confirmation_message, enabled
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING action_id
	`

	var actionID int64
	err = tx.QueryRow(ctx, query,
		action.DefinitionLevel,
		action.EntityID,
		action.Name,
		action.Label,
		action.Description,
		action.CommandTemplate,
		action.DisplayOrder,
		action.GroupName,
		action.ButtonStyle,
		action.Icon,
		action.RequiresConfirmation,
		action.ConfirmationMessage,
		action.Enabled,
	).Scan(&actionID)
	if err != nil {
		return 0, fmt.Errorf("failed to insert action definition: %w", err)
	}

	// Insert input fields and options
	for _, field := range fields {
		fieldQuery := `
			INSERT INTO action_input_fields (
				action_id, name, label, field_type, required, placeholder,
				help_text, default_value, display_order, pattern,
				min_value, max_value, min_length, max_length
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING field_id
		`

		var fieldID int64
		err = tx.QueryRow(ctx, fieldQuery,
			actionID,
			field.Name,
			field.Label,
			field.FieldType,
			field.Required,
			field.Placeholder,
			field.HelpText,
			field.DefaultValue,
			field.DisplayOrder,
			field.Pattern,
			field.MinValue,
			field.MaxValue,
			field.MinLength,
			field.MaxLength,
		).Scan(&fieldID)
		if err != nil {
			return 0, fmt.Errorf("failed to insert input field: %w", err)
		}

		// Insert options for this field
		for _, option := range options {
			// Only insert options that belong to this field (matched by name)
			if option.FieldID == field.FieldID || option.FieldID == 0 {
				optionQuery := `
					INSERT INTO action_input_options (
						field_id, value, label, display_order, is_default
					)
					VALUES ($1, $2, $3, $4, $5)
				`
				_, err = tx.Exec(ctx, optionQuery,
					fieldID,
					option.Value,
					option.Label,
					option.DisplayOrder,
					option.IsDefault,
				)
				if err != nil {
					return 0, fmt.Errorf("failed to insert input option: %w", err)
				}
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return actionID, nil
}

// Update updates an action definition (not implemented yet - future work)
func (r *ActionRepository) Update(ctx context.Context, action *manman.ActionDefinition) error {
	return fmt.Errorf("update not implemented yet")
}

// Delete deletes an action definition
func (r *ActionRepository) Delete(ctx context.Context, actionID int64) error {
	query := `DELETE FROM action_definitions WHERE action_id = $1`
	_, err := r.db.Exec(ctx, query, actionID)
	if err != nil {
		return fmt.Errorf("failed to delete action: %w", err)
	}
	return nil
}

// ListByLevel retrieves all actions at a specific level and entity
func (r *ActionRepository) ListByLevel(ctx context.Context, level string, entityID int64) ([]*manman.ActionDefinition, error) {
	query := `
		SELECT action_id, definition_level, entity_id, name, label, description, command_template,
		       display_order, group_name, button_style, icon, requires_confirmation,
		       confirmation_message, enabled, created_at, updated_at
		FROM action_definitions
		WHERE definition_level = $1 AND entity_id = $2
		ORDER BY display_order, action_id
	`

	rows, err := r.db.Query(ctx, query, level, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actions []*manman.ActionDefinition
	for rows.Next() {
		action := &manman.ActionDefinition{}
		err := rows.Scan(
			&action.ActionID,
			&action.DefinitionLevel,
			&action.EntityID,
			&action.Name,
			&action.Label,
			&action.Description,
			&action.CommandTemplate,
			&action.DisplayOrder,
			&action.GroupName,
			&action.ButtonStyle,
			&action.Icon,
			&action.RequiresConfirmation,
			&action.ConfirmationMessage,
			&action.Enabled,
			&action.CreatedAt,
			&action.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}

	return actions, rows.Err()
}
