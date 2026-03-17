package manman

import (
	"database/sql/driver"
	"encoding/json"
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

// PortBinding represents a container-to-host port mapping
type PortBinding struct {
	ContainerPort int32  `json:"container_port"`
	HostPort      int32  `json:"host_port"`
	Protocol      string `json:"protocol"` // "TCP" | "UDP"
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

	// Action definition levels (like patches)
	ActionLevelGame             = "game"
	ActionLevelGameConfig       = "game_config"
	ActionLevelServerGameConfig = "server_game_config"

	// Backup statuses
	BackupStatusPending   = "pending"
	BackupStatusRunning   = "running"
	BackupStatusCompleted = "completed"
	BackupStatusFailed    = "failed"

	// Workshop installation statuses
	InstallationStatusPending     = "pending"
	InstallationStatusDownloading = "downloading"
	InstallationStatusInstalled   = "installed"
	InstallationStatusFailed      = "failed"
	InstallationStatusRemoved     = "removed"

	// Workshop platform types
	PlatformTypeSteamWorkshop = "steam_workshop"
)
