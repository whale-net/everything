package kinds

import (
	"fmt"
)

// Hook represents one of the eight hooks in the kind system (H1-H8).
// Each hook supplies a policy that a kind declares once, and one or more
// common mechanisms consume via dispatch.
//
// See ARCHITECTURE.md "Kind hooks (H1-H8)" and FR-63 for the full specification.
type Hook interface {
	// Name returns the hook identifier ("H1", "H2", ..., "H8").
	Name() string

	// ValueShaped reports whether this hook's policy is value-shaped (true for
	// H3, H4, H5, H6, H8) or structural (false for H1, H2, H7). Value-shaped
	// hooks' literal policy values are subject to the literal ban: they may
	// appear only in a kind's declaration, inside common mechanisms, and at
	// declared exempt locations. Structural hooks are exempt from this ban.
	//
	// See FR-63(d).
	ValueShaped() bool
}

// H1 is the hook for "which build outputs constitute the artifact set, and how
// they are produced". Supplied by kinds that publish multiple files per build.
// Example: binaries publish one file per {os, arch} pair. Structural hook.
type H1 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// H2 is the hook for "the variant selector's dimensions and their enumeration".
// Determines the axes across which artifacts are versioned and independently
// promoted. Example: binaries use {os, arch}. Structural hook.
type H2 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// H3 is the hook for "per-file content type sent on upload and stored on the
// object". Part of blob identity (FR-61). Value-shaped hook.
type H3 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// H4 is the hook for "stored compression/encoding policy". Determines whether
// files are gzipped before storage, and how decompression is signaled. Related
// to FR-30 (compression handling) and FR-61 (blob identity). Value-shaped hook.
type H4 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// H5 is the hook for "consumer-facing file naming within a version". Determines
// the structure of file names written into the manifest and transported to
// consumers (FR-67). Value-shaped hook.
type H5 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// H6 is the hook for "the checksum manifest's format, granularity, and
// per-file entry naming". Controls the structure of the manifest that proves
// integrity (FR-19). Does not control whether a manifest exists — that is
// required for all kinds. Value-shaped hook.
type H6 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// H7 is the hook for "the app-type → artifact-kind mapping value".
// Maps deploy_unit.app_type values to artifact kinds (e.g. "external-api" →
// ARTIFACT_KIND_IMAGE). Structural hook (FR-64).
type H7 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// H8 is the hook for "the kind's pre-cutover key and file-name derivation
// convention". Supplies the template for pre-cutover artifact naming (FR-27,
// FR-67). Legitimately empty for kinds with no pre-cutover history; empty
// means no derivation, not found — never a fabricated key. Value-shaped hook.
type H8 interface {
	Hook
	// TODO: Implementation will populate this during Phase 2 (Implementation).
}

// HookSet defines all eight hooks a kind must supply.
type HookSet interface {
	H1() H1
	H2() H2
	H3() H3
	H4() H4
	H5() H5
	H6() H6
	H7() H7
	H8() H8
}

// Kind represents a publishable artifact kind. Every kind supplies a HookSet.
type Kind interface {
	// Name returns the kind's name (e.g. "binary", "firmware", "image", "chart").
	Name() string

	// Hooks returns the full set of eight hooks this kind supplies.
	Hooks() HookSet
}

// KindRegistry maps kind names to their implementations. A single global
// instance is used by common mechanisms for dispatch.
var kindRegistry = make(map[string]Kind)

// Register adds a kind to the registry. Called at init() time by kind
// implementations. Panics if a kind with the same name is already registered.
func Register(kind Kind) {
	if _, exists := kindRegistry[kind.Name()]; exists {
		panic(fmt.Sprintf("kind %q already registered", kind.Name()))
	}
	kindRegistry[kind.Name()] = kind
}

// Get retrieves a registered kind by name. Returns nil if not found.
func Get(name string) Kind {
	return kindRegistry[name]
}

// All returns a map of all registered kinds (name → Kind).
func All() map[string]Kind {
	result := make(map[string]Kind)
	for k, v := range kindRegistry {
		result[k] = v
	}
	return result
}
