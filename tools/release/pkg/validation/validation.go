// Package validation provides version validation utilities for the release helper.
package validation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// semanticVersionRegex matches semantic version strings (vX.Y.Z or vX.Y.Z-suffix)
	semanticVersionRegex = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([a-zA-Z0-9\-\.]+))?$`)
)

// SemanticVersion represents a parsed semantic version.
type SemanticVersion struct {
	Major  int
	Minor  int
	Patch  int
	Suffix string
}

// ParseSemanticVersion parses a semantic version string.
func ParseSemanticVersion(version string) (*SemanticVersion, error) {
	matches := semanticVersionRegex.FindStringSubmatch(version)
	if matches == nil {
		return nil, fmt.Errorf("invalid semantic version format: %s (expected vX.Y.Z or vX.Y.Z-suffix)", version)
	}

	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])
	suffix := matches[4]

	return &SemanticVersion{
		Major:  major,
		Minor:  minor,
		Patch:  patch,
		Suffix: suffix,
	}, nil
}

// String returns the string representation of the version.
func (v *SemanticVersion) String() string {
	version := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Suffix != "" {
		version += "-" + v.Suffix
	}
	return version
}

// IncrementMinor increments the minor version and resets patch to 0.
func (v *SemanticVersion) IncrementMinor() *SemanticVersion {
	return &SemanticVersion{
		Major:  v.Major,
		Minor:  v.Minor + 1,
		Patch:  0,
		Suffix: "",
	}
}

// IncrementPatch increments the patch version.
func (v *SemanticVersion) IncrementPatch() *SemanticVersion {
	return &SemanticVersion{
		Major:  v.Major,
		Minor:  v.Minor,
		Patch:  v.Patch + 1,
		Suffix: "",
	}
}

// ValidateSemanticVersion validates that a version string is a valid semantic version.
func ValidateSemanticVersion(version string) error {
	_, err := ParseSemanticVersion(version)
	return err
}

// CompareVersions compares two semantic versions.
// Returns -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2.
func CompareVersions(v1, v2 string) (int, error) {
	ver1, err := ParseSemanticVersion(v1)
	if err != nil {
		return 0, fmt.Errorf("failed to parse v1: %w", err)
	}

	ver2, err := ParseSemanticVersion(v2)
	if err != nil {
		return 0, fmt.Errorf("failed to parse v2: %w", err)
	}

	// Compare major
	if ver1.Major < ver2.Major {
		return -1, nil
	}
	if ver1.Major > ver2.Major {
		return 1, nil
	}

	// Compare minor
	if ver1.Minor < ver2.Minor {
		return -1, nil
	}
	if ver1.Minor > ver2.Minor {
		return 1, nil
	}

	// Compare patch
	if ver1.Patch < ver2.Patch {
		return -1, nil
	}
	if ver1.Patch > ver2.Patch {
		return 1, nil
	}

	// Versions are equal in major.minor.patch
	// If one has a suffix and the other doesn't, the one without is "greater"
	if ver1.Suffix == "" && ver2.Suffix != "" {
		return 1, nil
	}
	if ver1.Suffix != "" && ver2.Suffix == "" {
		return -1, nil
	}

	// Both have suffixes or both don't - compare lexicographically
	return strings.Compare(ver1.Suffix, ver2.Suffix), nil
}
