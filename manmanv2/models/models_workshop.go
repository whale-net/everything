package manman

import "time"

// WorkshopAddon represents a workshop addon in the library
type WorkshopAddon struct {
	AddonID          int64   `db:"addon_id"`
	GameID           int64   `db:"game_id"`
	WorkshopID       string  `db:"workshop_id"`
	PlatformType     string  `db:"platform_type"`
	Name             string  `db:"name"`
	Description      *string `db:"description"`
	FileSizeBytes    *int64  `db:"file_size_bytes"`
	InstallationPath *string `db:"installation_path"`
	PresetID         int64   `db:"preset_id"`
	IsCollection     bool    `db:"is_collection"`
	IsDeprecated     bool    `db:"is_deprecated"`
	// CollectionID correlates an addon created as a child of a Steam Workshop
	// collection back to the collection's own addon row. Nil for standalone
	// addons and for the collection row itself.
	CollectionID *int64     `db:"collection_id"`
	Metadata     JSONB      `db:"metadata"`
	LastUpdated  *time.Time `db:"last_updated"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
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

// WorkshopBatchJob represents a batch operation (collection bulk-add or
// mixed-format batch create) and its aggregate progress/outcome. NFR1/NFR2:
// deliberately has no sgc_id or SGC-scoped uniqueness -- addon writes it
// produces land in the library-scoped workshop_library_addons junction.
type WorkshopBatchJob struct {
	BatchJobID     int64     `db:"batch_job_id"`
	JobType        string    `db:"job_type"` // "collection_add" | "batch_create"
	GameID         int64     `db:"game_id"`
	LibraryID      *int64    `db:"library_id"` // nil only if a job type is created without a library target
	SourceInput    *string   `db:"source_input"`
	Status         string    `db:"status"` // "pending" | "running" | "completed" | "completed_with_errors" | "failed"
	TotalItems     int       `db:"total_items"`
	SucceededItems int       `db:"succeeded_items"`
	FailedItems    int       `db:"failed_items"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

// WorkshopBatchJobItem represents one line/entry of a batch job and its
// per-item outcome (NFR5: schema shape that makes per-item atomicity
// possible).
type WorkshopBatchJobItem struct {
	BatchJobItemID int64     `db:"batch_job_item_id"`
	BatchJobID     int64     `db:"batch_job_id"`
	RawInput       string    `db:"raw_input"`   // the line exactly as pasted, or the collection child id
	WorkshopID     *string   `db:"workshop_id"` // populated once parsed
	AddonID        *int64    `db:"addon_id"`    // populated on success
	Status         string    `db:"status"`      // "pending" | "succeeded" | "failed"
	ErrorMessage   *string   `db:"error_message"`
	DisplayOrder   int       `db:"display_order"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

// WorkshopCacheEntry is one version of a content-addressed Workshop cache
// object (#2181, plan #2175, FR5/FR9). Identity is WorkshopID +
// ContentVersion ONLY (NFR1) -- there is deliberately no sgc_id, server_id,
// deployment_id, library_id, or game_id column here, and CacheKey (derived
// by //manmanv2/api/workshop.CacheKey) never embeds any of them. No
// SGC-scoped uniqueness exists anywhere in this layer (NFR2).
//
// This is an append-only version history, not SCD2 (AGENTS.md § SCD2): when
// an addon's source changes, a new row is inserted and the prior row is
// never closed out via valid_from/valid_to or an is_current flag. Rows are
// only ever removed by explicit Admin eviction (FR12); no garbage collection
// of superseded entries exists in this layer.
type WorkshopCacheEntry struct {
	CacheEntryID   int64      `db:"cache_entry_id"`
	WorkshopID     string     `db:"workshop_id"`
	ContentVersion string     `db:"content_version"`
	CacheKey       string     `db:"cache_key"`
	S3Key          string     `db:"s3_key"`
	SizeBytes      *int64     `db:"size_bytes"`
	LastVerifiedAt *time.Time `db:"last_verified_at"`
	CreatedAt      time.Time  `db:"created_at"`
}

// WorkshopCacheHostPresence records that server_id currently holds a copy of
// cache_entry_id's object. This is purely an observation of *where a copy
// currently sits* -- it is never part of a cache entry's identity (NFR1),
// which is why it lives in its own table rather than as a column on
// WorkshopCacheEntry.
type WorkshopCacheHostPresence struct {
	CacheEntryID int64     `db:"cache_entry_id"`
	ServerID     int64     `db:"server_id"`
	FirstSeenAt  time.Time `db:"first_seen_at"`
	LastSeenAt   time.Time `db:"last_seen_at"`
}
