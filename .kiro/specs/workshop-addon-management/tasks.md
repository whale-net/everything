# Implementation Plan: Workshop Addon Management

## Overview

This implementation plan breaks down the workshop addon management feature into discrete coding tasks. The feature adds Steam Workshop addon download and management capabilities to ManManV2, including database models, API endpoints, download orchestration, and UI components.

## Tasks

- [ ] 1. Database schema and migrations
  - [x] 1.1 Create migration for workshop_addons table
    - Create `manmanv2/migrate/migrations/XXX_workshop_addons.up.sql`
    - Include all columns: addon_id, game_id, workshop_id, platform_type, name, description, file_size_bytes, installation_path, is_collection, is_deprecated, metadata, timestamps
    - Add indexes for game_id, workshop_id, platform_type, is_deprecated
    - Add unique constraint on (game_id, workshop_id, platform_type)
    - _Requirements: 1.1, 1.2, 1.3, 1.6, 1.7_
  
  - [x] 1.2 Create migration for workshop_installations table
    - Create installation tracking table with sgc_id and addon_id foreign keys
    - Include status, installation_path, progress_percent, error_message, timestamps
    - Add indexes for sgc_id, addon_id, status
    - Add unique constraint on (sgc_id, addon_id)
    - _Requirements: 2.1, 2.3, 2.7_
  
  - [x] 1.3 Create migration for workshop_libraries tables
    - Create workshop_libraries table for custom addon collections
    - Create workshop_library_addons junction table
    - Create workshop_library_references table for library hierarchies
    - Add appropriate indexes and constraints
    - _Requirements: 14.4, 14.5, 14.6_
  
  - [x] 1.4 Create down migrations for rollback
    - Create corresponding .down.sql files for all migrations
    - Test migration up/down cycle
    - _Requirements: All database requirements_

- [ ] 2. Go models and constants
  - [x] 2.1 Add WorkshopAddon model to manmanv2/models.go
    - Define struct with all fields matching database schema
    - Add JSONB type for metadata field
    - Add struct tags for database mapping
    - _Requirements: 1.1, 1.3, 1.6_
  
  - [x] 2.2 Add WorkshopInstallation model to manmanv2/models.go
    - Define struct with status, progress, error tracking
    - Add foreign key references to SGCID and AddonID
    - _Requirements: 2.1, 2.3_
  
  - [x] 2.3 Add WorkshopLibrary models to manmanv2/models.go
    - Define WorkshopLibrary, WorkshopLibraryAddon, WorkshopLibraryReference structs
    - _Requirements: 14.4, 14.5_
  
  - [x] 2.4 Add workshop constants to manmanv2/models.go
    - Add installation status constants (pending, downloading, installed, failed, removed)
    - Add platform type constants (steam_workshop)
    - _Requirements: 2.3, 11.1_

- [ ] 3. Repository layer implementation
  - [x] 3.1 Create WorkshopAddonRepository interface and implementation
    - Define interface in `manmanv2/api/repository/repository.go`
    - Implement in `manmanv2/api/repository/postgres/workshop_addon.go`
    - Implement Create, Get, GetByWorkshopID, List, Update, Delete methods
    - _Requirements: 1.1, 1.2, 1.4, 1.5_
  
  - [x] 3.2 Write property tests for WorkshopAddonRepository
    - **Property 1: Addon Storage Round Trip**
    - **Validates: Requirements 1.1, 1.3, 1.6**
  
  - [x] 3.3 Write property tests for addon uniqueness and filtering
    - **Property 2: Workshop ID Uniqueness Per Game**
    - **Property 3: Game Filtering Correctness**
    - **Validates: Requirements 1.2, 1.4**
  
  - [x] 3.4 Create WorkshopInstallationRepository interface and implementation
    - Define interface in repository.go
    - Implement in `manmanv2/api/repository/postgres/workshop_installation.go`
    - Implement Create, Get, GetBySGCAndAddon, ListBySGC, ListByAddon, UpdateStatus, UpdateProgress, Delete
    - _Requirements: 2.1, 2.2, 2.5, 2.6_
  
  - [x] 3.5 Write property tests for WorkshopInstallationRepository
    - **Property 7: Installation Record Completeness**
    - **Property 8: SGC Installation Query Completeness**
    - **Property 9: Addon Installation Query Completeness**
    - **Validates: Requirements 2.1, 2.3, 2.5, 2.6**
  
  - [x] 3.6 Create WorkshopLibraryRepository interface and implementation
    - Define interface in repository.go
    - Implement in `manmanv2/api/repository/postgres/workshop_library.go`
    - Implement CRUD methods plus AddAddon, RemoveAddon, AddReference, RemoveReference, DetectCircularReference
    - _Requirements: 14.4, 14.5, 14.6_
  
  - [~] 3.7 Write property tests for library operations
    - **Property 35: Library Addon Association**
    - **Property 36: Library Reference Acyclicity**
    - **Property 37: Library Reference Resolution**
    - **Validates: Requirements 14.4, 14.5, 14.6, 14.7**

