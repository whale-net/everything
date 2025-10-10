# Test Coverage Setup Guide

## Overview
This repository now supports test coverage collection with Codecov integration.

## Local Setup

### 1. Update Python Dependencies

After modifying `pyproject.toml` to add coverage dependencies, update the lock file:

```bash
# Install uv if not already installed
curl -LsSf https://astral.sh/uv/install.sh | sh

# Update the lock file
uv lock --python 3.11
```

### 2. Collect Coverage Locally

```bash
# Run tests with coverage
bazel coverage //...

# Or use the helper script (generates HTML report too)
./tools/collect_coverage.sh
```

The coverage report will be saved to `coverage_output/coverage.lcov`.

### 3. View Coverage Report

**Generate HTML report:**
```bash
# Requires lcov package (install via package manager)
# On Ubuntu/Debian: sudo apt-get install lcov
# On macOS: brew install lcov

./tools/collect_coverage.sh
open coverage_output/html/index.html  # macOS
xdg-open coverage_output/html/index.html  # Linux
```

## CI/CD Integration

### Codecov Setup

1. **Link Repository to Codecov:**
   - Go to https://codecov.io
   - Sign in with GitHub
   - Add your repository

2. **Add Codecov Token to GitHub Secrets:**
   - Get your token from Codecov dashboard
   - Go to GitHub repository → Settings → Secrets and variables → Actions
   - Add new secret: `CODECOV_TOKEN` with the token value

3. **Automatic Upload:**
   - Coverage is automatically collected and uploaded on every CI run
   - View coverage reports in pull request comments
   - Access detailed reports at https://codecov.io/gh/OWNER/REPO

## Coverage Configuration

### `.bazelrc` Settings

```starlark
# Coverage configurations
coverage --combined_report=lcov
coverage --instrumentation_filter="^//(?!external|bazel-out|demo|manman)"
coverage --instrument_test_targets
```

### `codecov.yml` Settings

- **Project coverage:** Target auto-detection with 1% threshold
- **Patch coverage:** Target auto-detection with 1% threshold
- **Ignored paths:** Test files, demo apps, external dependencies

## Troubleshooting

### Coverage report not generated

If `bazel coverage //...` doesn't generate a report:

1. Check if tests are passing: `bazel test //...`
2. Look for the coverage file manually:
   ```bash
   find $(bazel info output_path) -name "*.dat" -o -name "*.lcov"
   ```
3. Try running coverage on a specific target:
   ```bash
   bazel coverage //demo/hello_python:test_main
   ```

### CODECOV_TOKEN not set in CI

If CI fails to upload to Codecov:
- Verify the secret is set in GitHub repository settings
- Check the secret name matches exactly: `CODECOV_TOKEN`
- Ensure the workflow has access to secrets (not available for forked PRs)

### Coverage percentages seem low

- Check `codecov.yml` ignore patterns
- Verify instrumentation filter in `.bazelrc`
- Some code may not be covered by tests yet

## Best Practices

1. **Run coverage before committing:**
   ```bash
   bazel coverage //...
   ```

2. **Check coverage on PR branches:**
   - Codecov bot will comment on PRs with coverage diff
   - Aim for ≥80% coverage on new code

3. **Focus on meaningful coverage:**
   - Don't chase 100% coverage
   - Focus on testing critical paths and edge cases

4. **Local iteration:**
   - Use `bazel test //...` during development (faster)
   - Use `bazel coverage //...` before pushing (comprehensive)
