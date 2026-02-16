package kraken

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CreateManifestList creates a Docker manifest list for multi-architecture images.
func CreateManifestList(registryRepo, version string, platforms []string) error {
	if platforms == nil {
		platforms = []string{"amd64", "arm64"}
	}

	manifestName := fmt.Sprintf("%s:%s", registryRepo, version)

	var platformImages []string
	for _, platform := range platforms {
		platformImages = append(platformImages, fmt.Sprintf("%s:%s-%s", registryRepo, version, platform))
	}

	fmt.Printf("Creating manifest list %s for platforms: %s\n", manifestName, strings.Join(platforms, ", "))

	args := append([]string{"manifest", "create", manifestName}, platformImages...)
	cmd := exec.Command("docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create manifest list: %s\n%w", string(out), err)
	}

	for _, platform := range platforms {
		platformImage := fmt.Sprintf("%s:%s-%s", registryRepo, version, platform)
		arch := platform
		annotateCmd := exec.Command("docker", "manifest", "annotate", manifestName, platformImage, "--arch", arch, "--os", "linux")
		if out, err := annotateCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to annotate manifest: %s\n%w", string(out), err)
		}
	}

	fmt.Printf("Successfully created manifest list %s\n", manifestName)
	return nil
}

// PushManifestList pushes a Docker manifest list to the registry.
func PushManifestList(registryRepo, version string) error {
	manifestName := fmt.Sprintf("%s:%s", registryRepo, version)
	fmt.Printf("Pushing manifest list %s\n", manifestName)

	cmd := exec.Command("docker", "manifest", "push", manifestName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to push manifest list: %s\n%w", string(out), err)
	}

	fmt.Printf("Successfully pushed manifest list %s\n", manifestName)
	return nil
}

// RegistryTags holds container registry tags for an app.
type RegistryTags struct {
	Latest  string
	Version string
	Commit  string
}

// FormatRegistryTags formats container registry tags for an app.
func FormatRegistryTags(domain, appName, version, registry string, commitSHA string, platform string) RegistryTags {
	imageName := fmt.Sprintf("%s-%s", domain, appName)

	var baseRepo string
	if registry == "ghcr.io" {
		if owner := os.Getenv("GITHUB_REPOSITORY_OWNER"); owner != "" {
			baseRepo = fmt.Sprintf("%s/%s/%s", registry, strings.ToLower(owner), imageName)
		} else {
			baseRepo = fmt.Sprintf("%s/%s", registry, imageName)
		}
	} else {
		baseRepo = fmt.Sprintf("%s/%s", registry, imageName)
	}

	platformSuffix := ""
	if platform != "" {
		platformSuffix = "-" + platform
	}

	tags := RegistryTags{
		Latest:  fmt.Sprintf("%s:latest%s", baseRepo, platformSuffix),
		Version: fmt.Sprintf("%s:%s%s", baseRepo, version, platformSuffix),
	}

	if commitSHA != "" {
		tags.Commit = fmt.Sprintf("%s:%s%s", baseRepo, commitSHA, platformSuffix)
	}

	return tags
}

// TagsList returns all tags as a slice.
func (t RegistryTags) TagsList() []string {
	tags := []string{t.Latest, t.Version}
	if t.Commit != "" {
		tags = append(tags, t.Commit)
	}
	return tags
}

// BuildImage builds and loads a container image for an app.
func BuildImage(bazelTarget string, platform string) (string, error) {
	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return "", err
	}

	appPath := strings.SplitN(bazelTarget[2:], ":", 2)[0]
	loadTarget := fmt.Sprintf("//%s:%s_image_load", appPath, metadata.Name)

	fmt.Printf("Building and loading %s for platform %s (using optimized oci_load)...\n", loadTarget, orDefault(platform, "default"))

	buildArgs := []string{"run", loadTarget}
	switch platform {
	case "arm64":
		buildArgs = append(buildArgs[:1], append([]string{"--platforms=//tools:linux_arm64"}, buildArgs[1:]...)...)
	case "amd64":
		buildArgs = append(buildArgs[:1], append([]string{"--platforms=//tools:linux_x86_64"}, buildArgs[1:]...)...)
	}

	_, err = RunBazel(buildArgs, true, nil)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s-%s:latest", metadata.Domain, metadata.Name), nil
}

// PushImageWithTags pushes a container image with multiple tags to the registry.
func PushImageWithTags(bazelTarget string, tags []string) error {
	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return err
	}

	appPath := strings.SplitN(bazelTarget[2:], ":", 2)[0]
	pushTarget := fmt.Sprintf("//%s:%s_image_push", appPath, metadata.Name)

	fmt.Printf("Pushing %d tags using %s...\n", len(tags), pushTarget)

	var tagNames []string
	for _, tag := range tags {
		parts := strings.SplitN(tag, ":", 2)
		if len(parts) == 2 {
			tagNames = append(tagNames, parts[1])
		}
	}

	fmt.Printf("Pushing with tags: %s\n", strings.Join(tagNames, ", "))

	bazelArgs := []string{"run", pushTarget, "--"}
	for _, tagName := range tagNames {
		bazelArgs = append(bazelArgs, "--tag", tagName)
	}

	_, err = RunBazel(bazelArgs, false, nil)
	if err != nil {
		return fmt.Errorf("failed to push image: %w", err)
	}

	fmt.Printf("Successfully pushed image with %d tags\n", len(tagNames))
	return nil
}

// ReleaseMultiarchImage releases a multi-architecture image using OCI image index.
func ReleaseMultiarchImage(bazelTarget, version, registry string, platforms []string, commitSHA string) error {
	if platforms == nil {
		platforms = []string{"amd64", "arm64"}
	}

	metadata, err := GetAppMetadata(bazelTarget)
	if err != nil {
		return err
	}

	appPath := strings.SplitN(bazelTarget[2:], ":", 2)[0]
	imageName := fmt.Sprintf("%s-%s", metadata.Domain, metadata.Name)

	fmt.Printf("Releasing multi-architecture image: %s\n", imageName)
	fmt.Printf("Version: %s\n", version)
	fmt.Printf("Platforms: %s\n", strings.Join(platforms, ", "))

	// Build the OCI image index
	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	fmt.Println("Building OCI image index with platform transitions...")
	fmt.Printf("%s\n", strings.Repeat("=", 80))

	indexTarget := fmt.Sprintf("//%s:%s_image", appPath, metadata.Name)
	fmt.Printf("Building index: %s\n", indexTarget)

	_, err = RunBazel([]string{"build", indexTarget}, true, nil)
	if err != nil {
		return err
	}
	fmt.Printf("✅ Built OCI image index containing %d platform variants\n", len(platforms))

	// Push with all tags
	tags := FormatRegistryTags(metadata.Domain, metadata.Name, version, registry, commitSHA, "")

	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	fmt.Printf("Pushing OCI image index with tags...\n")
	fmt.Printf("%s\n", strings.Repeat("=", 80))

	if err := PushImageWithTags(bazelTarget, tags.TagsList()); err != nil {
		return err
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	fmt.Printf("✅ Successfully released %s:%s\n", imageName, version)
	fmt.Printf("%s\n", strings.Repeat("=", 80))
	fmt.Println("\nPublished tags:")
	for _, tag := range tags.TagsList() {
		fmt.Printf("  - %s\n", tag)
	}
	fmt.Printf("\nThe image index contains %d platform variants: %s\n", len(platforms), strings.Join(platforms, ", "))
	fmt.Println("Docker will automatically select the correct architecture when users pull.")

	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
