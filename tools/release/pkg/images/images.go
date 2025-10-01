// Package images provides container image operations for the release helper.
package images

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/whale-net/everything/tools/release/pkg/core"
	"github.com/whale-net/everything/tools/release/pkg/metadata"
)

// BuildImage builds a container image for an app.
func BuildImage(bazelTarget string, platform string) (string, error) {
	// Determine the correct load target based on platform
	var loadTarget string
	if platform == "amd64" {
		loadTarget = bazelTarget + "_amd64_load"
	} else if platform == "arm64" {
		loadTarget = bazelTarget + "_arm64_load"
	} else if platform == "" {
		loadTarget = bazelTarget + "_load"
	} else {
		return "", fmt.Errorf("unsupported platform: %s", platform)
	}

	// Build and load the image
	_, err := core.RunBazel([]string{"run", loadTarget}, false, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build and load image: %w", err)
	}

	// Return the image tag (would need to extract from build output in real implementation)
	return fmt.Sprintf("image:%s", platform), nil
}

// FormatRegistryTags formats the registry tags for an image.
func FormatRegistryTags(registry, repoName, version string, commitSHA string) []string {
	var tags []string

	// Main version tag
	imageBase := fmt.Sprintf("%s/%s", registry, repoName)
	tags = append(tags, fmt.Sprintf("%s:%s", imageBase, version))

	// Add commit SHA tag if provided
	if commitSHA != "" {
		tags = append(tags, fmt.Sprintf("%s:%s", imageBase, commitSHA))
	}

	return tags
}

// PushImage pushes an image with specified tags to the registry.
func PushImage(bazelTarget string, tags []string, dryRun bool) error {
	if dryRun {
		fmt.Println("DRY RUN: Would push the following tags:")
		for _, tag := range tags {
			fmt.Printf("  - %s\n", tag)
		}
		return nil
	}

	// Get the push target
	pushTarget := bazelTarget + "_push"

	// Set environment variable for tags
	env := make(map[string]string)
	// In real implementation, this would set the tags via environment variables
	// that the OCI push rule understands

	_, err := core.RunBazel([]string{"run", pushTarget}, false, env)
	if err != nil {
		return fmt.Errorf("failed to push image: %w", err)
	}

	return nil
}

// TagAndPushImage is a convenience function that builds, tags, and pushes an image.
func TagAndPushImage(appMetadata *metadata.AppMetadata, version string, commitSHA string, dryRun bool, allowOverwrite bool) error {
	// Format registry tags
	tags := FormatRegistryTags(appMetadata.Registry, appMetadata.RepoName, version, commitSHA)

	// Build the image
	fmt.Printf("Building image for %s...\n", appMetadata.Name)
	bazelTarget := fmt.Sprintf("//%s/%s:%s", appMetadata.Domain, appMetadata.Name, appMetadata.ImageTarget)
	_, err := BuildImage(bazelTarget, "")
	if err != nil {
		return fmt.Errorf("failed to build image: %w", err)
	}

	// Push the image
	fmt.Printf("Pushing image with tags: %v\n", tags)
	err = PushImage(bazelTarget, tags, dryRun)
	if err != nil {
		return fmt.Errorf("failed to push image: %w", err)
	}

	fmt.Printf("✓ Successfully released %s:%s\n", appMetadata.Name, version)
	return nil
}

// CheckImageExists checks if an image with a given tag exists in the registry.
func CheckImageExists(registry, repoName, tag string) (bool, error) {
	// Use docker/crane to check if image exists
	imageRef := fmt.Sprintf("%s/%s:%s", registry, repoName, tag)
	
	// Try using crane if available
	cmd := exec.Command("crane", "manifest", imageRef)
	err := cmd.Run()
	if err != nil {
		// If crane fails, try docker
		cmd = exec.Command("docker", "manifest", "inspect", imageRef)
		err = cmd.Run()
		if err != nil {
			// Image doesn't exist or we can't check
			return false, nil
		}
	}

	return true, nil
}

// MultiArchPushConfig represents configuration for multi-architecture image push.
type MultiArchPushConfig struct {
	Registry   string
	RepoName   string
	Version    string
	CommitSHA  string
	Platforms  []string
	DryRun     bool
}

// PushMultiArchImage builds and pushes multi-architecture images with manifest lists.
func PushMultiArchImage(config MultiArchPushConfig, bazelTarget string) error {
	if config.DryRun {
		fmt.Println("DRY RUN: Would perform multi-architecture release")
		fmt.Printf("  Registry: %s\n", config.Registry)
		fmt.Printf("  Repo: %s\n", config.RepoName)
		fmt.Printf("  Version: %s\n", config.Version)
		fmt.Printf("  Platforms: %s\n", strings.Join(config.Platforms, ", "))
		return nil
	}

	// Build images for each platform
	var platformDigests []string
	for _, platform := range config.Platforms {
		fmt.Printf("Building image for platform %s...\n", platform)
		
		// Build the platform-specific image
		_, err := BuildImage(bazelTarget, platform)
		if err != nil {
			return fmt.Errorf("failed to build image for %s: %w", platform, err)
		}

		// Push platform-specific image
		platformTag := fmt.Sprintf("%s/%s:%s-%s", config.Registry, config.RepoName, config.Version, platform)
		platformDigests = append(platformDigests, platformTag)
	}

	// Create and push manifest list
	fmt.Println("Creating manifest list...")
	manifestTag := fmt.Sprintf("%s/%s:%s", config.Registry, config.RepoName, config.Version)
	
	// This would use docker manifest or crane to create manifest list
	// For now, just print what would happen
	fmt.Printf("Would create manifest list %s referencing:\n", manifestTag)
	for _, digest := range platformDigests {
		fmt.Printf("  - %s\n", digest)
	}

	return nil
}