- [ ] 4. Steam Workshop API client
  - [~] 4.1 Create SteamWorkshopClient in manmanv2/api/steam/client.go
    - Implement GetWorkshopItemDetails method
    - Implement GetCollectionDetails method
    - Add HTTP client with timeout and retry logic
    - Parse Steam API JSON responses
    - _Requirements: 9.1, 9.2, 14.1_
  
  - [~] 4.2 Write property tests for Steam API client
    - **Property 25: Metadata Fetch Round Trip**
    - **Property 33: Collection Detection**
    - **Validates: Requirements 9.2, 14.1**
  
  - [~] 4.3 Implement metadata caching
    - Add in-memory cache with TTL for workshop metadata
    - Implement cache key generation from workshop ID
    - Add cache hit/miss metrics
    - _Requirements: 9.4_
  
  - [~] 4.4 Write property test for caching
    - **Property 26: Metadata Caching Efficiency**
    - **Validates: Requirements 9.4**
  
  - [~] 4.5 Add error handling for Steam API failures
    - Handle rate limiting with exponential backoff
    - Handle API unavailability gracefully
    - Return structured errors for different failure types
    - _Requirements: 9.3, 9.6_

- [ ] 5. Workshop Manager service (Control Plane)
  - [~] 5.1 Create WorkshopManager in manmanv2/api/workshop/manager.go
    - Initialize with repository dependencies
    - Add SteamWorkshopClient dependency
    - Add RabbitMQ publisher for download commands
    - _Requirements: All control plane requirements_
  
  - [~] 5.2 Implement InstallAddon method
    - Check for existing installation (idempotency)
    - Resolve installation path from volume strategies
    - Create installation record
    - Publish download command to RabbitMQ
    - _Requirements: 2.2, 3.1, 3.2, 3.3, 4.1, 4.2, 4.3_
  
  - [~] 5.3 Write property tests for InstallAddon
    - **Property 6: Installation Idempotency**
    - **Property 11: Path Resolution Consistency**
    - **Property 14: Volume Strategy Validation**
    - **Validates: Requirements 2.2, 3.2, 3.3, 4.1, 4.2, 4.3, 4.4**
  
  - [~] 5.4 Implement FetchAndCreateAddon method
    - Fetch metadata from Steam Workshop API
    - Handle collections by fetching all items
    - Create addon record in database
    - Store collection items in metadata JSONB
    - _Requirements: 9.1, 9.2, 14.1, 14.2, 14.3_
  
  - [~] 5.5 Write property test for collection expansion
    - **Property 34: Collection Expansion**
    - **Validates: Requirements 14.2, 14.3**
  
  - [~] 5.6 Implement RemoveInstallation method
    - Validate no active sessions using the addon
    - Delete installation record
    - Publish removal command to host manager
    - _Requirements: 10.1, 10.3, 10.5_
  
  - [~] 5.7 Write property tests for removal
    - **Property 28: Active Session Protection**
    - **Validates: Requirements 10.5**

