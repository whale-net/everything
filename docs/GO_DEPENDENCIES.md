# Adding Go Dependencies to Bazel

This guide explains how to add Go dependencies to the Everything monorepo using Bazel's module system (bzlmod) with Gazelle.

## Overview

This repository uses Bazel with the following tools:
- **rules_go** for building Go code
- **Gazelle** for dependency management (`go_deps` extension) and BUILD file generation
- **bzlmod** (MODULE.bazel) instead of WORKSPACE

All Go dependencies are declared in `MODULE.bazel` and do not require a `go.mod` file.

### Should I Use Gazelle?

**Yes! Gazelle is highly recommended** and already integrated in this repository:

1. **Dependency Management** (✅ Already using): The `go_deps` extension automatically manages external Go dependencies
2. **BUILD File Generation** (⚡ Strongly recommended): Auto-generates and updates BUILD.bazel files, eliminating manual work

**TL;DR**: 
- Add dependencies to MODULE.bazel (manual)
- Run `bazel run //:gazelle` to auto-generate BUILD files (automated)
- Build and test as usual

This guide covers both the recommended Gazelle workflow and the manual approach for those who need fine-grained control.

## Quick Start: Using Gazelle (Recommended)

For most cases, use Gazelle to auto-generate BUILD files after adding dependencies to MODULE.bazel.

### 1. Add Dependency to MODULE.bazel

```starlark
go_deps.module(
    path = "github.com/google/uuid",
    sum = "h1:sUQHxdj3Y+KzOGaMvjmwBNYG2b3CtOSJ/qhJUiBhCcM=",
    version = "v1.6.0",
)

use_repo(go_deps, "com_github_google_uuid")
```

### 2. Enable Gazelle (One-Time Setup)

If not already set up, add Gazelle rules to the root `BUILD.bazel`:

```starlark
load("@gazelle//:def.bzl", "gazelle")

# Configure the Go import path prefix for this repository
# gazelle:prefix github.com/whale-net/everything

# Main gazelle target - generates/updates BUILD files
gazelle(name = "gazelle")

# Optional: Target to update go_deps from go.mod (if using go.mod)
# gazelle(
#     name = "gazelle-update-repos",
#     args = [
#         "-from_file=go.mod",
#         "-to_macro=deps.bzl%go_dependencies",
#         "-prune",
#     ],
#     command = "update-repos",
# )
```

**Note**: The `gazelle:prefix` directive tells Gazelle the import path prefix for your internal packages. This should match your module path.

### 3. Run Gazelle to Generate BUILD Files

```bash
# Update BUILD files for the entire repository
bazel run //:gazelle

# Update BUILD files for a specific directory
bazel run //:gazelle -- path/to/directory
```

Gazelle will:
- Create BUILD.bazel files where needed
- Add/update `go_library`, `go_binary`, `go_test` targets
- Add dependencies automatically based on imports

### 4. Build and Test

```bash
bazel build //path/to/your:target
bazel test //path/to/your:test
```

That's it! Gazelle handles the BUILD file creation and dependency wiring.

### Complete Example: Adding UUID Package with Gazelle

```bash
# 1. Get the checksum
curl "https://sum.golang.org/lookup/github.com/google/uuid@v1.6.0"

# 2. Edit MODULE.bazel to add dependency
# Add to go_deps section:
#   go_deps.module(
#       path = "github.com/google/uuid",
#       sum = "h1:sUQHxdj3Y+KzOGaMvjmwBNYG2b3CtOSJ/qhJUiBhCcM=",
#       version = "v1.6.0",
#   )
# Add to use_repo:
#   use_repo(go_deps, "com_github_google_uuid")

# 3. Create your Go file using the package
cat > mypackage/generator.go <<EOF
package mypackage

import "github.com/google/uuid"

func GenerateID() string {
    return uuid.New().String()
}
EOF

# 4. Run Gazelle to generate BUILD.bazel
bazel run //:gazelle

# 5. Gazelle automatically creates mypackage/BUILD.bazel with:
# go_library(
#     name = "mypackage",
#     srcs = ["generator.go"],
#     importpath = "github.com/whale-net/everything/mypackage",
#     visibility = ["//visibility:public"],
#     deps = ["@com_github_google_uuid//:go_default_library"],
# )

# 6. Build and test
bazel build //mypackage
```

---

## Manual Step-by-Step Guide

If you prefer manual control or need to understand the details, follow this guide.

### 1. Find the Dependency Information

First, identify the Go module path and version you need. You can find this on:
- The package's GitHub repository (e.g., `github.com/user/package`)
- The Go package registry at https://pkg.go.dev/

Example: Adding `github.com/google/uuid` version `v1.6.0`

### 2. Get the Checksum

You need the module checksum. Get it using:

```bash
# Method 1: Use go mod download (requires temporary go.mod)
mkdir /tmp/gomod && cd /tmp/gomod
go mod init temp
go get github.com/google/uuid@v1.6.0
go mod download -json github.com/google/uuid@v1.6.0 | grep Sum

# Method 2: Check the Go checksum database
curl "https://sum.golang.org/lookup/github.com/google/uuid@v1.6.0"
```

The checksum looks like: `h1:sUQHxdj3Y+KzOGaMvjmwBNYG2b3CtOSJ/qhJUiBhCcM=`

### 3. Add to MODULE.bazel

Open `MODULE.bazel` and add your dependency in the `go_deps` section:

```starlark
# Go dependencies
go_deps = use_extension("@gazelle//:extensions.bzl", "go_deps")

# ... existing dependencies ...

# Add your new dependency
go_deps.module(
    path = "github.com/google/uuid",
    sum = "h1:sUQHxdj3Y+KzOGaMvjmwBNYG2b3CtOSJ/qhJUiBhCcM=",
    version = "v1.6.0",
)
```

**Key points:**
- `path`: The Go module import path
- `version`: The semantic version (must start with `v`)
- `sum`: The checksum from step 2

### 4. Add to use_repo() Call

Find the `use_repo()` call at the end of the `go_deps` section and add your dependency using the Bazel-ified name:

```starlark
use_repo(
    go_deps,
    # ... existing repos ...
    "com_github_google_uuid",  # Add this line
)
```

**Naming convention:**
- Replace `/` with `_`
- Replace `.` with `_`
- Replace `-` with `_`
- Prefix with appropriate namespace:
  - `com_github_` for github.com packages
  - `org_golang_` for golang.org packages
  - `in_gopkg_` for gopkg.in packages

**Examples:**
```
github.com/google/uuid           → com_github_google_uuid
golang.org/x/crypto              → org_golang_x_crypto
gopkg.in/yaml.v3                 → in_gopkg_yaml_v3
github.com/jackc/pgx/v5          → com_github_jackc_pgx_v5
google.golang.org/grpc           → org_golang_google_grpc
```

### 5. Use in BUILD.bazel

Reference the dependency in your Go code's `BUILD.bazel` file:

```starlark
load("@rules_go//go:def.bzl", "go_library")

go_library(
    name = "mylib",
    srcs = ["mylib.go"],
    importpath = "github.com/whale-net/everything/mylib",
    visibility = ["//visibility:public"],
    deps = [
        "@com_github_google_uuid//:go_default_library",
    ],
)
```

**Standard library references:**
```starlark
deps = [
    "@com_github_google_uuid//:go_default_library",  # External package
    "//libs/go/rmq",                                  # Internal package
]
```

### 6. Verify and Build

Test that everything works:

```bash
# Build your target
bazel build //path/to/your:target

# Run tests
bazel test //path/to/your:target_test

# Query dependencies to verify
bazel query "deps(//path/to/your:target)"
```

## Adding Transitive Dependencies

If your main dependency has its own dependencies that aren't automatically resolved, you'll need to add them manually.

### Finding Transitive Dependencies

```bash
# Method 1: Check the package's go.mod
curl -s https://raw.githubusercontent.com/user/package/main/go.mod

# Method 2: Build and check error messages
bazel build //your/target
# Error messages will indicate missing dependencies
```

### Adding Transitive Dependencies

Add each transitive dependency following the same steps as above:

```starlark
# Main dependency
go_deps.module(
    path = "github.com/main/package",
    sum = "h1:...",
    version = "v1.0.0",
)

# Transitive dependencies
go_deps.module(
    path = "github.com/dependency/one",
    sum = "h1:...",
    version = "v2.1.0",
)
go_deps.module(
    path = "github.com/dependency/two",
    sum = "h1:...",
    version = "v3.2.1",
)

# Add all to use_repo
use_repo(
    go_deps,
    "com_github_main_package",
    "com_github_dependency_one",
    "com_github_dependency_two",
)
```

## Real-World Examples

### Example 1: RabbitMQ Client

```starlark
go_deps.module(
    path = "github.com/rabbitmq/amqp091-go",
    sum = "h1:STpn5XsHlHGcecLmMFCtg7mqq0RnD+zFr4uzukfVhBw=",
    version = "v1.10.0",
)

use_repo(go_deps, "com_github_rabbitmq_amqp091_go")
```

Usage in BUILD.bazel:

```starlark
go_library(
    name = "rmq",
    srcs = ["connection.go"],
    deps = [
        "@com_github_rabbitmq_amqp091_go//:go_default_library",
    ],
)
```

### Example 2: PostgreSQL Client with Transitive Dependencies

