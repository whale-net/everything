// Package release provides utilities for managing app releases in the monorepo.
package release

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// semverPattern matches v{major}.{minor}.{patch} with an optional pre-release suffix.
var semverPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-[a-zA-Z0-9\-\.]+)?$`)

// Version holds the parsed components of a semantic version string.
type Version struct {
	Major      int
	Minor      int
	Patch      int
	PreRelease string // empty if not a pre-release
}

// String reconstructs the canonical "vMAJOR.MINOR.PATCH[-prerelease]" form.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.PreRelease != "" {
		s += "-" + v.PreRelease
	}
	return s
}

// ValidateSemanticVersion reports whether version follows the v{major}.{minor}.{patch}
// format, with an optional pre-release suffix (e.g. v1.0.0-beta1).
// The special value "latest" is NOT considered valid by this function — callers that
// need to accept "latest" must check for it explicitly.
func ValidateSemanticVersion(version string) bool {
	return semverPattern.MatchString(version)
}

// ParseSemanticVersion parses a version string of the form "vMAJOR.MINOR.PATCH" or
// "vMAJOR.MINOR.PATCH-prerelease". The leading "v" is optional.
func ParseSemanticVersion(version string) (Version, error) {
	s := strings.TrimPrefix(version, "v")

	// Split pre-release suffix on the first '-'
	parts := strings.SplitN(s, "-", 2)
	core := parts[0]
	preRelease := ""
	if len(parts) == 2 {
		preRelease = parts[1]
	}

	components := strings.Split(core, ".")
	if len(components) != 3 {
		return Version{}, fmt.Errorf("invalid semantic version format: %q", version)
	}

	major, err := strconv.Atoi(components[0])
	if err != nil {
		return Version{}, fmt.Errorf("invalid semantic version format: %q", version)
	}
	minor, err := strconv.Atoi(components[1])
	if err != nil {
		return Version{}, fmt.Errorf("invalid semantic version format: %q", version)
	}
	patch, err := strconv.Atoi(components[2])
	if err != nil {
		return Version{}, fmt.Errorf("invalid semantic version format: %q", version)
	}

	return Version{Major: major, Minor: minor, Patch: patch, PreRelease: preRelease}, nil
}

// IncrementMinorVersion increments the minor component and resets patch to 0.
// The pre-release suffix is dropped in the result.
func IncrementMinorVersion(current string) (string, error) {
	v, err := ParseSemanticVersion(current)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d.%d.0", v.Major, v.Minor+1), nil
}

// IncrementPatchVersion increments the patch component.
// The pre-release suffix is dropped in the result.
func IncrementPatchVersion(current string) (string, error) {
	v, err := ParseSemanticVersion(current)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch+1), nil
}
