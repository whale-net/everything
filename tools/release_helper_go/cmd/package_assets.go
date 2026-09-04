package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// PackagedAsset describes one compiled/extracted release artifact.
type PackagedAsset struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// CLIPlatform specifies a cross-compilation target platform for CLI binaries.
type CLIPlatform struct {
	Name          string // e.g. "linux_amd64", "linux-amd64"
	BazelPlatform string // e.g. "//tools:linux_x86_64"
	OS            string // "linux", "darwin"
	Arch          string // "amd64", "arm64"
}

// DefaultCLIPlatforms defines the standard target platforms for CLI binaries.
var DefaultCLIPlatforms = []CLIPlatform{
	{Name: "linux_amd64", BazelPlatform: "//tools:linux_x86_64", OS: "linux", Arch: "amd64"},
	{Name: "linux_arm64", BazelPlatform: "//tools:linux_arm64", OS: "linux", Arch: "arm64"},
	{Name: "darwin_arm64", BazelPlatform: "//tools:darwin_arm64", OS: "darwin", Arch: "arm64"},
}

// normalizePlatform matches a string to a CLIPlatform.
func normalizePlatform(s string) (CLIPlatform, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "linux_amd64", "linux-amd64", "linux_x86_64", "linux-x86_64", "amd64":
		return CLIPlatform{Name: "linux_amd64", BazelPlatform: "//tools:linux_x86_64", OS: "linux", Arch: "amd64"}, nil
	case "linux_arm64", "linux-arm64", "arm64":
		return CLIPlatform{Name: "linux_arm64", BazelPlatform: "//tools:linux_arm64", OS: "linux", Arch: "arm64"}, nil
	case "darwin_arm64", "darwin-arm64", "macos_arm64", "macos-arm64":
		return CLIPlatform{Name: "darwin_arm64", BazelPlatform: "//tools:darwin_arm64", OS: "darwin", Arch: "arm64"}, nil
	case "darwin_amd64", "darwin-amd64", "darwin_x86_64", "darwin-x86_64", "macos_x86_64", "macos-x86_64":
		return CLIPlatform{Name: "darwin_amd64", BazelPlatform: "//tools:darwin_x86_64", OS: "darwin", Arch: "amd64"}, nil
	default:
		return CLIPlatform{}, fmt.Errorf("unsupported platform %q (expected linux_amd64, linux_arm64, darwin_arm64, darwin_amd64)", s)
	}
}

// firmwareTarget derives the build target for a firmware app.
func firmwareTarget(meta AppMetadata) string {
	target := meta.BinaryTarget
	if strings.HasSuffix(target, "_merged_bin") || strings.HasSuffix(target, "_bin") {
		return target
	}
	pkg := appBazelPkg(meta)
	targetName := meta.Name
	if idx := strings.Index(target, ":"); idx >= 0 {
		targetName = target[idx+1:]
	}
	// Prefer _merged_bin for full 0x0 flashable binary
	return fmt.Sprintf("//%s:%s_merged_bin", pkg, targetName)
}

// binaryTargetPkgAndName extracts the package and base target name from a label.
func binaryTargetPkgAndName(meta AppMetadata) (string, string) {
	target := meta.BinaryTarget
	pkg := appBazelPkg(meta)
	name := meta.Name
	if idx := strings.Index(target, ":"); idx >= 0 {
		name = target[idx+1:]
	}
	return pkg, name
}

// computeFileSHA256 reads a file and computes its SHA256 hex digest.
func computeFileSHA256(filePath string) (string, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}