- [~] 6. Checkpoint - Ensure control plane tests pass
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 7. Download Orchestrator (Host Manager Component)
  - [~] 7.1 Create DownloadOrchestrator in manmanv2/host/workshop/orchestrator.go
    - Initialize with Docker client, server ID, environment, host data dir
    - Add semaphore for concurrency control
    - Add RabbitMQ publisher for status updates
    - Add in-progress download tracking map
    - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.6, 17.1_
  
  - [~] 7.2 Implement HandleDownloadCommand method
    - Check for duplicate in-progress downloads
    - Acquire concurrency semaphore
    - Build environment-aware container name
    - Clean up existing containers from failed attempts
    - Create and start download container
    - Monitor logs for progress
    - Clean up container after completion
    - Publish status updates to RabbitMQ
    - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.6, 12.1, 12.2_
  
  - [~] 7.3 Write property tests for download orchestration
    - **Property 13: Concurrent Download Isolation**
    - **Property 15: Volume Mount Consistency**
    - **Property 16: Container Cleanup Guarantee**
    - **Property 45: Concurrent Download Limit Enforcement**
    - **Validates: Requirements 3.7, 5.2, 5.3, 5.4, 5.6, 17.1**
  
  - [~] 7.4 Implement SteamCMD command building
    - Build command with app ID and workshop ID
    - Format: `steamcmd +login anonymous +workshop_download_item <app_id> <workshop_id> +quit`
    - _Requirements: 8.1, 8.2_
  
  - [~] 7.5 Write property test for SteamCMD command format
    - **Property 22: SteamCMD Command Format**
    - **Validates: Requirements 8.1, 8.2**
  
  - [~] 7.6 Implement progress parsing from SteamCMD logs
    - Parse percentage from log lines
    - Extract error messages from failed downloads
    - Classify errors as retryable or permanent
    - _Requirements: 8.4, 12.2, 15.4, 15.5_
  
  - [~] 7.7 Write property tests for log parsing
    - **Property 23: SteamCMD Output Parsing**
    - **Property 24: SteamCMD Error Handling**
    - **Property 40: Error Classification Correctness**
    - **Validates: Requirements 8.4, 8.5, 12.2, 15.4, 15.5**
  
  - [~] 7.8 Implement retry logic with exponential backoff
    - Retry up to 3 times for retryable errors
    - Use exponential backoff (1s, 2s, 4s)
    - Skip retries for permanent errors
    - Log all retry attempts
    - _Requirements: 15.1, 15.2, 15.4, 15.5, 15.6_
  
  - [~] 7.9 Write property tests for retry logic
    - **Property 38: Retry Exponential Backoff**
    - **Property 39: Retry Limit Enforcement**
    - **Validates: Requirements 15.1, 15.2**

- [ ] 8. RabbitMQ integration
  - [~] 8.1 Define DownloadAddonCommand message in manmanv2/host/rmq/messages.go
    - Add struct with installation_id, sgc_id, addon_id, workshop_id, steam_app_id, install_path
    - Add JSON tags for serialization
    - _Requirements: 3.1, 5.1_
  
  - [~] 8.2 Define InstallationStatusUpdate message in manmanv2/host/rmq/messages.go
    - Add struct with installation_id, status, progress_percent, error_message
    - Add JSON tags for serialization
    - _Requirements: 12.1, 12.5, 12.6, 12.7_
  
  - [~] 8.3 Add workshop download handler to host manager
    - Subscribe to `workshop.download` queue in host manager
    - Deserialize DownloadAddonCommand
    - Call DownloadOrchestrator.HandleDownloadCommand
    - _Requirements: 5.1_
  
  - [~] 8.4 Add installation status handler to API server
    - Subscribe to `workshop.installation.status` queue in API server
    - Deserialize InstallationStatusUpdate
    - Update installation record in database
    - _Requirements: 12.1, 12.5, 12.6, 12.7_

