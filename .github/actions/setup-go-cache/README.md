# Setup Go Module Cache Action

A composite GitHub Action that sets up Go with optimized module caching. This action handles the common issue where `go.sum` files don't exist when using only internal Go packages, which causes cache restoration failures.

## Features

- ✅ Sets up Go with specified version
- ✅ Configures Go module cache (`~/go/pkg/mod`) and build cache (`~/.cache/go-build`)
- ✅ Handles missing `go.sum` files gracefully (prevents "Dependencies file is not found" errors)
- ✅ Uses optimized cache key strategy with separate `hashFiles()` calls
- ✅ Provides cache hit information for debugging
- ✅ Supports custom cache key suffixes for advanced use cases

## Usage

### Basic Usage

```yaml
- name: Setup Go with caching
  uses: ./.github/actions/setup-go-cache
  with:
    go-version: '1.25'
```

### Advanced Usage

```yaml
- name: Setup Go with caching
  uses: ./.github/actions/setup-go-cache
  with:
    go-version: '1.21'
    cache-dependency-path: 'path/to/go.mod'
    cache-key-suffix: '-custom-suffix'
```

## Inputs

| Input | Description | Required | Default |
|-------|-------------|----------|---------|
| `go-version` | Go version to install | Yes | `1.25` |
| `cache-dependency-path` | Path to dependency file (go.mod) | No | `go.mod` |
| `cache-key-suffix` | Additional suffix for cache key customization | No | `''` |

## Outputs

| Output | Description |
|--------|-------------|
| `cache-hit` | Boolean indicating if cache was hit |
| `go-version` | The installed Go version |

## How It Works

The action uses a cache key strategy that handles missing `go.sum` files:

```yaml
key: ${{ runner.os }}-go-${{ hashFiles('go.mod') }}-${{ hashFiles('go.sum') }}
```

This approach:
- Uses separate `hashFiles()` calls instead of passing multiple files to one call
- When `go.sum` is missing, `hashFiles('go.sum')` returns an empty string
- Creates a valid cache key based on `go.mod` content
- Future-proof: will properly hash both files if external dependencies are added

## Migration from Manual Setup

### Before (duplicated across multiple jobs)

```yaml
- name: Setup Go
  uses: actions/setup-go@v5
  with:
    go-version: '1.25'
    
- name: Setup Go module cache
  uses: actions/cache@v4
  with:
    path: |
      ~/go/pkg/mod
      ~/.cache/go-build
    key: ${{ runner.os }}-go-${{ hashFiles('go.mod') }}-${{ hashFiles('go.sum') }}
    restore-keys: |
      ${{ runner.os }}-go-
```

### After (reusable action)

```yaml
- name: Setup Go with caching
  uses: ./.github/actions/setup-go-cache
  with:
    go-version: '1.25'
```

## Benefits

1. **DRY Principle**: Eliminates code duplication across workflows
2. **Consistency**: Ensures all jobs use the same caching strategy
3. **Maintainability**: Single place to update Go caching logic
4. **Error Prevention**: Handles missing `go.sum` files automatically
5. **Performance**: Optimized cache keys for better hit rates