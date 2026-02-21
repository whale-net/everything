# Requirements Document: Workshop Addon Management

## Introduction

The Workshop Addon Management feature enables ManManV2 to download, install, track, and manage game workshop addons (maps, mods, custom content) from platforms like Steam Workshop. This feature addresses the need for older games (e.g., Left 4 Dead 2) that lack automatic workshop download capabilities, while also providing centralized management for games that do support it (e.g., CS2).

The system will maintain a library of available workshop addons per game, track installations per ServerGameConfig to prevent duplicate downloads, and integrate with the existing volume strategy and action systems to enable map selection and addon management workflows.

## Glossary

- **Workshop_Addon**: A downloadable piece of content (map, mod, skin, etc.) from a workshop platform (Steam Workshop, etc.)
- **Workshop_Library**: A catalog of available workshop addons for a specific game
- **Workshop_Installation**: A record tracking that a specific addon has been downloaded to a specific ServerGameConfig
- **SteamCMD**: Command-line tool for downloading Steam Workshop content
- **ServerGameConfig (SGC)**: A game configuration deployed on a specific server
- **Volume_Strategy**: Configuration strategy defining persistent storage paths for game data
- **Action_System**: ManManV2's system for executing commands on game sessions
- **Download_Manager**: Component responsible for downloading workshop content using appropriate tools
- **Addon_Metadata**: Information about a workshop addon including workshop ID, name, description, size, and game-specific paths

## Requirements

### Requirement 1: Workshop Addon Library Management

**User Story:** As a game server administrator, I want to maintain a library of available workshop addons for each game, so that I can easily discover and install content without manually tracking workshop IDs.

#### Acceptance Criteria

1. THE System SHALL store workshop addon metadata including workshop ID, name, description, file size, and last updated timestamp
2. WHEN an administrator creates a workshop addon entry, THE System SHALL validate that the workshop ID is unique per game
3. THE System SHALL associate each workshop addon with a specific game via game_id
4. WHEN listing workshop addons, THE System SHALL filter by game_id and support pagination
5. THE System SHALL allow administrators to update addon metadata (name, description) without re-downloading content
6. THE System SHALL store game-specific installation paths for each addon relative to volume mount points
7. WHEN an addon is marked as deprecated, THE System SHALL retain the record but flag it as unavailable for new installations

### Requirement 2: Workshop Installation Tracking

**User Story:** As a system operator, I want to track which workshop addons are installed on each ServerGameConfig, so that duplicate downloads are prevented and disk space is managed efficiently.

#### Acceptance Criteria

1. THE System SHALL record each workshop addon installation with references to sgc_id and addon_id
2. WHEN a download request is received for an addon already installed on a ServerGameConfig, THE System SHALL skip the download and return the existing installation record
3. THE System SHALL store installation metadata including installation timestamp, file path, and installation status
4. WHEN an installation fails, THE System SHALL record the failure status and error message
5. THE System SHALL support querying all installed addons for a given ServerGameConfig
6. THE System SHALL support querying all ServerGameConfigs that have a specific addon installed
7. WHEN a ServerGameConfig is deleted, THE System SHALL cascade delete or mark orphaned installation records

### Requirement 3: Workshop Content Download

**User Story:** As a game server administrator, I want to download workshop addons on-demand to ServerGameConfigs, so that content is available when needed without manual intervention.

#### Acceptance Criteria

1. WHEN a download is requested, THE Download_Manager SHALL use the appropriate tool (SteamCMD for Steam Workshop content)
2. THE Download_Manager SHALL download content to paths specified by the addon's installation path configuration
3. WHEN downloading, THE Download_Manager SHALL resolve the target path relative to the ServerGameConfig's volume mount points
4. THE Download_Manager SHALL execute downloads within the context of the game server container or via a helper container
5. WHEN a download completes successfully, THE System SHALL update the installation record status to "installed"
6. WHEN a download fails, THE System SHALL update the installation record status to "failed" and record the error message
7. THE Download_Manager SHALL support concurrent downloads for different ServerGameConfigs without conflicts

### Requirement 4: Volume Strategy Integration

**User Story:** As a system architect, I want workshop addon downloads to integrate with existing volume strategies, so that downloaded content persists correctly and follows established storage patterns.

#### Acceptance Criteria

1. WHEN determining download paths, THE System SHALL query volume strategies for the ServerGameConfig's game
2. THE System SHALL resolve addon installation paths relative to volume mount points defined in volume strategies
3. WHEN a volume strategy specifies a target path, THE System SHALL combine it with the addon's relative installation path
4. THE System SHALL validate that required volume strategies exist before allowing addon downloads
5. WHEN multiple volume strategies exist, THE System SHALL use the strategy with the appropriate apply_order for addon storage
6. THE System SHALL ensure downloaded files have appropriate permissions (0777 for compatibility with game processes)

### Requirement 5: Download Container Execution

