package manman

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// JSONB is a custom type for PostgreSQL JSONB columns
type JSONB map[string]interface{}

// Value implements the driver.Valuer interface
func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Scan implements the sql.Scanner interface
func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(bytes, j)
}

// Server represents a physical/virtual machine running the host manager
type Server struct {
	ServerID    int64      `db:"server_id"`
	Name        string     `db:"name"`
	Status      string     `db:"status"`
	Environment *string    `db:"environment"`
	LastSeen    *time.Time `db:"last_seen"`
	IsDefault   bool       `db:"is_default"`
}

// Game represents a game definition (e.g., Minecraft, Valheim)
type Game struct {
	GameID     int64   `db:"game_id"`
	Name       string  `db:"name"`
	SteamAppID *string `db:"steam_app_id"`
	Metadata   JSONB   `db:"metadata"`
}

// GameConfig represents a preset/template for running a game
type GameConfig struct {
	ConfigID     int64   `db:"config_id"`
	GameID       int64   `db:"game_id"`
	Name         string  `db:"name"`
	Image        string  `db:"image"`
	ArgsTemplate *string `db:"args_template"`
	EnvTemplate  JSONB   `db:"env_template"`
	Files        JSONB   `db:"files"`
	Parameters   JSONB   `db:"parameters"`
	Entrypoint     JSONB   `db:"entrypoint"` // []string stored as JSONB
	Command        JSONB   `db:"command"`    // []string stored as JSONB
}

// ServerGameConfig represents a game configuration deployed on a specific server
type ServerGameConfig struct {
	SGCID        int64  `db:"sgc_id"`
	ServerID     int64  `db:"server_id"`
	GameConfigID int64  `db:"game_config_id"`
	PortBindings JSONB  `db:"port_bindings"`
	Parameters   JSONB  `db:"parameters"`
	Status       string `db:"status"`
}

// Session represents an execution of a ServerGameConfig
type Session struct {
	SessionID            int64      `db:"session_id"`
	SGCID                int64      `db:"sgc_id"`
	StartedAt            *time.Time `db:"started_at"`
	EndedAt              *time.Time `db:"ended_at"`
	ExitCode             *int       `db:"exit_code"`
	Status               string     `db:"status"`
	Parameters           JSONB      `db:"parameters"`
	RestoredFromBackupID *int64     `db:"restored_from_backup_id"`
	CreatedAt            time.Time  `db:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"`
}

// ServerPort represents port allocation tracking at server level
type ServerPort struct {
	ServerID    int64     `db:"server_id"`
	Port        int       `db:"port"`
	Protocol    string    `db:"protocol"`
	SGCID       *int64    `db:"sgc_id"`
	SessionID   *int64    `db:"session_id"`
	AllocatedAt time.Time `db:"allocated_at"`
}

// PortBinding represents a container-to-host port mapping
type PortBinding struct {
	ContainerPort int32  `json:"container_port"`
	HostPort      int32  `json:"host_port"`
	Protocol      string `json:"protocol"` // "TCP" | "UDP"
}

// ServerCapability represents the resources available on a server
type ServerCapability struct {
	CapabilityID           int64      `db:"capability_id"`
	ServerID               int64      `db:"server_id"`
	TotalMemoryMB          int32      `db:"total_memory_mb"`
	AvailableMemoryMB      int32      `db:"available_memory_mb"`
	CPUCores               int32      `db:"cpu_cores"`
	AvailableCPUMillicores int32      `db:"available_cpu_millicores"`
	DockerVersion          string     `db:"docker_version"`
	RecordedAt             *time.Time `db:"recorded_at"`
}

// LogReference represents a reference to a log file for a session
type LogReference struct {
	LogID            int64      `db:"log_id"`
	SessionID        int64      `db:"session_id"`
	SGCID            *int64     `db:"sgc_id"`
	FilePath         string     `db:"file_path"`
	StartTime        time.Time  `db:"start_time"`
	EndTime          time.Time  `db:"end_time"`
	LineCount        int32      `db:"line_count"`
	Source           string     `db:"source"`
	MinuteTimestamp  *time.Time `db:"minute_timestamp"`
	State            string     `db:"state"`
	AppendedAt       *time.Time `db:"appended_at"`
	CreatedAt        time.Time  `db:"created_at"`
}