- [~] 9. Checkpoint - Ensure download orchestration works
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 10. gRPC API service
  - [~] 10.1 Define WorkshopService protobuf in manmanv2/protos/workshop.proto
    - Define WorkshopAddon, WorkshopInstallation, WorkshopLibrary messages
    - Define all RPC methods (CreateAddon, InstallAddon, ListAddons, etc.)
    - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5_
  
  - [~] 10.2 Generate Go code from protobuf
    - Run protoc to generate Go code
    - Verify generated code compiles
    - _Requirements: 6.1_
  
  - [~] 10.3 Implement WorkshopServiceHandler in manmanv2/api/handlers/workshop.go
    - Implement CreateAddon RPC
    - Implement GetAddon RPC
    - Implement ListAddons RPC with pagination and filtering
    - Implement UpdateAddon RPC
    - Implement DeleteAddon RPC
    - _Requirements: 6.1, 6.2_
  
  - [~] 10.4 Write property tests for addon API
    - **Property 17: API CRUD Completeness**
    - **Validates: Requirements 6.1**
  
  - [~] 10.5 Implement installation management RPCs
    - Implement InstallAddon RPC (calls WorkshopManager.InstallAddon)
    - Implement GetInstallation RPC
    - Implement ListInstallations RPC with filtering by SGC
    - Implement RemoveInstallation RPC
    - _Requirements: 6.3, 6.4, 6.5_
  
  - [~] 10.6 Write property test for installation listing
    - **Property 18: Installation List Filtering**
    - **Validates: Requirements 6.4**
  
  - [~] 10.7 Implement library management RPCs
    - Implement CreateLibrary, GetLibrary, ListLibraries RPCs
    - Implement AddAddonToLibrary, RemoveAddonFromLibrary RPCs
    - Implement AddLibraryReference RPC with circular reference detection
    - _Requirements: 14.4, 14.5, 14.6_
  
  - [~] 10.8 Implement FetchAddonMetadata RPC
    - Call SteamWorkshopClient to fetch metadata
    - Return metadata without creating addon record
    - Handle API failures gracefully
    - _Requirements: 9.1, 9.2, 9.3_
  
  - [~] 10.9 Add authentication and authorization to all RPCs
    - Require authentication for all workshop RPCs
    - Add audit logging for all operations
    - _Requirements: 16.1, 16.6_
  
  - [~] 10.10 Write property test for audit logging
    - **Property 44: Audit Log Completeness**
    - **Validates: Requirements 16.6, 10.7**

- [ ] 11. Input validation and security
  - [~] 11.1 Implement workshop ID validation
    - Validate Steam Workshop IDs are numeric strings
    - Prevent path traversal in workshop IDs
    - _Requirements: 11.3, 16.2_
  
  - [~] 11.2 Write property test for path traversal prevention
    - **Property 41: Path Traversal Prevention**
    - **Validates: Requirements 16.2**
  
  - [~] 11.3 Implement metadata sanitization
    - Sanitize HTML/JavaScript in addon names and descriptions
    - Prevent XSS attacks in UI
    - _Requirements: 16.3_
  
  - [~] 11.4 Write property test for sanitization
    - **Property 42: Metadata Sanitization**
    - **Validates: Requirements 16.3**
  
  - [~] 11.5 Implement installation path validation
    - Validate paths remain within volume boundaries
    - Prevent directory traversal in installation paths
    - _Requirements: 16.5_
  
  - [~] 11.6 Write property test for path boundary validation
    - **Property 43: Installation Path Boundary Validation**
    - **Validates: Requirements 16.5**

- [ ] 12. Action system integration
  - [~] 12.1 Create action helper for workshop addon actions
    - Create `manmanv2/api/workshop/actions.go`
    - Implement function to generate action input options from installed addons
    - Implement template variable substitution for addon metadata
    - _Requirements: 7.1, 7.2, 7.3, 7.6_
  
  - [~] 12.2 Write property tests for action integration
    - **Property 19: Action Addon Reference Validity**
    - **Property 20: Action Template Rendering**
    - **Property 21: Dynamic Action Options Synchronization**
    - **Validates: Requirements 7.2, 7.3, 7.4, 7.5, 7.6**
  
  - [~] 12.3 Add workshop addon support to action execution
    - Verify addon is installed before executing action
    - Substitute addon metadata in command templates
    - _Requirements: 7.4, 7.6_

