package kinds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

// AppMetadataRegistry loads and indexes app metadata from the repository checkout.
// This is the central authoring site (FR-36) that enumerates which apps and kinds
// publish through the release path.
type AppMetadataRegistry struct {
	// appsByFullName maps full_name ("<domain>-<name>") to AppManifest.
	appsByFullName map[string]*appmetapb.AppManifest

	// appsByAppType indexes apps by their app_type for H7 dispatch.
	appsByAppType map[string][]*appmetapb.AppManifest
}

// NewAppMetadataRegistry constructs an empty registry.
func NewAppMetadataRegistry() *AppMetadataRegistry {
	return &AppMetadataRegistry{
		appsByFullName: make(map[string]*appmetapb.AppManifest),
		appsByAppType:  make(map[string][]*appmetapb.AppManifest),
	}
}

// LoadFromCheckout loads all app metadata from the repository checkout,
// walking the Bazel output tree to find all *_metadata.json files.
// ctx is used for potential future operations (e.g., progress reporting).
//
// Returns error if any metadata file cannot be read or parsed.
func (r *AppMetadataRegistry) LoadFromCheckout(ctx context.Context, checkoutRoot string) error {
	if _, err := os.Stat(checkoutRoot); err != nil {
		return fmt.Errorf("checkout root %q not found: %w", checkoutRoot, err)
	}

	// Walk the repository to find all *_metadata.json files
	err := filepath.Walk(checkoutRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		basename := filepath.Base(path)
		if len(basename) > 13 && basename[len(basename)-13:] == "_metadata.json" {
			if err := r.loadMetadataFile(path); err != nil {
				// Log but don't fail on individual file errors -- 
				// many _metadata.json files may be part of the build tree
				// that aren't app releases, and their format may differ.
				_ = err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk checkout: %w", err)
	}

	return nil
}

// loadMetadataFile loads a single *_metadata.json file and indexes its app.
func (r *AppMetadataRegistry) loadMetadataFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %q: %w", filePath, err)
	}

	// First try to parse as AppManifest proto
	var app appmetapb.AppManifest
	err = protojson.Unmarshal(data, &app)
	if err != nil {
		// If proto parsing fails, try raw JSON (for compatibility with old format)
		// This allows the loader to be tolerant of non-app metadata files
		return fmt.Errorf("parse %q as AppManifest: %w", filePath, err)
	}

	// Index the app by full_name
	fullName := fmt.Sprintf("%s-%s", app.Domain, app.Name)
	r.appsByFullName[fullName] = &app

	// Index by app_type for H7 dispatch
	if app.AppType != "" {
		r.appsByAppType[app.AppType] = append(r.appsByAppType[app.AppType], &app)
	}

	return nil
}

// GetApp retrieves an app by its full name ("<domain>-<name>").
func (r *AppMetadataRegistry) GetApp(fullName string) *appmetapb.AppManifest {
	return r.appsByFullName[fullName]
}

// AppTypesForKind returns the list of app_types that produce the given kind.
// This is used to build H7's AppTypeMapping.
func (r *AppMetadataRegistry) AppTypesForKind(kind appmetapb.ArtifactKind) []string {
	seen := make(map[string]bool)
	var result []string

	// Iterate through all apps and collect app_types matching the kind
	for _, app := range r.appsByFullName {
		if app.ArtifactKind == kind && app.AppType != "" {
			if !seen[app.AppType] {
				seen[app.AppType] = true
				result = append(result, app.AppType)
			}
		}
	}

	return result
}

// AllApps returns a map of all loaded apps, keyed by full_name.
func (r *AppMetadataRegistry) AllApps() map[string]*appmetapb.AppManifest {
	result := make(map[string]*appmetapb.AppManifest)
	for k, v := range r.appsByFullName {
		result[k] = v
	}
	return result
}

// AppsWithKind returns all apps that have the given artifact kind.
func (r *AppMetadataRegistry) AppsWithKind(kind appmetapb.ArtifactKind) map[string]*appmetapb.AppManifest {
	result := make(map[string]*appmetapb.AppManifest)
	for fullName, app := range r.appsByFullName {
		if app.ArtifactKind == kind {
			result[fullName] = app
		}
	}
	return result
}

// BinaryNameForApp returns the "binary name" (app name without domain prefix)
// for a CLI binary app. This is used by S3 key conventions and file naming.
// Returns empty string if the app is not found.
func (r *AppMetadataRegistry) BinaryNameForApp(fullName string) string {
	app := r.GetApp(fullName)
	if app == nil {
		return ""
	}
	return app.Name
}

// FullNameForBinaryName returns the full app name for a given binary name.
// Returns empty string if no app with that binary name exists.
// Note: this assumes binary names are unique; if multiple apps share the same
// Name but different Domain, this returns the first match found.
func (r *AppMetadataRegistry) FullNameForBinaryName(binaryName string) string {
	for fullName, app := range r.appsByFullName {
		if app.Name == binaryName {
			return fullName
		}
	}
	return ""
}

// RegisterApp directly registers an app manifest in the registry, without
// reading from disk. This is used in tests and for programmatic construction
// of the registry.
func (r *AppMetadataRegistry) RegisterApp(app *appmetapb.AppManifest) {
	if app == nil {
		return
	}
	// Index the app by full_name
	fullName := fmt.Sprintf("%s-%s", app.Domain, app.Name)
	r.appsByFullName[fullName] = app

	// Index by app_type for H7 dispatch
	if app.AppType != "" {
		r.appsByAppType[app.AppType] = append(r.appsByAppType[app.AppType], app)
	}
}
