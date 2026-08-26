package kinds

// Conformance declares the checked-in metadata that enforces FR-63's four
// mechanical checks: (a) hook-set closure, (b) kind-identity ban inside common
// mechanisms, (c) vacuity guard on common-mechanism resolution, and (d)
// value-shaped-hook literal bans across the repository.
//
// This is the single source of truth for what constitutes a "common mechanism"
// and which locations are exempt from value-shaped-hook literal bans. Any
// change to this declaration is itself subject to review and testing.
//
// See FR-63 and the Testing phase acceptance criteria.

// CommonMechanisms is the declared set of locations constituting common
// mechanisms — code paths that every kind uses without branching on kind
// identity, except through hook dispatch.
//
// Each entry is a Go import path that implements one row of FR-35's left column
// (a mechanism that handles a property the publishing pipeline manages).
// The test file conformance_test.go resolves these paths to source files and
// verifies that:
//   - They import no mechanism packages for other mechanisms (no circular logic)
//   - They branch on kind identity only through hook dispatch, never directly
//   - They contain no literal values for value-shaped hooks (H3, H4, H5, H6, H8)
//     except in special exempted locations listed below
//
// See FR-63(c).
var CommonMechanisms = []string{
	// TODO: Placeholder entries will be populated as common mechanisms are implemented.
	// Examples of what will go here:
	// "github.com/whale-net/everything/tools/app_registry/publish/compression",
	// "github.com/whale-net/everything/tools/app_registry/publish/manifest",
	// "github.com/whale-net/everything/tools/app_registry/publish/upload",
	// "github.com/whale-net/everything/tools/app_registry/publish/variants",
}

// StructuralHookExemptions lists hook names that are exempt from the
// value-shaped-hook literal ban because they are structural, not value-shaped.
// Only H1, H2, H7 should appear here.
//
// See FR-63(d): "H1, H2, H7 are structural and exempt, and that exemption is
// declared rather than implied."
var StructuralHookExemptions = map[string]bool{
	"H1": true, // Which build outputs constitute the artifact set
	"H2": true, // Variant selector's dimensions and enumeration
	"H7": true, // App-type → artifact-kind mapping value
}

// ValueShapedHookBans defines the set of hooks whose policy values are
// subject to the literal ban. These are value-shaped hooks: H3, H4, H5, H6, H8.
// Their literal values may appear only in:
//   1. A kind's own hook declaration
//   2. Inside a common mechanism (code location)
//   3. At a declared exempt location (see BanExemptLocations below)
//
// The hook name is the key; the value is a human-readable description of the
// policy (for test output).
//
// See FR-63(d).
var ValueShapedHookBans = map[string]string{
	"H3": "per-file content type",
	"H4": "stored compression/encoding policy",
	"H5": "consumer-facing file naming",
	"H6": "checksum manifest format",
	"H8": "pre-cutover key and file-name derivation",
}

// BanExemptLocation represents a single location exempt from a hook's literal ban.
type BanExemptLocation struct {
	// Hook is the hook name (e.g., "H8").
	Hook string
	// Path is the file or directory path relative to the repo root where this
	// hook's policy value may appear (e.g., "tools/app_registry/ENV.md").
	Path string
	// Reason is a human-readable explanation of why this location is exempt
	// (e.g., "ENV.md key-naming convention block for pre-cutover template").
	Reason string
}

// BanExemptLocations is the complete set of locations exempt from value-shaped-
// hook literal bans. For each hook and location pair, the test resolves the
// path to one or more actual files and verifies that:
//   - The file exists (a stale exemption fails the test)
//   - The hook's policy value appears in the file
//   - No other location contains that value outside the declared set
//
// New exemptions are added here; the test is the enforcement mechanism.
//
// See FR-63(d): "The exempt locations, exhaustively: the ban's domain and its
// exemptions live in the same checked-in declaration as (c)'s common-mechanism
// set."
var BanExemptLocations = []BanExemptLocation{
	// H8 exemption: pre-cutover template documentation in ENV.md
	{
		Hook:   "H8",
		Path:   "tools/app_registry/ENV.md",
		Reason: "ENV.md key-naming convention block for pre-cutover template derivation",
	},
	// H8 exemption: pre-cutover recovery procedure in recovery documentation
	{
		Hook:   "H8",
		Path:   "tools/release_helper_go/docs/RECOVERY.md",
		Reason: "FR-40 step 3's recovery-only appendix for artifact key derivation",
	},
}

// AffordanceExemptions is a map for extension points that other tasks will add
// to. For example, FR-73(c)'s URL-stripping conformance affordance might add an
// entry here describing a location where URL-stripping checks are exempted.
//
// The structure allows future tasks to declare exemptions without modifying
// the CommonMechanisms, StructuralHookExemptions, ValueShapedHookBans, or
// BanExemptLocations directly.
//
// See issue #8 in the plan.
var AffordanceExemptions = map[string]interface{}{
	// FR-37: manifest-seeding exemption for test setup
	// The firmware fixture (FR-37) seeds a firmware-typed, already-reconciled owner
	// directly into the manifest snapshot for test setup only, as a declared
	// exemption from FR-64's "reconcile is the sole propagation mechanism".
	// This exemption applies only to test setup paths, not production.
	// See FR-37's test doc comment and citest/firmware_fixture_test.go.
	"manifest-seeding-test-exemption": map[string]string{
		"affordance": "Direct manifest seeding for firmware fixture test setup",
		"location":   "tools/app_registry/citest/firmware_fixture_test.go",
		"scope":      "Test setup only, not production",
		"reason":     "FR-37 firmware genericity fixture requires pre-seeded owner to test full upload/confirm/publish/resolve chain",
	},
}