**User Story:** As a system operator, I want workshop addons downloaded via a dedicated container with appropriate volume mounts, so that downloads are isolated from game containers and use specialized tooling.

#### Acceptance Criteria

1. THE System SHALL spawn a download container with SteamCMD and workshop download tools
2. THE Download_Manager SHALL mount the target ServerGameConfig's volumes to the download container
3. WHEN downloading to a game path like `/data/maps/`, THE System SHALL mount the volume at the same path in the download container
4. THE Download_Manager SHALL execute the download within the container and terminate the container upon completion
5. THE System SHALL handle download container failures gracefully and report errors to administrators
6. THE System SHALL clean up download containers after successful or failed downloads

### Requirement 6: Admin API and UI Controls

**User Story:** As a game server administrator, I want API endpoints and UI controls to manage workshop addons, so that I can configure and monitor addon installations through the management interface.

#### Acceptance Criteria

1. THE System SHALL provide API endpoints to create, read, update, and delete workshop addon library entries
2. THE System SHALL provide API endpoints to list workshop addons filtered by game
3. THE System SHALL provide API endpoints to trigger addon downloads to specific ServerGameConfigs
4. THE System SHALL provide API endpoints to list installed addons for a ServerGameConfig
5. THE System SHALL provide API endpoints to remove installed addons from a ServerGameConfig
6. THE Management_UI SHALL display workshop addon libraries per game with search and filter capabilities
7. THE Management_UI SHALL display installed addons for each ServerGameConfig with installation status
8. THE Management_UI SHALL provide buttons to trigger addon downloads and removals
9. WHEN an addon download is in progress, THE Management_UI SHALL display progress status
10. THE Management_UI SHALL display error messages when addon operations fail

### Requirement 7: Action System Integration for Map Selection

**User Story:** As a game server administrator, I want to integrate workshop addons with the action system, so that I can create map selection actions for games that don't support automatic workshop downloads.

#### Acceptance Criteria

1. THE System SHALL allow creating action definitions that reference installed workshop addons
2. WHEN creating a map change action, THE System SHALL populate action input options from installed workshop addons
3. THE System SHALL render action commands with workshop addon metadata (map name, workshop ID, file path)
4. WHEN an action is executed, THE System SHALL verify that the referenced addon is installed before executing the command
5. THE System SHALL support dynamic action input options that update when addons are installed or removed
6. THE System SHALL provide template variables for addon metadata in action command templates
7. WHEN a game supports automatic workshop downloads (like CS2), THE System SHALL still allow manual addon management for consistency

### Requirement 8: SteamCMD Integration

**User Story:** As a system operator, I want the system to use SteamCMD for downloading Steam Workshop content, so that workshop addons are downloaded reliably using the official tool.

#### Acceptance Criteria

1. THE Download_Manager SHALL execute SteamCMD with appropriate parameters for workshop downloads
2. THE Download_Manager SHALL construct SteamCMD commands with: login anonymous, workshop_download_item <app_id> <workshop_id>, and quit
3. WHEN SteamCMD requires authentication, THE System SHALL support providing Steam credentials securely
4. THE Download_Manager SHALL parse SteamCMD output to detect download success or failure
5. THE Download_Manager SHALL handle SteamCMD errors (network failures, invalid workshop IDs, authentication failures)
6. THE System SHALL store SteamCMD download logs for troubleshooting
7. THE Download_Manager SHALL support configuring SteamCMD installation path per server or globally

### Requirement 9: Addon Metadata Discovery

**User Story:** As a game server administrator, I want to automatically discover addon metadata from workshop platforms, so that I don't have to manually enter names and descriptions.

#### Acceptance Criteria

1. WHEN an administrator provides a workshop ID, THE System SHALL attempt to fetch metadata from the workshop platform API
2. THE System SHALL retrieve addon name, description, file size, and last updated timestamp from the workshop API
3. WHEN metadata fetching fails, THE System SHALL allow manual entry of addon information
4. THE System SHALL cache fetched metadata to reduce API calls
5. THE System SHALL provide an option to refresh metadata for existing addon library entries
6. WHEN workshop platform APIs are unavailable, THE System SHALL gracefully degrade to manual metadata entry

### Requirement 10: Addon Removal and Cleanup

**User Story:** As a system operator, I want to remove installed workshop addons from ServerGameConfigs, so that disk space can be reclaimed when content is no longer needed.

#### Acceptance Criteria

1. THE System SHALL provide an API endpoint to remove an installed addon from a ServerGameConfig
2. WHEN removing an addon, THE System SHALL delete the addon files from the volume storage
3. THE System SHALL update the installation record status to "removed" or delete the record
4. WHEN file deletion fails, THE System SHALL record the error and mark the installation as "removal_failed"
5. THE System SHALL prevent removal of addons that are currently in use by active sessions
6. THE System SHALL support bulk removal of multiple addons from a ServerGameConfig
7. WHEN removing addons, THE System SHALL log the operation for audit purposes