// Backup represents a backup of game save data for a session
type Backup struct {
	BackupID            int64     `db:"backup_id"`
	SessionID           int64     `db:"session_id"`
	ServerGameConfigID  int64     `db:"server_game_config_id"`
	S3URL               string    `db:"s3_url"`
	SizeBytes           int64     `db:"size_bytes"`
	Description         *string   `db:"description"`
	CreatedAt           time.Time `db:"created_at"`
}

// ============================================================================
// Normalized Parameter Schema Models
// ============================================================================

// ParameterDefinition defines a parameter for a game
type ParameterDefinition struct {
	ParamID       int64     `db:"param_id"`
	GameID        int64     `db:"game_id"`
	Key           string    `db:"key"`
	ParamType     string    `db:"param_type"`
	Description   *string   `db:"description"`
	Required      bool      `db:"required"`
	DefaultValue  *string   `db:"default_value"`
	MinValue      *int64    `db:"min_value"`
	MaxValue      *int64    `db:"max_value"`
	AllowedValues *[]string `db:"allowed_values"` // PostgreSQL text[] array
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

// GameConfigParameterValue stores a parameter value for a GameConfig
type GameConfigParameterValue struct {
	ValueID   int64     `db:"value_id"`
	ConfigID  int64     `db:"config_id"`
	ParamID   int64     `db:"param_id"`
	Value     string    `db:"value"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// ServerGameConfigParameterValue stores a parameter value override for a ServerGameConfig
type ServerGameConfigParameterValue struct {
	ValueID   int64     `db:"value_id"`
	SGCID     int64     `db:"sgc_id"`
	ParamID   int64     `db:"param_id"`
	Value     string    `db:"value"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// SessionParameterValue stores a parameter value override for a Session
type SessionParameterValue struct {
	ValueID   int64     `db:"value_id"`
	SessionID int64     `db:"session_id"`
	ParamID   int64     `db:"param_id"`
	Value     string    `db:"value"`
	CreatedAt time.Time `db:"created_at"`
}

// ============================================================================
// Configuration Strategy System Models
// ============================================================================

// ConfigurationStrategy defines how to render configuration for a game
type ConfigurationStrategy struct {
	StrategyID    int64     `db:"strategy_id"`
	GameID        int64     `db:"game_id"`
	Name          string    `db:"name"`
	Description   *string   `db:"description"`
	StrategyType  string    `db:"strategy_type"`
	TargetPath    *string   `db:"target_path"`
	BaseTemplate  *string   `db:"base_template"`
	RenderOptions JSONB     `db:"render_options"`
	ApplyOrder    int       `db:"apply_order"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

// StrategyParameterBinding links parameters to configuration strategies
type StrategyParameterBinding struct {
	BindingID      int64     `db:"binding_id"`
	StrategyID     int64     `db:"strategy_id"`
	ParamID        int64     `db:"param_id"`
	BindingType    string    `db:"binding_type"`
	TargetKey      string    `db:"target_key"`
	ValueTemplate  *string   `db:"value_template"`
	ConditionExpr  *string   `db:"condition_expr"`
	CreatedAt      time.Time `db:"created_at"`
}

// ConfigurationPatch stores configuration overrides at different levels
type ConfigurationPatch struct {
	PatchID      int64     `db:"patch_id"`
	StrategyID   int64     `db:"strategy_id"`
	PatchLevel   string    `db:"patch_level"`
	EntityID     int64     `db:"entity_id"`
	PatchContent *string   `db:"patch_content"`
	PatchFormat  string    `db:"patch_format"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// ActionDefinition defines an action that can be executed on a game session
type ActionDefinition struct {
	ActionID             int64      `db:"action_id"`
	GameID               int64      `db:"game_id"`
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

// ActionVisibilityOverride controls action visibility at different configuration levels
type ActionVisibilityOverride struct {
	OverrideID    int64     `db:"override_id"`
	ActionID      int64     `db:"action_id"`
	OverrideLevel string    `db:"override_level"`
	EntityID      int64     `db:"entity_id"`
	Enabled       bool      `db:"enabled"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
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

// Status constants
const (
	ServerStatusOnline  = "online"
	ServerStatusOffline = "offline"

	SGCStatusActive   = "active"
	SGCStatusInactive = "inactive"

	SessionStatusPending   = "pending"
	SessionStatusStarting  = "starting"
	SessionStatusRunning   = "running"
	SessionStatusStopping  = "stopping"
	SessionStatusStopped   = "stopped"
	SessionStatusCrashed   = "crashed"
	SessionStatusLost      = "lost"
	SessionStatusCompleted = "completed"

	ProtocolTCP = "TCP"
	ProtocolUDP = "UDP"

	// Parameter types
	ParamTypeString = "string"
	ParamTypeInt    = "int"
	ParamTypeBool   = "bool"
	ParamTypeSecret = "secret"

	// Configuration strategy types
	StrategyTypeCLIArgs        = "cli_args"
	StrategyTypeEnvVars        = "env_vars"
	StrategyTypeFileProperties = "file_properties"
	StrategyTypeFileJSON       = "file_json"
	StrategyTypeFileYAML       = "file_yaml"
	StrategyTypeFileINI        = "file_ini"
	StrategyTypeFileXML        = "file_xml"
	StrategyTypeFileLua        = "file_lua"
	StrategyTypeFileCustom     = "file_custom"
	StrategyTypeVolume         = "volume"

	// Binding types
	BindingTypeDirect     = "direct"
	BindingTypeTemplate   = "template"
	BindingTypeJSONPath   = "json_path"
	BindingTypeXPath      = "xpath"
	BindingTypeINISection = "ini_section"

	// Patch levels
	PatchLevelGameConfig       = "game_config"
	PatchLevelServerGameConfig = "server_game_config"
	PatchLevelSession          = "session"

	// Patch formats
	PatchFormatTemplate       = "template"
	PatchFormatJSONMergePatch = "json_merge_patch"
	PatchFormatJSONPatch      = "json_patch"
	PatchFormatYAMLMerge      = "yaml_merge"

	// Log archival states
	LogStateComplete = "complete"
	LogStatePending  = "pending"

	// Action field types
	FieldTypeText     = "text"
	FieldTypeNumber   = "number"
	FieldTypeSelect   = "select"
	FieldTypeTextarea = "textarea"
	FieldTypeCheckbox = "checkbox"
	FieldTypeRadio    = "radio"
	FieldTypeEmail    = "email"
	FieldTypeURL      = "url"

	// Action button styles
	ButtonStylePrimary   = "primary"
	ButtonStyleSecondary = "secondary"
	ButtonStyleSuccess   = "success"
	ButtonStyleDanger    = "danger"
	ButtonStyleWarning   = "warning"
	ButtonStyleInfo      = "info"
	ButtonStyleLight     = "light"
	ButtonStyleDark      = "dark"

	// Action execution statuses
	ActionStatusSuccess         = "success"
	ActionStatusFailed          = "failed"
	ActionStatusValidationError = "validation_error"

	// Action visibility override levels
	OverrideLevelGameConfig       = "game_config"
	OverrideLevelServerGameConfig = "server_game_config"
	OverrideLevelSession          = "session"
)

// IsActive returns true if the session is in an active state (not completed or stopped)
// Note: crashed and lost are still considered active for management purposes
func (s Session) IsActive() bool {
	switch s.Status {
	case SessionStatusPending, SessionStatusStarting, SessionStatusRunning,
		SessionStatusStopping, SessionStatusCrashed, SessionStatusLost:
		return true
	default:
		return false
	}
}

// IsAvailable returns true if the session is running and ready for connections
func (s Session) IsAvailable() bool {
	return s.Status == SessionStatusRunning
}
