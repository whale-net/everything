package kinds

import (
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// MetadataH7Hook implements the H7 interface by reading from a centralized
// AppMetadataRegistry. This is the single authoring site (FR-36) for
// app-type → artifact-kind mappings, replacing the five hardcoded enumerations.
type MetadataH7Hook struct {
	kind             appmetapb.ArtifactKind
	metadataRegistry *AppMetadataRegistry
}

// NewMetadataH7Hook constructs an H7 hook that reads from the given metadata registry.
func NewMetadataH7Hook(kind appmetapb.ArtifactKind, registry *AppMetadataRegistry) *MetadataH7Hook {
	return &MetadataH7Hook{
		kind:             kind,
		metadataRegistry: registry,
	}
}

// Name returns the hook identifier "H7".
func (h *MetadataH7Hook) Name() string {
	return "H7"
}

// ValueShaped returns false; H7 is a structural hook (FR-63(d) exempt).
func (h *MetadataH7Hook) ValueShaped() bool {
	return false
}

// AppTypeMapping returns the list of app_types that produce this artifact kind,
// derived from the centralized metadata registry instead of hardcoded.
func (h *MetadataH7Hook) AppTypeMapping() []string {
	if h.metadataRegistry == nil {
		return []string{}
	}
	return h.metadataRegistry.AppTypesForKind(h.kind)
}

// InitializeMetadataH7 initializes H7 hooks for all registered kinds using
// the given metadata registry. This must be called after all kinds are
// registered but before dispatch begins (i.e., before any code consults
// kind.Hooks().H7()).
//
// For each registered kind that has a Binary-like structure, this updates
// its H7 hook to use the metadata-based version, replacing hardcoded
// app-type enumerations with lookups from the registry.
func InitializeMetadataH7(registry *AppMetadataRegistry) {
	if registry == nil {
		return
	}

	// For the Binary kind, update its H7 hook to use metadata.
	// Binary currently publishes ARTIFACT_KIND_BINARY.
	binaryKind := Get("binary")
	if binaryKind != nil {
		if binary, ok := binaryKind.(*Binary); ok {
			binary.h7Metadata = NewMetadataH7Hook(appmetapb.ArtifactKind_ARTIFACT_KIND_BINARY, registry)
		}
	}

	// Future kinds (image, chart, etc.) can be updated similarly here.
	// For now, only binary is handled since that's the only kind that
	// currently has hardcoded app-type mappings.
}