### Requirement 11: Extensible Platform Support

**User Story:** As a system architect, I want the system designed to support multiple workshop platforms in the future, so that the architecture doesn't prevent extending beyond Steam Workshop.

#### Acceptance Criteria

1. THE System SHALL store a platform_type field for each workshop addon (initially only "steam_workshop")
2. THE System SHALL design the Download_Manager interface to allow future platform implementations
3. THE System SHALL validate workshop IDs according to platform-specific formats
4. THE System SHALL use platform-agnostic terminology in APIs and data models where possible

### Requirement 12: Download Progress and Status Reporting

**User Story:** As a game server administrator, I want to see real-time progress of workshop addon downloads, so that I can monitor long-running downloads and troubleshoot issues.

#### Acceptance Criteria

1. THE System SHALL update installation records with download progress percentage during downloads
2. THE Download_Manager SHALL parse download tool output to extract progress information
3. THE System SHALL expose download progress via API endpoints
4. THE Management_UI SHALL poll for download progress and display it to administrators
5. WHEN a download is queued but not started, THE System SHALL indicate "pending" status
6. WHEN a download is in progress, THE System SHALL indicate "downloading" status with percentage
7. WHEN a download completes, THE System SHALL indicate "installed" status with completion timestamp

### Requirement 13: Addon Version Management

**User Story:** As a game server administrator, I want to track workshop addon versions, so that I can update addons when new versions are released and rollback if needed.

#### Acceptance Criteria

1. THE System SHALL store version information for workshop addons when available from the platform
2. WHEN an addon is updated on the workshop platform, THE System SHALL detect version changes
3. THE System SHALL provide an API endpoint to update an installed addon to the latest version
4. WHEN updating an addon, THE System SHALL preserve the old version temporarily for rollback
5. THE System SHALL record version history for each installation
6. THE System SHALL allow administrators to rollback to previous addon versions
7. WHEN version information is unavailable, THE System SHALL use last_updated timestamp as a version indicator

### Requirement 14: Addon Collections and Library References

**User Story:** As a game server administrator, I want to manage addon collections and library references, so that I can organize related addons and reuse curated content sets.

#### Acceptance Criteria

1. THE System SHALL detect whether a workshop ID is a collection or individual item
2. WHEN downloading a collection, THE System SHALL download all items in the collection if requested
3. WHEN a collection is downloaded, THE System SHALL create library entries for all items in the collection
4. THE System SHALL support creating custom addon libraries that group related addons
5. THE System SHALL allow a library to reference another library for organizational hierarchy
6. THE System SHALL prevent circular library references
7. WHEN installing from a library that references other libraries, THE System SHALL resolve all referenced addons

### Requirement 15: Error Handling and Retry Logic

**User Story:** As a system operator, I want robust error handling and retry logic for addon downloads, so that transient failures don't require manual intervention.

#### Acceptance Criteria

1. WHEN a download fails due to network errors, THE System SHALL automatically retry up to 3 times
2. THE System SHALL use exponential backoff between retry attempts
3. WHEN all retries are exhausted, THE System SHALL mark the installation as "failed" and notify administrators
4. THE System SHALL distinguish between retryable errors (network) and permanent errors (invalid workshop ID)
5. WHEN a permanent error occurs, THE System SHALL not retry and immediately mark as failed
6. THE System SHALL log all retry attempts with timestamps and error messages
7. THE System SHALL provide an API endpoint to manually retry failed installations

### Requirement 16: Security and Access Control

**User Story:** As a system administrator, I want workshop addon management to respect security boundaries, so that unauthorized users cannot install malicious content or access sensitive data.

#### Acceptance Criteria

1. THE System SHALL require authentication for all workshop addon management API endpoints
2. THE System SHALL validate workshop IDs to prevent path traversal attacks in installation paths
3. THE System SHALL sanitize addon metadata to prevent XSS attacks in the Management_UI
4. WHEN storing Steam credentials, THE System SHALL encrypt them at rest
5. THE System SHALL validate that addon installation paths remain within allowed volume boundaries
6. THE System SHALL log all addon management operations for security auditing
7. THE System SHALL support role-based access control for addon management operations

### Requirement 17: Performance and Resource Management

**User Story:** As a system operator, I want workshop addon downloads to be resource-efficient, so that they don't impact game server performance or exhaust system resources.

#### Acceptance Criteria

1. THE System SHALL limit concurrent addon downloads per server to prevent resource exhaustion
2. THE System SHALL support configuring download bandwidth limits per server
3. THE System SHALL monitor disk space before initiating downloads and fail if insufficient space exists
4. THE System SHALL clean up temporary download files after installation completes or fails
5. THE System SHALL support pausing and resuming downloads for large addons
6. THE System SHALL prioritize downloads based on administrator-defined priority levels
7. THE System SHALL provide metrics on download performance (speed, success rate, disk usage)

