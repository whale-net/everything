# Adding Go Dependencies to Bazel

This guide explains how to add Go dependencies to the Everything monorepo using Bazel's module system (bzlmod) with **automated dependency tracking**.

## Overview

This repository uses Bazel with the following tools:
- **rules_go** for building Go code
- **Gazelle** for BUILD file generation and dependency management
- **bzlmod** (MODULE.bazel) with automated `go.mod` integration
- **go.mod** for declaring dependencies (automatically synced to MODULE.bazel)

**Key Feature:** Dependencies are automatically resolved from `go.mod`. You no longer need to manually track transitive dependencies in MODULE.bazel!

## Quick Start: The Automated Workflow (Recommended)

This is the complete workflow for adding a new Go dependency. The entire process is automated - just three commands!

### 1. Add Dependency with `go get`

```bash
go get github.com/google/uuid@v1.6.0
```

This adds the dependency to `go.mod` and downloads it with checksums to `go.sum`.

### 2. Update BUILD Files with Gazelle

```bash
bazel run //:gazelle
```

Gazelle scans your Go imports and automatically:
- Creates/updates BUILD.bazel files
- Adds `go_library`, `go_binary`, `go_test` targets
- Wires up dependencies based on imports

### 3. Update MODULE.bazel with `bazel mod tidy`

```bash
bazel mod tidy
```

This command automatically:
- Scans all BUILD files for external dependencies
- Updates the `use_repo()` list in MODULE.bazel
- Only includes dependencies actually used in your code

### 4. Build and Test

```bash
bazel build //path/to/your:target
bazel test //path/to/your:test
```

## Complete Example: Adding UUID Package

Here's a real example of the full workflow:

```bash
# 1. Add dependency
go get github.com/google/uuid@v1.6.0

# 2. Create your Go file
cat > mypackage/generator.go <<EOF
package mypackage

import "github.com/google/uuid"

func GenerateID() string {
    return uuid.New().String()
}
EOF

# 3. Generate BUILD files
bazel run //:gazelle

# 4. Update MODULE.bazel
bazel mod tidy

# 5. Build
bazel build //mypackage
```

That's it! All dependency tracking is automated.

## What Happens Behind the Scenes

### The `go.mod` File

Located at `/go.mod`, this declares your direct dependencies:

```go
module github.com/whale-net/everything

go 1.25

require (
    github.com/google/uuid v1.6.0
    github.com/jackc/pgx/v5 v5.8.0
    // ... other dependencies
)
```

### MODULE.bazel Configuration

The MODULE.bazel file is configured to read from `go.mod`:

```starlark
# Go dependencies - Automated via go_deps.from_file
go_deps = use_extension("@gazelle//:extensions.bzl", "go_deps")

# This enables automatic transitive dependency resolution
go_deps.from_file(go_mod = "//:go.mod")

# This list is automatically managed by 'bazel mod tidy'
use_repo(go_deps, "com_github_google_uuid", ...)
```

**Key Points:**
- `go_deps.from_file()` reads `go.mod` and resolves ALL transitive dependencies automatically
- `use_repo()` is automatically populated by `bazel mod tidy` - don't edit it manually!
- Bazel downloads dependencies into a "hub" repository and makes them available

### How `bazel mod tidy` Works

`bazel mod tidy` is like a garbage collector for dependencies:

1. Scans all your BUILD files
2. Finds which external repositories are actually referenced
3. Updates the `use_repo()` list to include only those dependencies
4. Removes entries for dependencies no longer used

This means you **never need to manually manage** the `use_repo()` list!

## Advanced: Adding Multiple Dependencies

```bash
# Add multiple dependencies at once
go get \
  github.com/gorilla/mux@v1.8.0 \
  github.com/rs/cors@v1.10.0 \
  github.com/sirupsen/logrus@v1.9.3

# Update BUILD files and MODULE.bazel
bazel run //:gazelle
bazel mod tidy

# Build
bazel build //...
```

## Upgrading Dependencies

To upgrade a dependency to a newer version:

