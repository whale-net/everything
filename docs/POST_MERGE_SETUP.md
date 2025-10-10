# Post-Setup Instructions

After merging this PR, follow these steps to complete the coverage setup:

## 1. Update Dependencies Lock File

The `pyproject.toml` has been updated with coverage dependencies (`pytest-cov` and `coverage[toml]`), but the `uv.lock` file needs to be regenerated:

```bash
# Install uv if not already installed (one-time setup)
curl -LsSf https://astral.sh/uv/install.sh | sh

# Update the lock file
./tools/update_lock.sh
# Or manually: uv lock --python 3.11

# Commit the updated lock file
git add uv.lock
git commit -m "Update uv.lock with coverage dependencies"
git push
```

## 2. Configure Codecov

### a. Link Repository to Codecov
1. Go to https://codecov.io
2. Sign in with your GitHub account
3. Click "Add a repository"
4. Find and enable `whale-net/everything`

### b. Get Codecov Token
1. In Codecov dashboard, go to your repository
2. Navigate to Settings → General
3. Copy the "Repository Upload Token"

### c. Add Token to GitHub Secrets
1. Go to https://github.com/whale-net/everything/settings/secrets/actions
2. Click "New repository secret"
3. Name: `CODECOV_TOKEN`
4. Value: Paste the token from Codecov
5. Click "Add secret"

## 3. Test Coverage Locally

Once the lock file is updated, test coverage collection:

```bash
# Run a single test with coverage
bazel coverage //demo/hello_python:test_main

# Run all tests with coverage
bazel coverage //...

# Or use the helper script (includes HTML report)
./tools/collect_coverage.sh

# View HTML report (if genhtml is available)
open coverage_output/html/index.html
```

## 4. Verify CI Integration

After setting up the Codecov token:

1. Create a test PR or push to an existing branch
2. Check the CI workflow runs successfully
3. Verify coverage is uploaded to Codecov
4. Look for the Codecov bot comment on the PR

## Expected Results

After setup is complete:

- ✅ Tests run with coverage instrumentation in CI
- ✅ Coverage reports upload to Codecov automatically
- ✅ Codecov bot comments on PRs with coverage diff
- ✅ Coverage dashboard available at https://codecov.io/gh/whale-net/everything
- ✅ Local coverage reports can be generated with `bazel coverage //...`

## Troubleshooting

### Lock file update fails
```bash
# Check uv version
uv --version

# Try with verbose output
uv lock --python 3.11 --verbose
```

### Coverage not uploading to Codecov
- Verify `CODECOV_TOKEN` secret is set correctly
- Check CI logs for upload errors
- Ensure coverage files are being generated (check artifact uploads)

### Coverage percentage is 0%
- Verify lock file was updated and committed
- Check that tests are actually running: `bazel test //...`
- Look for coverage file at: `$(bazel info output_path)/_coverage/_coverage_report.dat`

## Reference Documentation

- Main README: [README.md](../README.md) - See "Test Coverage" section
- Detailed setup guide: [docs/COVERAGE_SETUP.md](COVERAGE_SETUP.md)
- Codecov docs: https://docs.codecov.com/docs
- Bazel coverage: https://bazel.build/configure/coverage
