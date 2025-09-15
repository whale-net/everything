# Intelligent Testing Strategy

This document describes the intelligent testing strategy implemented for the Everything monorepo.

## Overview

The testing strategy automatically determines what to test based on the context:

- **PR Context**: Tests only apps that have changed since the base commit
- **Main Branch Context**: Tests only apps that have changed since the previous commit
- **Release Context**: Tests only the specific app being released
- **Local Context**: Tests all apps (for development)

## Usage

### Local Development

```bash
# Plan what would be tested
bazel run //tools:test -- plan

# Run tests based on context
bazel run //tools:test -- run

# Override context
bazel run //tools:test -- run --context=pr
```

### CI/CD Integration

The testing strategy is automatically used in GitHub Actions:

- **CI Workflow**: Uses intelligent testing based on PR/main context
- **Release Workflow**: Tests only the app being released

### Manual Testing

```bash
# Test specific app for release
bazel run //tools:test -- run --context=release --release-app=hello_python

# Detect changes since a commit
bazel run //tools:test -- changes --since=abc123

# List all testable apps
bazel run //tools:test -- list
```

## Architecture

### Components

1. **Test Helper** (`tools/test_helper.py`): Main testing orchestration tool
2. **Release Helper Integration**: Reuses app detection and metadata from release helper
3. **Context Detection**: Automatically determines testing context from environment
4. **Change Detection**: Uses git to detect changed files and apps

### Testing Contexts

| Context | Trigger | Behavior |
|---------|---------|----------|
| `pr` | GitHub PR event | Test changed apps since base commit |
| `main` | Push to main branch | Test changed apps since previous commit |
| `release` | Release process | Test only the app being released |
| `local` | Local development | Test all apps |

### Test Targets

For each app, the following targets are tested:
- `//{app}/...` - All targets in the app directory
- `//libs/...` - Shared libraries (always tested)
- `//tools:test_helper_test` - Test helper itself (always tested)

## Benefits

1. **Faster CI**: Only tests what has changed
2. **Focused Testing**: Release testing validates only the released app
3. **Consistent**: Same logic for local and CI testing
4. **Maintainable**: Centralized testing logic
5. **Extensible**: Easy to add new contexts or testing strategies

## Integration with Release Process

The testing helper integrates seamlessly with the release process:

1. **Release Planning**: Release helper determines which apps to release
2. **Pre-Release Testing**: Test helper validates each app before release
3. **Post-Release**: Same testing logic can be used for validation

This ensures that only thoroughly tested apps are released to production.