// PackageAppAssets builds and packages release artifacts for CLI and firmware applications.
func PackageAppAssets(bazel BazelRunner, _ FileSystem, workspaceRoot string, app AppMetadata, outputDir string, requestedPlatforms []string) ([]PackagedAsset, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output directory %s: %w", outputDir, err)
	}

	var packaged []PackagedAsset

	switch app.AppType {
	case "cli", "binary":
		platforms := DefaultCLIPlatforms
		if len(requestedPlatforms) > 0 {
			platforms = nil
			for _, rp := range requestedPlatforms {
				p, err := normalizePlatform(rp)
				if err != nil {
					return nil, err
				}
				platforms = append(platforms, p)
			}
		}

		pkg, binName := binaryTargetPkgAndName(app)

		for _, plat := range platforms {
			// --config=ci-images: this build's output is read straight off
			// bazel-bin below, so it needs a real file on disk afterward --
			// see .bazelrc's comment on ci-images and build_app.go's push,
			// which had the same gap (issue found while investigating GH
			// Actions run 33833765164/job/100902056651's push slowness).
			args := []string{"build", "--config=ci-images", "--platforms=" + plat.BazelPlatform, app.BinaryTarget}
			if _, err := bazel.Run(args...); err != nil {
				return nil, fmt.Errorf("bazel build %s for platform %s: %w", app.BinaryTarget, plat.Name, err)
			}

			// Look for built binary in standard bazel-bin paths
			candidatePaths := []string{
				filepath.Join(workspaceRoot, "bazel-bin", pkg, binName+"_", binName),
				filepath.Join(workspaceRoot, "bazel-bin", pkg, binName),
				filepath.Join(workspaceRoot, "bazel-bin", pkg, app.Name+"_", app.Name),
				filepath.Join(workspaceRoot, "bazel-bin", pkg, app.Name),
			}

			var foundPath string
			for _, cp := range candidatePaths {
				if fi, err := os.Stat(cp); err == nil && !fi.IsDir() {
					foundPath = cp
					break
				}
			}

			if foundPath == "" {
				// Fall back to scanning bazel-bin/<pkg>
				pkgBinDir := filepath.Join(workspaceRoot, "bazel-bin", pkg)
				_ = filepath.Walk(pkgBinDir, func(path string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() && (info.Mode()&0111 != 0) && !strings.HasSuffix(path, ".params") && !strings.HasSuffix(path, ".a") {
						if strings.Contains(filepath.Base(path), binName) || strings.Contains(filepath.Base(path), app.Name) {
							foundPath = path
							return filepath.SkipAll
						}
					}
					return nil
				})
			}

			if foundPath == "" {
				return nil, fmt.Errorf("could not find built binary for target %s (platform %s)", app.BinaryTarget, plat.Name)
			}

			// Destination filename formatted as <name>-<os>-<arch>
			destFilename := fmt.Sprintf("%s-%s-%s", app.Name, plat.OS, plat.Arch)
			destPath := filepath.Join(outputDir, destFilename)

			if err := copyFile(foundPath, destPath); err != nil {
				return nil, fmt.Errorf("copy binary to %s: %w", destPath, err)
			}

			hash, size, err := computeFileSHA256(destPath)
			if err != nil {
				return nil, fmt.Errorf("compute sha256 for %s: %w", destPath, err)
			}

			packaged = append(packaged, PackagedAsset{
				Name:   destFilename,
				Path:   destPath,
				SHA256: hash,
				Size:   size,
			})
		}

	case "firmware":
		pkg, targetName := binaryTargetPkgAndName(app)
		fwTarget := firmwareTarget(app)

		// --config=ci-images (chained after esp32): same real-file-on-disk
		// requirement as the platform build above -- the firmware binary is
		// read from bazel-bin below.
		args := []string{"build", "--config=esp32", "--config=ci-images", fwTarget}
		if _, err := bazel.Run(args...); err != nil {
			// Try without _merged_bin fallback
			fwTarget = fmt.Sprintf("//%s:%s_bin", pkg, targetName)
			args = []string{"build", "--config=esp32", "--config=ci-images", fwTarget}
			if _, err2 := bazel.Run(args...); err2 != nil {
				return nil, fmt.Errorf("bazel build %s: %w (fallback failed: %v)", fwTarget, err, err2)
			}
		}

		candidatePaths := []string{
			filepath.Join(workspaceRoot, "bazel-bin", pkg, targetName+"_merged.bin"),
			filepath.Join(workspaceRoot, "bazel-bin", pkg, app.Name+"_merged.bin"),
			filepath.Join(workspaceRoot, "bazel-bin", pkg, targetName+".bin"),
			filepath.Join(workspaceRoot, "bazel-bin", pkg, app.Name+".bin"),
		}

		var foundPath string
		for _, cp := range candidatePaths {
			if fi, err := os.Stat(cp); err == nil && !fi.IsDir() {
				foundPath = cp
				break
			}
		}

		if foundPath == "" {
			return nil, fmt.Errorf("could not find built firmware .bin file for %s in %s", app.FullName(), filepath.Join(workspaceRoot, "bazel-bin", pkg))
		}

		// Save as <name>-esp32-merged.bin or <name>.bin
		destFilename := fmt.Sprintf("%s-esp32-merged.bin", app.Name)
		if strings.HasSuffix(foundPath, "_merged.bin") {
			destFilename = fmt.Sprintf("%s-esp32-merged.bin", app.Name)
		} else {
			destFilename = fmt.Sprintf("%s.bin", app.Name)
		}
		destPath := filepath.Join(outputDir, destFilename)

		if err := copyFile(foundPath, destPath); err != nil {
			return nil, fmt.Errorf("copy firmware bin to %s: %w", destPath, err)
		}

		hash, size, err := computeFileSHA256(destPath)
		if err != nil {
			return nil, fmt.Errorf("compute sha256 for %s: %w", destPath, err)
		}

		packaged = append(packaged, PackagedAsset{
			Name:   destFilename,
			Path:   destPath,
			SHA256: hash,
			Size:   size,
		})

	default:
		return nil, fmt.Errorf("app %s has type %q which does not produce binary/firmware release assets", app.FullName(), app.AppType)
	}

	// Generate checksums.txt and SHA256SUMS
	if err := generateChecksumFiles(outputDir, packaged); err != nil {
		return nil, err
	}

	return packaged, nil
}