```starlark
# Main dependency
go_deps.module(
    path = "github.com/jackc/pgx/v5",
    sum = "h1:mLoDLV6sonKlvjIEsV56SkWNCnuNv531l94GaIzO+XI=",
    version = "v5.7.2",
)

# Transitive dependencies
go_deps.module(
    path = "github.com/jackc/pgpassfile",
    sum = "h1:/6Hmqy13Ss2zCq62VdNG8tM1wchn8zjSGOBJ6icpsIM=",
    version = "v1.0.0",
)
go_deps.module(
    path = "github.com/jackc/pgservicefile",
    sum = "h1:iCEnooe7UlwOQYpKFhBabPMi4aNAfoODPEFNiAnClxo=",
    version = "v0.0.0-20240606120523-5a60cdf6a761",
)
go_deps.module(
    path = "github.com/jackc/puddle/v2",
    sum = "h1:PR8nw+E/1w0GLuRFSmiioY6UooMp6KJv0/61nB7icHo=",
    version = "v2.2.2",
)

use_repo(
    go_deps,
    "com_github_jackc_pgx_v5",
    "com_github_jackc_pgpassfile",
    "com_github_jackc_pgservicefile",
    "com_github_jackc_puddle_v2",
)
```

### Example 3: gRPC and Protobuf

```starlark
go_deps.module(
    path = "google.golang.org/grpc",
    sum = "h1:pWFv03aZoHzlRKHWicjsZytKAiYCtNS0dHbXnIdq7jQ=",
    version = "v1.70.0",
)
go_deps.module(
    path = "google.golang.org/protobuf",
    sum = "h1:tPhr+woSbjfYvY6/GPufUoYizxw1cF/yFoxJ2fmpwlM=",
    version = "v1.36.1",
)
go_deps.module(
    path = "google.golang.org/genproto/googleapis/rpc",
    sum = "h1:GXlROi7mbvMNy0MnP83EeJCfCqAZ13Vrf/CEFiNvw34=",
    version = "v0.0.0-20241216192217-9240e9c98484",
)

use_repo(
    go_deps,
    "org_golang_google_grpc",
    "org_golang_google_protobuf",
    "org_golang_google_genproto_googleapis_rpc",
)
```

## Troubleshooting

### Issue: "no such package" error

**Problem:** Bazel can't find the dependency.

**Solution:** Check that:
1. The dependency is in `go_deps.module()` with correct path/version/sum
2. The dependency is in the `use_repo()` call
3. The Bazel name matches the conversion rules

### Issue: Version conflicts

**Problem:** Multiple versions of the same package.

**Solution:** Bazel uses minimum version selection. Declare only one version per package in MODULE.bazel. Choose the highest version needed by all consumers.

### Issue: Wrong checksum

**Problem:** `sum mismatch` error when building.

**Solution:**
1. Verify you copied the full checksum including `h1:` prefix
2. Re-download the checksum from the Go checksum database
3. Ensure the version matches exactly (including `v` prefix)

### Issue: Missing transitive dependencies

**Problem:** Build fails with "package not found" for a sub-dependency.

**Solution:** Add the transitive dependency explicitly following the same steps. Use the error message to identify which package is missing.

### Issue: Gazelle not finding the target

**Problem:** `bazel run //:gazelle` fails with "no such target."

**Solution:** 
1. Check that the Gazelle target is defined in root BUILD.bazel
2. Verify the load statement: `load("@gazelle//:def.bzl", "gazelle")`
3. Ensure Gazelle is declared in MODULE.bazel: `bazel_dep(name = "gazelle", version = "0.39.1")`

### Issue: Gazelle generates incorrect dependencies

**Problem:** Gazelle adds wrong dependencies or misses some.

**Solution:**
1. Ensure the `gazelle:prefix` directive matches your module path
2. Check that dependencies are in `use_repo(go_deps, ...)`
3. Manually add `# gazelle:resolve` directives if needed:
   ```starlark
   # gazelle:resolve go github.com/example/pkg @com_github_example_pkg//:go_default_library
   ```

### Issue: Gazelle overwrites custom BUILD file changes

**Problem:** Manual BUILD.bazel edits get overwritten.

**Solution:** Use `# keep` comments to preserve specific lines:
```starlark
go_library(
    name = "mylib",
    srcs = ["mylib.go"],
    visibility = ["//visibility:public"],  # keep
    deps = [
        "@com_github_custom//:special",  # keep
    ],
)
```

## Gazelle vs Manual: When to Use Each

### Use Gazelle When:
- ✅ Creating new Go packages from scratch
- ✅ Adding many new files to existing packages
- ✅ Refactoring code across multiple packages
- ✅ You want automated dependency wiring
- ✅ Working in a standard Go project structure

### Use Manual When:
- ✅ Fine-tuning specific build targets
- ✅ Adding custom build configurations
- ✅ Working with non-standard build patterns
- ✅ You need precise control over visibility or dependencies

**Best practice**: Use Gazelle by default, then manually adjust BUILD files if needed.

## Advanced Gazelle Usage

