package kraken

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

var semverValidationRegex = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[a-zA-Z0-9\-\.]+)?$`)

// ValidateSemanticVersion validates that version follows semantic versioning format.
func ValidateSemanticVersion(version string) bool {
	return semverValidationRegex.MatchString(version)
}

// CheckVersionExistsInRegistry checks if a version already exists in the container registry.
func CheckVersionExistsInRegistry(bazelTarget, version string) bool {
	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return false
	}

	registry := metadata.Registry
	domain := metadata.Domain
	appName := metadata.Name

	imageName := fmt.Sprintf("%s-%s", domain, appName)

	var imageRef string
	if registry == "ghcr.io" {
		if owner := os.Getenv("GITHUB_REPOSITORY_OWNER"); owner != "" {
			imageRef = fmt.Sprintf("%s/%s/%s:%s", registry, strings.ToLower(owner), imageName, version)
		} else {
			imageRef = fmt.Sprintf("%s/%s:%s", registry, imageName, version)
		}
	} else {
		imageRef = fmt.Sprintf("%s/%s:%s", registry, imageName, version)
	}

	cmd := exec.Command("docker", "manifest", "inspect", imageRef)
	out, err := cmd.CombinedOutput()
	if err != nil {
		stderr := string(out)
		stderrLower := strings.ToLower(stderr)
		if strings.Contains(stderrLower, "manifest unknown") ||
			strings.Contains(stderrLower, "not found") ||
			strings.Contains(stderrLower, "name invalid") ||
			strings.Contains(stderrLower, "unauthorized") {
			return false
		}
		fmt.Fprintf(os.Stderr, "Warning: Could not definitively check if %s exists: %s\n", imageRef, stderr)
		fmt.Fprintln(os.Stderr, "Proceeding with caution - this may overwrite an existing version")
		return false
	}
	return true
}

// ValidateReleaseVersion validates that a release version is valid and doesn't already exist.
func ValidateReleaseVersion(bazelTarget, version string, allowOverwrite bool) error {
	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return err
	}
	appName := metadata.Name

	if version != "latest" && !ValidateSemanticVersion(version) {
		return fmt.Errorf(
			"version '%s' does not follow semantic versioning format. "+
				"Expected format: v{major}.{minor}.{patch} (e.g., v1.0.0, v2.1.3, v1.0.0-beta1)",
			version,
		)
	}

	if version == "latest" {
		fmt.Fprintf(os.Stderr, "✓ Allowing overwrite of 'latest' tag for app '%s' (main branch workflow)\n", appName)
		return nil
	}

	if !allowOverwrite {
		if CheckVersionExistsInRegistry(bazelTarget, version) {
			return fmt.Errorf(
				"version '%s' already exists for app '%s'. "+
					"Refusing to overwrite existing version. Use a different version number.",
				version, appName,
			)
		}
		fmt.Fprintf(os.Stderr, "✓ Version '%s' is available for app '%s'\n", version, appName)
	} else {
		fmt.Fprintf(os.Stderr, "⚠️  Allowing overwrite of version '%s' for app '%s' (if it exists)\n", version, appName)
	}

	return nil
}

// GetAppFullName returns the full domain-appname format for an app.
func GetAppFullName(app AppInfo) string {
	return fmt.Sprintf("%s-%s", app.Domain, app.Name)
}

// GetAvailableDomains returns a sorted list of all available domains.
func GetAvailableDomains() ([]string, error) {
	allApps, err := ListAllApps()
	if err != nil {
		return nil, err
	}
	domainSet := make(map[string]bool)
	for _, app := range allApps {
		domainSet[app.Domain] = true
	}
	var domains []string
	for d := range domainSet {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	return domains, nil
}

// ValidateDomain validates that a domain exists and returns all apps in that domain.
func ValidateDomain(domainName string) ([]AppInfo, error) {
	allApps, err := ListAllApps()
	if err != nil {
		return nil, err
	}
	var domainApps []AppInfo
	for _, app := range allApps {
		if app.Domain == domainName {
			domainApps = append(domainApps, app)
		}
	}
	if len(domainApps) == 0 {
		domains, _ := GetAvailableDomains()
		return nil, fmt.Errorf("domain '%s' not found. Available domains: %s", domainName, strings.Join(domains, ", "))
	}
	return domainApps, nil
}

// IsDomainName checks if a name appears to be a domain name.
func IsDomainName(name string) bool {
	domains, err := GetAvailableDomains()
	if err != nil {
		return false
	}
	for _, d := range domains {
		if d == name {
			return true
		}
	}
	return false
}

// ValidateApps validates that requested apps exist and returns the valid ones.
// Apps can be referenced in multiple formats:
//   - Full format: domain-appname (e.g., "demo-hello_python")
//   - Short format: appname (e.g., "hello_python") - only if unambiguous
//   - Path format: domain/appname (e.g., "demo/hello_python")
//   - Domain format: domain (e.g., "demo") - returns all apps in that domain
func ValidateApps(requestedApps []string) ([]AppInfo, error) {
	allApps, err := ListAllApps()
	if err != nil {
		return nil, err
	}

	fullNameLookup := make(map[string]AppInfo)
	shortNameLookup := make(map[string][]AppInfo)
	pathLookup := make(map[string]AppInfo)

	for _, app := range allApps {
		fullName := GetAppFullName(app)
		fullNameLookup[fullName] = app

		pathName := fmt.Sprintf("%s/%s", app.Domain, app.Name)
		pathLookup[pathName] = app

		shortNameLookup[app.Name] = append(shortNameLookup[app.Name], app)
	}

	var validApps []AppInfo
	var invalidApps []string

	for _, requested := range requestedApps {
		// Check if this is a domain name first
		if IsDomainName(requested) {
			domainApps, err := ValidateDomain(requested)
			if err != nil {
				invalidApps = append(invalidApps, requested)
				continue
			}
			validApps = append(validApps, domainApps...)
			continue
		}

		// Try full format (domain-name)
		if app, ok := fullNameLookup[requested]; ok {
			validApps = append(validApps, app)
			continue
		}

		// Try path format (domain/name)
		if app, ok := pathLookup[requested]; ok {
			validApps = append(validApps, app)
			continue
		}

		// Try short format (name only) - only if unambiguous
		if matches, ok := shortNameLookup[requested]; ok {
			if len(matches) == 1 {
				validApps = append(validApps, matches[0])
				continue
			}
			// Ambiguous
			var ambiguous []string
			for _, a := range matches {
				ambiguous = append(ambiguous, GetAppFullName(a))
			}
			invalidApps = append(invalidApps, fmt.Sprintf("%s (ambiguous, could be: %s)", requested, strings.Join(ambiguous, ", ")))
			continue
		}

		invalidApps = append(invalidApps, requested)
	}

	if len(invalidApps) > 0 {
		var availableFull []string
		for _, app := range allApps {
			availableFull = append(availableFull, GetAppFullName(app))
		}
		sort.Strings(availableFull)

		domains, _ := GetAvailableDomains()

		return nil, fmt.Errorf(
			"Invalid apps: %s.\n"+
				"Available apps: %s\n"+
				"Available domains: %s\n"+
				"You can use: full format (domain-appname, e.g. demo-hello_python), "+
				"path format (domain/appname, e.g. demo/hello_python), short format (appname, e.g. hello_python, if unambiguous), "+
				"or domain format (domain, e.g. demo)",
			strings.Join(invalidApps, ", "),
			strings.Join(availableFull, ", "),
			strings.Join(domains, ", "),
		)
	}

	return validApps, nil
}