// generateChecksumFiles writes checksums.txt and SHA256SUMS in outputDir.
func generateChecksumFiles(outputDir string, assets []PackagedAsset) error {
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })

	var sb strings.Builder
	for _, a := range assets {
		sb.WriteString(fmt.Sprintf("%s  %s\n", a.SHA256, a.Name))
	}
	content := []byte(sb.String())

	for _, fname := range []string{"checksums.txt", "SHA256SUMS"} {
		dest := filepath.Join(outputDir, fname)
		if err := os.WriteFile(dest, content, 0644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
	}
	return nil
}

func newPackageAssetsCmd() *cobra.Command {
	var (
		version   string
		outputDir string
		platforms []string
	)

	cmd := &cobra.Command{
		Use:          "package-assets <app-name>",
		Short:        "Package release assets (multi-arch CLI binaries, firmware .bin, checksums)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			apps, err := resolveApps([]string{args[0]}, allApps)
			if err != nil {
				return err
			}
			app := apps[0]

			if outputDir == "" {
				outputDir = filepath.Join(os.TempDir(), "release-assets", app.FullName())
			}

			assets, err := PackageAppAssets(defaultBazel, defaultFS, workspaceRoot, app, outputDir, platforms)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ Successfully packaged %d assets in %s (version: %s):\n", len(assets), outputDir, version)
			for _, a := range assets {
				fmt.Fprintf(cmd.OutOrStdout(), "  - %s (SHA256: %s, %d bytes)\n", a.Name, a.SHA256, a.Size)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&version, "version", "latest", "Version tag")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Directory to write packaged assets and checksums")
	cmd.Flags().StringSliceVar(&platforms, "platforms", nil, "Target platforms to build for (e.g. linux_amd64,linux_arm64,darwin_arm64)")
	return cmd
}
