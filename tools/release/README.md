# Release Tool - Go Implementation

This directory contains the Go implementation of the release helper tool, which is a rewrite of the Python version in `tools/release_helper/`.

## Structure

```
tools/release/
├── cmd/
│   └── release/          # CLI entry point
│       └── main.go
├── pkg/
│   ├── core/            # Core utilities (workspace, Bazel runner)
│   ├── metadata/        # App metadata operations
│   ├── validation/      # Version validation
│   ├── git/             # Git operations
│   ├── images/          # Image build/push operations (TODO)
│   ├── changes/         # Change detection (TODO)
│   ├── release/         # Release planning (TODO)
│   ├── github/          # GitHub API operations (TODO)
│   └── cli/             # CLI commands
└── BUILD.bazel
```

## Commands Implemented

Currently implemented commands (7 of 20+):
- ✅ `list` - List all apps with release metadata
- ✅ `list-app-versions [app]` - List versions for apps by checking git tags
- ✅ `increment-version <app> <minor|patch>` - Calculate next version
- ✅ `build <app> [--platform=...]` - Build container image
- ✅ `plan --event-type=...` - Plan release and output CI matrix
- ✅ `changes [--base-commit=...]` - Detect changed apps since a commit
- ✅ `release <app> [--version=...]` - Build, tag, and push container image

## Commands TODO

The following commands from the Python version need to be ported:
- `validate-version` - Validate a version string
- `summary` - Generate release summary
- `release-notes` - Generate release notes for a specific app
- `release-notes-all` - Generate release notes for all apps
- `create-github-release` - Create GitHub release
- `create-combined-github-release` - Create combined GitHub release
- `create-combined-github-release-with-notes` - Create combined release with notes
- `list-helm-charts` - List Helm charts
- `helm-chart-info` - Get Helm chart information
- `resolve-chart-app-versions` - Resolve app versions for chart
- `build-helm-chart` - Build Helm chart
- `plan-helm-release` - Plan Helm release
- `release-multiarch` - Build and release multi-architecture images

## Testing

Tests are written using Go's standard testing framework and follow patterns from the Python test suite:

```bash
# Run all tests
bazel test //tools/release/...

# Run specific test
bazel test //tools/release:core_test
bazel test //tools/release:validation_test
bazel test //tools/release:git_test
bazel test //tools/release:metadata_test
bazel test //tools/release:images_test
bazel test //tools/release:changes_test
bazel test //tools/release:release_test
```

## Usage

Build and run the Go version:

```bash
# Build the binary
bazel build //tools/release:release

# Run via Bazel
bazel run //tools/release:release -- list
bazel run //tools/release:release -- list-app-versions
bazel run //tools/release:release -- increment-version hello_python minor
bazel run //tools/release:release -- build hello_python
bazel run //tools/release:release -- plan --event-type=workflow_dispatch --apps=all --version=v1.0.0
bazel run //tools/release:release -- changes --base-commit=HEAD^
bazel run //tools/release:release -- release hello_python --version=v1.0.0 --dry-run
```

## Migration Status

### Completed Modules
- ✅ Core utilities (workspace detection, Bazel runner)
- ✅ Metadata operations (app discovery, metadata parsing)
- ✅ Validation (semantic versioning, version comparison)
- ✅ Git operations (tag management, version parsing, auto-increment)
- ✅ Images module (build, tag, push operations)
- ✅ Changes module (change detection, Bazel query)
- ✅ Release module (planning, CI matrix generation)
- ✅ Basic CLI structure (cobra-based, 7 commands)

### In Progress
- 🚧 GitHub module (release creation, API integration)
- 🚧 Release notes generation
- 🚧 Full CLI command parity with Python version

### Pending
- ⏳ Helm chart operations (13 commands remaining)
- ⏳ Multi-arch image support (command integration)
- ⏳ Summary and validation commands

## Design Notes

### Leveraging Existing Tests

The Go implementation leverages the existing Python test suite by:
1. Following the same test patterns and structure
2. Testing the same edge cases and scenarios
3. Using similar test names for easy cross-reference
4. Maintaining backward compatibility with existing workflows

### Key Differences from Python Version

1. **Type Safety**: Go provides compile-time type checking
2. **Performance**: Go binaries are faster, especially for Bazel operations
3. **Dependencies**: Reduced runtime dependencies (no Python interpreter needed)
4. **Error Handling**: Explicit error handling vs Python exceptions
5. **CLI Framework**: Using Cobra instead of Typer
6. **Testing**: Using Go's testing package instead of pytest

### Maintaining Compatibility

The Go version maintains CLI compatibility with the Python version to ensure:
- Existing CI/CD workflows continue to work
- Documentation remains valid
- Migration can be gradual
- Easy fallback to Python version if needed

## Development

To add new functionality:

1. Implement the module in `pkg/<module>/`
2. Add tests in `pkg/<module>/<module>_test.go`
3. Add CLI commands in `pkg/cli/cli.go`
4. Update this README
5. Run tests: `bazel test //tools/release/...`

Follow the existing patterns in core, metadata, validation, and git modules.

## Dependencies

- github.com/spf13/cobra - CLI framework
- Standard Go library for most operations

Dependencies are managed in `go.mod` and automatically handled by Bazel via Gazelle.