- [ ] 13. Management UI components
  - [~] 13.1 Create workshop addon library page template
    - Create `manmanv2/ui/templates/workshop_library.html`
    - Display addons per game with search and filter
    - Add buttons for create, edit, delete addon
    - Add button to fetch metadata from Steam
    - _Requirements: 6.6_
  
  - [~] 13.2 Create workshop addon installation page template
    - Create `manmanv2/ui/templates/workshop_installations.html`
    - Display installed addons for SGC with status
    - Show progress bars for in-progress downloads
    - Add install and remove buttons
    - Display error messages for failed installations
    - _Requirements: 6.7, 6.8, 6.9, 6.10_
  
  - [~] 13.3 Implement UI handlers in manmanv2/ui/handlers_workshop.go
    - Implement WorkshopLibraryPage handler
    - Implement WorkshopInstallationsPage handler
    - Implement HTMX endpoints for install/remove actions
    - Implement HTMX endpoint for progress polling
    - _Requirements: 6.6, 6.7, 6.8, 6.9_
  
  - [~] 13.4 Add workshop addon management to SGC detail page
    - Add workshop addons section to gameserver.html template
    - Display installed addons with status
    - Add install button that opens addon selection modal
    - _Requirements: 6.7, 6.8_

- [ ] 14. Resource management and performance
  - [~] 14.1 Implement disk space validation
    - Check available disk space before downloads
    - Reject downloads if insufficient space
    - _Requirements: 17.3_
  
  - [~] 14.2 Write property test for disk space validation
    - **Property 46: Disk Space Validation**
    - **Validates: Requirements 17.3**
  
  - [~] 14.3 Implement temporary file cleanup
    - Clean up temp files after successful downloads
    - Clean up temp files after failed downloads
    - _Requirements: 17.4_
  
  - [~] 14.4 Write property test for cleanup
    - **Property 47: Temporary File Cleanup**
    - **Validates: Requirements 17.4**
  
  - [~] 14.5 Implement download priority queue
    - Add priority field to download commands
    - Process downloads in priority order
    - _Requirements: 17.6_
  
  - [~] 14.6 Write property test for priority ordering
    - **Property 48: Download Priority Ordering**
    - **Validates: Requirements 17.6**

- [ ] 15. Integration and end-to-end testing
  - [~] 15.1 Write integration test for complete addon installation workflow
    - Test: Create addon → Install to SGC → Verify files downloaded → Verify status updated
    - Use test Docker daemon and test database
    - _Requirements: All installation requirements_
  
  - [~] 15.2 Write integration test for collection expansion
    - Test: Fetch collection metadata → Create library entries for all items → Install collection
    - _Requirements: 14.1, 14.2, 14.3_
  
  - [~] 15.3 Write integration test for addon removal
    - Test: Install addon → Remove addon → Verify files deleted → Verify status updated
    - _Requirements: 10.1, 10.2, 10.3_
  
  - [~] 15.4 Write integration test for concurrent downloads
    - Test: Trigger multiple downloads → Verify concurrency limit enforced → Verify all complete
    - _Requirements: 3.7, 17.1_

- [~] 16. Final checkpoint - Ensure all tests pass
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 17. Documentation and deployment
  - [~] 17.1 Update API documentation
    - Document all workshop service RPCs
    - Add examples for common workflows
    - Document error codes and messages
    - _Requirements: All API requirements_
  
  - [~] 17.2 Update deployment documentation
    - Document SteamCMD container requirements
    - Document RabbitMQ queue configuration
    - Document environment variables for configuration
    - _Requirements: All deployment requirements_
  
  - [~] 17.3 Create migration guide
    - Document how to migrate existing game configs to use workshop addons
    - Provide examples for L4D2 and CS2
    - _Requirements: All requirements_

## Notes

- Tasks marked with `*` are optional property-based tests that can be skipped for faster MVP
- Each task references specific requirements for traceability
- Checkpoints ensure incremental validation
- Property tests validate universal correctness properties
- Integration tests validate end-to-end workflows
- The implementation follows ManManV2's split-plane architecture with control plane (API) and execution plane (host manager)
