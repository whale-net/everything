// Package semver is the one shared implementation of "vMAJOR.MINOR.PATCH"
// parsing, incrementing, and numeric comparison used across this repo's
// release tooling.
//
// Before this package existed, tools/release_helper_go/cmd/plan.go had its
// own regex-plus-strings.Split parser, and tools/app_registry (AR-5) needed
// the same logic server-side to populate artifact.version_major/minor/patch
// and to implement AllocateVersion — see
// tools/app_registry/architecture/04-version-model.md ("semver semantics").
// Rather than add a third copy, both consume
// this package: release_helper_go's incrementVersion is now a thin wrapper,
// and tools/app_registry/server/repository/postgres imports it directly.
// This is a plain shared library with no dependency on either caller, which
// is why it lives in libs/go rather than under tools/release_helper_go or
// tools/app_registry — the same reasoning ARCHITECTURE.md gives for
// //tools/appmeta/proto living outside the registry.
package semver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// re matches an optional "v" prefix, three dot-separated integer components,
// and optional prerelease (-foo) / build metadata (+bar) suffixes. Component
// integers are unbounded-width digit runs; Parse itself doesn't limit magnitude.
var re = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)

// Version is a parsed semantic version. Prerelease/Build are populated by
// Parse but rejected by ParseRelease — see architecture/04-version-model.md:
// this repo's release tooling does not support prerelease ordering today,
// but keeps the field so a caller can detect and reject one explicitly
// rather than silently mis-sorting it.
type Version struct {
	Major, Minor, Patch int
	Prerelease          string
	Build               string
}

// Parse parses s as "[v]MAJOR.MINOR.PATCH[-prerelease][+build]". The "v"
// prefix is optional on input; String() always emits it.
func Parse(s string) (Version, error) {
	m := re.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("invalid version %q: expected v<major>.<minor>.<patch>", s)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return Version{}, fmt.Errorf("invalid version %q: major component: %w", s, err)
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return Version{}, fmt.Errorf("invalid version %q: minor component: %w", s, err)
	}
	patch, err := strconv.Atoi(m[3])
	if err != nil {
		return Version{}, fmt.Errorf("invalid version %q: patch component: %w", s, err)
	}
	return Version{Major: major, Minor: minor, Patch: patch, Prerelease: m[4], Build: m[5]}, nil
}

// ParseRelease parses like Parse but rejects a prerelease or build-metadata
// suffix. This is the AllocateVersion contract from
// tools/app_registry/architecture/04-version-model.md: "AllocateVersion
// rejects a prerelease or build-metadata version
// explicitly ... rather than half-accepting one and sorting it wrongly."
func ParseRelease(s string) (Version, error) {
	v, err := Parse(s)
	if err != nil {
		return Version{}, err
	}
	if v.Prerelease != "" || v.Build != "" {
		return Version{}, fmt.Errorf("version %q has a prerelease or build-metadata suffix, which is not supported here", s)
	}
	return v, nil
}

// String renders v as "vMAJOR.MINOR.PATCH", dropping any prerelease/build
// metadata — matching every version string this repo's tooling actually
// produces (plan.go, RecordArtifact, AllocateVersion all reject or strip
// prerelease before this point).
func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// IsRelease reports whether v carries no prerelease or build metadata.
func (v Version) IsRelease() bool { return v.Prerelease == "" && v.Build == "" }

// Increment returns the result of bumping v by kind ("major", "minor", or
// "patch"), following standard semver reset rules: a major bump zeros minor
// and patch, a minor bump zeros patch. Prerelease/build metadata is dropped,
// matching plan.go's pre-existing behaviour of stripping prerelease before
// bumping.
func (v Version) Increment(kind string) (Version, error) {
	switch kind {
	case "major":
		return Version{Major: v.Major + 1}, nil
	case "minor":
		return Version{Major: v.Major, Minor: v.Minor + 1}, nil
	case "patch":
		return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}, nil
	default:
		return Version{}, fmt.Errorf("unknown increment type: %s", kind)
	}
}

// Compare returns -1, 0, or 1 comparing a and b by MAJOR, then MINOR, then
// PATCH — numerically, never lexically. This is the load-bearing behaviour
// architecture/04-version-model.md exists to guarantee: lexical TEXT ordering
// puts "v1.10.0" before "v1.9.0", which is wrong.
func Compare(a, b Version) int {
	if a.Major != b.Major {
		return compareInt(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return compareInt(a.Minor, b.Minor)
	}
	return compareInt(a.Patch, b.Patch)
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
