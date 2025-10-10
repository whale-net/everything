# Test Coverage Quick Reference

## 🎯 Quick Commands

```bash
# Run tests with coverage
bazel coverage //...

# Run coverage on specific target
bazel coverage //demo/hello_python:test_main

# Generate HTML report (requires lcov)
./tools/collect_coverage.sh

# Update dependencies lock file
./tools/update_lock.sh
```

## 📊 Coverage Reports

### Local Development
```bash
# Collect coverage
bazel coverage //...

# Find coverage report
ls $(bazel info output_path)/_coverage/_coverage_report.dat

# Generate HTML (optional, requires genhtml)
./tools/collect_coverage.sh
open coverage_output/html/index.html
```

### CI/CD Pipeline
- ✅ Automatic on every PR
- ✅ Uploads to Codecov
- ✅ Bot comments on PRs
- ✅ Dashboard: https://codecov.io/gh/whale-net/everything

## 🔧 Configuration Files

| File | Purpose |
|------|---------|
| `.bazelrc` | Coverage flags for Bazel |
| `codecov.yml` | Codecov settings |
| `pyproject.toml` | Python dependencies |
| `uv.lock` | Locked dependencies |

## 📝 Coverage Flags (in .bazelrc)

```bash
# Generate LCOV format report
coverage --combined_report=lcov

# Filter what to instrument (exclude external deps)
coverage --instrumentation_filter="^//(?!external|bazel-out)"

# Include test code in coverage
coverage --instrument_test_targets
```

## 🚀 Workflow

### Adding New Code
1. Write code and tests
2. Run: `bazel test //your/package:test`
3. Check coverage: `bazel coverage //your/package:test`
4. Push and check Codecov bot on PR

### Updating Dependencies
1. Edit `pyproject.toml`
2. Run: `./tools/update_lock.sh`
3. Commit both files
4. Or use GitHub Actions: Manually trigger "Update uv.lock" workflow

## 💡 Tips

**Fast iteration:**
```bash
# Just run tests (faster)
bazel test //...

# Collect coverage (slower, for final check)
bazel coverage //...
```

**Debugging coverage issues:**
```bash
# Find coverage files
find $(bazel info output_path) -name "*.dat" -o -name "*.lcov"

# Check specific target
bazel coverage //demo/hello_python:test_main --test_output=all
```

**Coverage best practices:**
- Run `bazel test //...` during development (fast feedback)
- Run `bazel coverage //...` before committing (comprehensive)
- Check Codecov reports on PRs for coverage changes
- Aim for ≥80% coverage on new code

## 🔗 Resources

- [Detailed Setup Guide](COVERAGE_SETUP.md)
- [Post-Merge Setup](POST_MERGE_SETUP.md)
- [Codecov Dashboard](https://codecov.io/gh/whale-net/everything)
- [Bazel Coverage Docs](https://bazel.build/configure/coverage)

## ❓ Common Issues

**Coverage not generated?**
```bash
# Check if tests pass first
bazel test //...

# Try specific target
bazel coverage //demo/hello_python:test_main --test_output=streamed
```

**Lock file out of sync?**
```bash
./tools/update_lock.sh
git commit uv.lock
```

**Codecov not uploading?**
- Check `CODECOV_TOKEN` is set in GitHub secrets
- Verify coverage file exists in CI logs
- Check Codecov dashboard for errors
