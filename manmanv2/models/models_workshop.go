package manman

import "time"

// WorkshopAddon represents a workshop addon in the library
type WorkshopAddon struct {
	AddonID          int64      `db:"addon_id"`
	GameID           int64      `db:"game_id"`
	WorkshopID       string     `db:"workshop_id"`
	PlatformType     string     `db:"platform_type"`
	Name             string     `db:"name"`
	Description      *string    `db:"description"`
	FileSizeBytes    *int64     `db:"file_size_bytes"`
	InstallationPath *string    `db:"installation_path"`
	PresetID         int64      `db:"preset_id"`
	IsCollection     bool       `db:"is_collection"`
	IsDeprecated     bool       `db:"is_deprecated"`
	Metadata         JSONB      `db:"metadata"`
	LastUpdated      *time.Time `db:"last_updated"`
	CreatedAt        time.Time  `db:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at"`
}

// WorkshopAddonWithGame is returned by ListAddons queries that join with the games table.
type WorkshopAddonWithGame struct {
	WorkshopAddon
	SteamAppID *string // games.steam_app_id
}

// GameAddonPathPreset represents a reusable installation path template for workshop addons
type GameAddonPathPreset struct {
	PresetID         int64     `db:"preset_id"`
	GameID           int64     `db:"game_id"`
	Name             string    `db:"name"`
	Description      *string   `db:"description"`
	InstallationPath string    `db:"installation_path"`
	CreatedAt        time.Time `db:"created_at"`
}

// WorkshopInstallation represents an addon installed on a ServerGameConfig
type WorkshopInstallation struct {
	InstallationID      int64      `db:"installation_id"`
	SGCID               int64      `db:"sgc_id"`
	AddonID             int64      `db:"addon_id"`
	Status              string     `db:"status"`
	InstallationPath    string     `db:"installation_path"`
	ProgressPercent     int        `db:"progress_percent"`
	ErrorMessage        *string    `db:"error_message"`
	DownloadStartedAt   *time.Time `db:"download_started_at"`
	DownloadCompletedAt *time.Time `db:"download_completed_at"`
	CreatedAt           time.Time  `db:"created_at"`
	UpdatedAt           time.Time  `db:"updated_at"`
}

// WorkshopLibrary represents a collection of workshop addons
type WorkshopLibrary struct {
	LibraryID   int64     `db:"library_id"`
	GameID      int64     `db:"game_id"`
	Name        string    `db:"name"`
	Description *string   `db:"description"`
	PresetID    *int64    `db:"preset_id"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

// SGCWorkshopLibrary represents a library attached to an SGC with optional overrides
type SGCWorkshopLibrary struct {
	SGCID                    int64     `db:"sgc_id"`
	LibraryID                int64     `db:"library_id"`
	PresetID                 *int64    `db:"preset_id"`
	VolumeID                 *int64    `db:"volume_id"`
	InstallationPathOverride *string   `db:"installation_path_override"`
	CreatedAt                time.Time `db:"created_at"`
}

// WorkshopLibraryAddon represents the junction between libraries and addons
type WorkshopLibraryAddon struct {
	LibraryID    int64     `db:"library_id"`
	AddonID      int64     `db:"addon_id"`
	DisplayOrder int       `db:"display_order"`
	CreatedAt    time.Time `db:"created_at"`
}

// WorkshopLibraryReference represents library-to-library references for hierarchies
type WorkshopLibraryReference struct {
	ParentLibraryID int64     `db:"parent_library_id"`
	ChildLibraryID  int64     `db:"child_library_id"`
	CreatedAt       time.Time `db:"created_at"`
}