```bash
# Upgrade to latest version
go get github.com/google/uuid@latest

# Or upgrade to specific version
go get github.com/google/uuid@v1.7.0

# Update Bazel configuration
bazel run //:gazelle
bazel mod tidy

# Test
bazel test //...
```

## Removing Dependencies

To remove an unused dependency:

```bash
# Remove from go.mod
go mod tidy

# Update Bazel
bazel run //:gazelle
bazel mod tidy
```

The `bazel mod tidy` command will automatically remove the dependency from `use_repo()`.

## Understanding Dependency Names

Bazel converts Go import paths to repository names using these rules:

| Go Import Path | Bazel Repository Name |
|----------------|----------------------|
| `github.com/user/pkg` | `com_github_user_pkg` |
| `golang.org/x/crypto` | `org_golang_x_crypto` |
| `google.golang.org/grpc` | `org_golang_google_grpc` |
| `gopkg.in/yaml.v3` | `in_gopkg_yaml_v3` |
| `github.com/jackc/pgx/v5` | `com_github_jackc_pgx_v5` |

**You don't need to remember these!** `bazel mod tidy` handles this automatically.

## Using Dependencies in BUILD Files

When Gazelle generates BUILD files, dependencies are automatically added:

```starlark
load("@rules_go//go:def.bzl", "go_library")

go_library(
    name = "mylib",
    srcs = ["mylib.go"],
    importpath = "github.com/whale-net/everything/mylib",
    visibility = ["//visibility:public"],
    deps = [
        "@com_github_google_uuid//:uuid",  # External dependency
        "//libs/go/rmq",                    # Internal dependency
    ],
)
```

## Troubleshooting

### Issue: Dependencies not resolving

**Problem:** Build fails with "no such package" errors.

**Solution:**
```bash
# Ensure go.mod and go.sum are up to date
go mod download

# Regenerate everything
bazel run //:gazelle
bazel mod tidy

# Clear Bazel cache if needed
bazel clean --expunge
```

### Issue: Version conflicts

**Problem:** Multiple versions of the same package.

**Solution:** Go mod uses minimum version selection. Update `go.mod` to use the version you need:

```bash
go get github.com/package/name@v2.0.0
bazel mod tidy
```

### Issue: Gazelle not updating BUILD files

**Problem:** BUILD files don't reflect Go code changes.

**Solution:**
```bash
# Run Gazelle in fix mode
bazel run //:gazelle -- -mode=fix

# Update specific directory
bazel run //:gazelle -- path/to/dir
```

### Issue: `bazel mod tidy` not working

**Problem:** `use_repo()` not updating automatically.

**Solution:**
1. Ensure dependencies are actually used in BUILD files
2. Check that `go_deps.from_file(go_mod = "//:go.mod")` is in MODULE.bazel
3. Try running with verbose output:
   ```bash
   bazel mod tidy --announce_rc
   ```

### Issue: Missing checksums in go.sum

**Problem:** Bazel complains about missing checksums.

**Solution:**
```bash
# Download all modules to populate go.sum
go mod download all

# Or download specific module
go mod download github.com/package/name
```

## Gazelle Configuration

The root `BUILD.bazel` contains Gazelle configuration:

```starlark
load("@gazelle//:def.bzl", "gazelle")

# CRITICAL: This prefix must match the module name in go.mod
# gazelle:prefix github.com/whale-net/everything
# gazelle:exclude generated
gazelle(name = "gazelle")
```

**Directives:**
- `gazelle:prefix` - Sets the import path prefix for internal packages
- `gazelle:exclude` - Excludes directories from Gazelle processing

## Comparison: Old vs New Workflow

### Old Manual Workflow ❌

```bash
# 1. Find package and version
# 2. Get checksum from sum.golang.org
# 3. Manually add go_deps.module() to MODULE.bazel
# 4. Manually add to use_repo()
# 5. Manually track all transitive dependencies
# 6. Repeat for every transitive dependency
```

### New Automated Workflow ✅

```bash
go get package@version
bazel run //:gazelle
bazel mod tidy
```

**Benefits:**
- ⚡ 90% faster - three commands vs dozens
- 🤖 Fully automated transitive dependency tracking
- 🔒 Reliable - uses Go's official dependency resolution
- 🧹 Automatic cleanup of unused dependencies
- ✨ No manual MODULE.bazel editing required

