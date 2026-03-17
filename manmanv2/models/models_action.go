package manman

import "time"

// ActionDefinition defines an action that can be executed on a game session
type ActionDefinition struct {
	ActionID             int64      `db:"action_id"`
	DefinitionLevel      string     `db:"definition_level"` // 'game', 'game_config', 'server_game_config'
	EntityID             int64      `db:"entity_id"`        // game_id, config_id, or sgc_id
	Name                 string     `db:"name"`
	Label                string     `db:"label"`
	Description          *string    `db:"description"`
	CommandTemplate      string     `db:"command_template"`
	DisplayOrder         int        `db:"display_order"`
	GroupName            *string    `db:"group_name"`
	ButtonStyle          string     `db:"button_style"`
	Icon                 *string    `db:"icon"`
	RequiresConfirmation bool       `db:"requires_confirmation"`
	ConfirmationMessage  *string    `db:"confirmation_message"`
	Enabled              bool       `db:"enabled"`
	CreatedAt            time.Time  `db:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"`
}

// ActionInputField defines an input field for a parameterized action
type ActionInputField struct {
	FieldID      int64      `db:"field_id"`
	ActionID     int64      `db:"action_id"`
	Name         string     `db:"name"`
	Label        string     `db:"label"`
	FieldType    string     `db:"field_type"`
	Required     bool       `db:"required"`
	Placeholder  *string    `db:"placeholder"`
	HelpText     *string    `db:"help_text"`
	DefaultValue *string    `db:"default_value"`
	DisplayOrder int        `db:"display_order"`
	Pattern      *string    `db:"pattern"`
	MinValue     *float64   `db:"min_value"`
	MaxValue     *float64   `db:"max_value"`
	MinLength    *int       `db:"min_length"`
	MaxLength    *int       `db:"max_length"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

// ActionInputOption defines an option for select/radio input fields
type ActionInputOption struct {
	OptionID     int64     `db:"option_id"`
	FieldID      int64     `db:"field_id"`
	Value        string    `db:"value"`
	Label        string    `db:"label"`
	DisplayOrder int       `db:"display_order"`
	IsDefault    bool      `db:"is_default"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// ActionExecution records the execution of an action
type ActionExecution struct {
	ExecutionID     int64      `db:"execution_id"`
	ActionID        int64      `db:"action_id"`
	SessionID       int64      `db:"session_id"`
	TriggeredBy     *string    `db:"triggered_by"`
	InputValues     JSONB      `db:"input_values"`
	RenderedCommand string     `db:"rendered_command"`
	Status          string     `db:"status"`
	ErrorMessage    *string    `db:"error_message"`
	ExecutedAt      time.Time  `db:"executed_at"`
}
