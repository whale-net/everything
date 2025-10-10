# Test Coverage Implementation Summary

## Overview

This implementation adds comprehensive test coverage support to the Everything monorepo using Bazel's native coverage capabilities and Codecov for reporting.

## What Was Added

### 1. Core Configuration

**`.bazelrc`** - Added coverage flags:
- `coverage --combined_report=lcov` - Generate LCOV format reports
- `coverage --instrumentation_filter="^//(?!external|bazel-out)"` - Exclude external dependencies
- `coverage --instrument_test_targets` - Include test code in coverage

**`pyproject.toml`** - Added dependencies:
- `pytest-cov` - Coverage plugin for pytest
- `coverage[toml]` - Core coverage.py with TOML support

**`codecov.yml`** - Codecov configuration:
- Auto-detect coverage targets with 1% threshold
- Ignore test files from coverage reports
- Configure PR comment layout and behavior

### 2. Scripts and Automation

**`tools/collect_coverage.sh`** - Local coverage collection:
- Runs `bazel coverage //...`
- Collects LCOV report from Bazel output
- Optionally generates HTML report with genhtml

**`tools/update_lock.sh`** - Dependency lock file updater:
- Updates `uv.lock` after modifying `pyproject.toml`
- Simple wrapper around `uv lock --python 3.11`

**`.github/workflows/update-lock.yml`** - Automated lock updates:
- Manually triggered workflow
- Updates `uv.lock` and creates PR automatically
- Useful when network access is limited locally

### 3. CI/CD Integration

**`.github/workflows/ci.yml`** - Updated test job:
1. Runs tests: `bazel test //...`
2. Collects coverage: `bazel coverage //...`
3. Uploads to Codecov using `codecov/codecov-action@v4`

Coverage is collected on every CI run and uploaded automatically when `CODECOV_TOKEN` is configured.

### 4. Documentation

**`README.md`** - Updated with:
- Codecov badge in header
- Test coverage section with quick commands
- Setup requirements and instructions
- Link to detailed guides

**`docs/COVERAGE_SETUP.md`** - Comprehensive guide:
- Local setup instructions
- CI/CD configuration steps
- Troubleshooting common issues
- Best practices

**`docs/POST_MERGE_SETUP.md`** - Post-merge checklist:
- Step-by-step instructions for completing setup
- Codecov configuration steps
- Lock file update process
- Verification steps

**`docs/COVERAGE_QUICK_REF.md`** - Quick reference:
- Common commands
- Configuration overview
- Workflow guidance
- Tips and tricks

## Architecture

### Coverage Flow

```
┌─────────────────┐
│ Developer       │
│ writes tests    │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ bazel coverage  │
│ //...           │
└────────┬────────┘
         │
         ▼
┌─────────────────────────┐
│ Bazel instruments code  │
│ & collects coverage     │
└────────┬────────────────┘
         │
         ▼
┌──────────────────────────┐
│ LCOV report generated    │
│ _coverage_report.dat     │
└────────┬─────────────────┘
         │
    ┌────┴────┐
    │         │
    ▼         ▼
┌────────┐  ┌──────────┐
│ Local  │  │ CI/CD    │
│ HTML   │  │ Upload   │
│ Report │  │ Codecov  │
└────────┘  └──────────┘
```

### Key Components

1. **Bazel Coverage** - Native coverage collection using `bazel coverage`
2. **LCOV Format** - Industry-standard coverage report format
3. **Codecov** - Cloud-based coverage reporting and analytics
4. **GitHub Actions** - Automated coverage collection on every CI run

## Usage

### For Developers

```bash
# Quick test during development
bazel test //your/package:test

# Full coverage check before committing
bazel coverage //...

# Generate HTML report
./tools/collect_coverage.sh
open coverage_output/html/index.html
```

### For CI/CD

Coverage is automatic:
1. Every PR run collects coverage
2. Reports upload to Codecov
3. Bot comments on PR with coverage diff
4. Dashboard shows trends over time

## Configuration Details

### Instrumentation Filter

```bash
--instrumentation_filter="^//(?!external|bazel-out)"
```

This includes:
- ✅ All workspace code (`//`)
- ✅ Demo apps (`//demo/...`)
- ✅ Main apps (`//manman/...`)
- ✅ Libraries (`//libs/...`)

Excludes:
- ❌ External dependencies (`external/`)
- ❌ Bazel outputs (`bazel-out/`)

### Codecov Ignores

From `codecov.yml`:
```yaml
ignore:
  - "bazel-*"           # Bazel symlinks
  - "**/*_test.py"      # Test files
  - "**/*_test.go"      # Test files
  - "tools/release_helper/**"  # Tooling
```

## Requirements

### Repository Setup
1. Python 3.11+ with uv for dependency management
2. Bazel 8.3+ with bzlmod support
3. Codecov account with repository linked

### GitHub Secrets
- `CODECOV_TOKEN` - Required for coverage uploads

### Optional Dependencies
- `lcov` - For HTML report generation (local development)
- `genhtml` - Part of lcov, generates HTML from LCOV

## Next Steps

After merging this PR:

1. **Update Lock File**
   ```bash
   # Option A: GitHub Actions workflow
   Actions → "Update uv.lock" → Run workflow
   
   # Option B: Locally
   ./tools/update_lock.sh
   git commit uv.lock -m "Update uv.lock with coverage dependencies"
   ```

2. **Configure Codecov**
   - Link repository at https://codecov.io
   - Add `CODECOV_TOKEN` to GitHub secrets
   - See [POST_MERGE_SETUP.md](POST_MERGE_SETUP.md)

3. **Verify Setup**
   ```bash
   # Local test
   bazel coverage //demo/hello_python:test_main
   
   # Full coverage
   bazel coverage //...
   ```

## Benefits

1. **Visibility** - Know what code is tested
2. **Trends** - Track coverage over time
3. **PR Integration** - See coverage impact on every PR
4. **Quality Gate** - Can enforce minimum coverage thresholds
5. **Standard Format** - LCOV is widely supported

## Limitations

1. **Go Coverage** - Currently focused on Python; Go coverage may need additional configuration
2. **External Dependencies** - Not included in coverage (by design)
3. **Test Files** - Excluded from coverage reports (by design)

## References

- [Bazel Coverage Documentation](https://bazel.build/configure/coverage)
- [Codecov Documentation](https://docs.codecov.com/docs)
- [LCOV Format](http://ltp.sourceforge.net/coverage/lcov.php)
- [pytest-cov](https://pytest-cov.readthedocs.io/)

## Maintenance

### Updating Dependencies
When adding/removing Python dependencies:
1. Edit `pyproject.toml`
2. Run `./tools/update_lock.sh`
3. Commit both files

### Modifying Coverage Settings
- Bazel flags: Edit `.bazelrc`
- Codecov settings: Edit `codecov.yml`
- CI integration: Edit `.github/workflows/ci.yml`

### Troubleshooting
See [COVERAGE_SETUP.md](COVERAGE_SETUP.md) for common issues and solutions.