### Gazelle Directives

Control Gazelle behavior with directives in BUILD.bazel or Go files:

```starlark
# In BUILD.bazel

# Set the import path prefix
# gazelle:prefix github.com/whale-net/everything

# Exclude directories from Gazelle
# gazelle:exclude vendor

# Set the Go naming convention
# gazelle:go_naming_convention go_default_library
```

```go
// In Go source files

// Exclude this file from Gazelle
// gazelle:ignore

// Map import to specific Bazel target
// gazelle:map_kind go_library go_library @io_bazel_rules_go//go:def.bzl
```

### Update Specific Paths

```bash
# Update only a specific directory
bazel run //:gazelle -- manman/host/session

# Update and fix imports
bazel run //:gazelle -- -mode=fix

# Update and print what would change (dry-run)
bazel run //:gazelle -- -mode=print-only
```

### Gazelle with External Dependencies

When you add a new import in your Go code:

1. Add the dependency to MODULE.bazel (see manual guide)
2. Run Gazelle to update BUILD files
3. Gazelle will automatically add the dependency to the appropriate targets

Example workflow:
```bash
# 1. Add dependency to MODULE.bazel
# 2. Import in your Go code
# 3. Run Gazelle
bazel run //:gazelle

# 4. Build
bazel build //your/package
```

## Best Practices

1. **Use Gazelle by default**: Let Gazelle generate BUILD files, then customize if needed
2. **Keep dependencies organized**: Group related dependencies together in MODULE.bazel with comments
3. **Use specific versions**: Always pin to specific versions (e.g., `v1.2.3`), not ranges
4. **Document why**: Add comments explaining why dependencies are needed
5. **Minimize dependencies**: Only add what you actually use
6. **Keep alphabetical**: Sort `use_repo()` entries alphabetically for easier maintenance
7. **Run Gazelle after changes**: Make it a habit to run `bazel run //:gazelle` after modifying Go imports
8. **Test thoroughly**: Run tests after adding dependencies to ensure everything works

## Quick Reference

### Common Gazelle Commands

```bash
# Generate/update all BUILD files
bazel run //:gazelle

# Update specific directory
bazel run //:gazelle -- path/to/dir

# Fix mode (more aggressive updates)
bazel run //:gazelle -- -mode=fix

# See what would change without applying
bazel run //:gazelle -- -mode=print-only

# Update and exclude a directory
bazel run //:gazelle -- -exclude=vendor
```

### Conversion Table

| Go Import Path | Bazel Repository Name |
|----------------|----------------------|
| `github.com/user/pkg` | `com_github_user_pkg` |
| `golang.org/x/crypto` | `org_golang_x_crypto` |
| `google.golang.org/grpc` | `org_golang_google_grpc` |
| `gopkg.in/yaml.v3` | `in_gopkg_yaml_v3` |
| `github.com/pkg/v2` | `com_github_pkg_v2` |

### MODULE.bazel Template

```starlark
# Add to go_deps section
go_deps.module(
    path = "github.com/YOUR/PACKAGE",
    sum = "h1:CHECKSUM_HERE",
    version = "vX.Y.Z",
)

# Add to use_repo section
use_repo(
    go_deps,
    "com_github_your_package",
)
```

### BUILD.bazel Template

```starlark
load("@rules_go//go:def.bzl", "go_library")

go_library(
    name = "mylib",
    srcs = ["mylib.go"],
    importpath = "github.com/whale-net/everything/mylib",
    deps = [
        "@com_github_your_package//:go_default_library",
    ],
)
```

## Workflow Comparison

### Gazelle Workflow (Recommended)
```
1. Add dependency to MODULE.bazel
2. Import in Go code
3. Run: bazel run //:gazelle
4. Build: bazel build //path/to:target
```

**Pros**: Fast, automatic, less error-prone
**Cons**: Less control over BUILD file structure

### Manual Workflow
```
1. Add dependency to MODULE.bazel
2. Import in Go code
3. Manually write BUILD.bazel with go_library/go_binary
4. Manually add deps = ["@com_github_..."]
5. Build: bazel build //path/to:target
```

**Pros**: Full control, educational
**Cons**: More work, error-prone for large projects

## Integration with CI/CD

Add Gazelle checks to CI to ensure BUILD files stay up to date:

```yaml
# .github/workflows/ci.yml
- name: Check Gazelle
  run: |
    bazel run //:gazelle
    git diff --exit-code
```

This ensures developers run Gazelle before committing.

## Related Documentation

- [Bazel rules_go documentation](https://github.com/bazelbuild/rules_go)
- [Gazelle documentation](https://github.com/bazelbuild/bazel-gazelle)
- [Go Modules Reference](https://go.dev/ref/mod)
- [Gazelle Directives Reference](https://github.com/bazelbuild/bazel-gazelle#directives)