## Best Practices

1. **Always use `go get`** to add dependencies (not manual `go.mod` editing)
2. **Run `bazel mod tidy` after Gazelle** to keep MODULE.bazel in sync
3. **Pin to specific versions** in `go.mod` for reproducibility
4. **Run `go mod tidy` periodically** to remove unused dependencies
5. **Commit go.mod and go.sum** to version control
6. **Don't manually edit** the `use_repo()` list in MODULE.bazel
7. **Run tests after updating** dependencies to ensure compatibility

## Quick Reference

### Common Commands

```bash
# Add new dependency
go get github.com/package/name@version

# Update BUILD files
bazel run //:gazelle

# Update MODULE.bazel
bazel mod tidy

# See what would change (dry-run)
bazel run //:gazelle -- -mode=print-only

# Fix all BUILD files
bazel run //:gazelle -- -mode=fix

# Update specific directory
bazel run //:gazelle -- path/to/dir

# Check for unused dependencies
go mod tidy

# Download all dependencies
go mod download all
```

### The Three-Command Workflow

Every time you add a Go dependency or change imports:

```bash
go get <package>@<version>  # Add dependency
bazel run //:gazelle         # Update BUILD files
bazel mod tidy               # Update MODULE.bazel
```

## Integration with CI/CD

Add these checks to CI to ensure dependencies stay consistent:

```yaml
# .github/workflows/ci.yml
- name: Check dependencies
  run: |
    # Check go.mod is tidy
    go mod tidy
    git diff --exit-code go.mod go.sum

    # Check BUILD files are up to date
    bazel run //:gazelle
    git diff --exit-code

    # Check MODULE.bazel is up to date
    bazel mod tidy
    git diff --exit-code MODULE.bazel
```

## Real-World Examples

### Example 1: Adding RabbitMQ Client

```bash
# 1. Add dependency
go get github.com/rabbitmq/amqp091-go@v1.10.0

# 2. Write your code
cat > libs/go/rmq/connection.go <<'EOF'
package rmq

import "github.com/rabbitmq/amqp091-go"

func Connect(url string) (*amqp091.Connection, error) {
    return amqp091.Dial(url)
}
EOF

# 3. Generate BUILD and update MODULE.bazel
bazel run //:gazelle
bazel mod tidy

# 4. Build
bazel build //libs/go/rmq
```

### Example 2: Adding PostgreSQL with Transitive Dependencies

```bash
# Just add the main dependency - transitive deps are automatic!
go get github.com/jackc/pgx/v5@v5.8.0

# Update Bazel
bazel run //:gazelle
bazel mod tidy

# The transitive dependencies (pgpassfile, pgservicefile, etc.)
# are automatically resolved and added by go_deps.from_file()
```

### Example 3: Adding gRPC Stack

```bash
# Add all gRPC dependencies
go get google.golang.org/grpc@v1.78.0
go get google.golang.org/protobuf@v1.36.11

# Update Bazel
bazel run //:gazelle
bazel mod tidy

# Build proto files
bazel build //your/proto:target
```

## Related Documentation

- [Bazel rules_go documentation](https://github.com/bazelbuild/rules_go)
- [Gazelle documentation](https://github.com/bazelbuild/bazel-gazelle)
- [Go Modules Reference](https://go.dev/ref/mod)
- [Bazel Bzlmod documentation](https://bazel.build/external/module)

## Known Issues

### Docker Library

The `github.com/docker/docker` library has complex transitive dependencies that may not be fully resolved by the automated workflow. If you encounter build errors related to Docker client dependencies:

1. The core Docker wrapper (`libs/go/docker`) works for basic operations
2. Advanced Docker features may require additional manual dependency management
3. Consider using a Docker client library alternative if you need advanced features

This is a known limitation with the Docker SDK's dependency graph and is being tracked.

## Migration Note

If you see old documentation mentioning manual `go_deps.module()` entries, that workflow is deprecated. The new automated workflow with `go.mod` is much simpler and more reliable.
