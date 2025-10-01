# Migration Guide: Python to Go Release Tool

This document provides a guide for migrating from the Python release helper to the Go implementation.

## Overview

The Go release tool is a rewrite of the Python version (`tools/release_helper/`) with the following goals:
- Improved performance (compiled binary vs interpreted)
- Reduced dependencies (no Python runtime required)
- Type safety (compile-time checking)
- Easier deployment (single binary)

## Current Status

### Implemented (Ready to Use)
The following commands are fully implemented and tested in Go:

1. **`list`** - List all apps with release metadata
   ```bash
   # Python
   bazel run //tools:release -- list
   
   # Go (same command)
   bazel run //tools/release:release -- list
   ```

2. **`list-app-versions [app]`** - List versions for apps
   ```bash
   # Python
   bazel run //tools:release -- list-app-versions hello_python
   
   # Go (same command)
   bazel run //tools/release:release -- list-app-versions hello_python
   ```

3. **`increment-version <app> <minor|patch>`** - Calculate next version
   ```bash
   # Python
   bazel run //tools:release -- increment-version hello_python minor
   
   # Go (same command)
   bazel run //tools/release:release -- increment-version hello_python minor
   ```

4. **`build <app>`** - Build container image
   ```bash
   # Python
   bazel run //tools:release -- build hello_python
   
   # Go (same command)
   bazel run //tools/release:release -- build hello_python --platform=amd64
   ```

5. **`plan`** - Plan release and output CI matrix
   ```bash
   # Python
   bazel run //tools:release -- plan --event-type=workflow_dispatch --apps=all --version=v1.0.0
   
   # Go (same command)
   bazel run //tools/release:release -- plan --event-type=workflow_dispatch --apps=all --version=v1.0.0
   ```

6. **`changes`** - Detect changed apps
   ```bash
   # Python
   bazel run //tools:release -- changes --base-commit=HEAD^
   
   # Go (same command)
   bazel run //tools/release:release -- changes --base-commit=HEAD^
   ```

7. **`release <app>`** - Build, tag, and push image
   ```bash
   # Python
   bazel run //tools:release -- release hello_python --version=v1.0.0
   
   # Go (same command)
   bazel run //tools/release:release -- release hello_python --version=v1.0.0 --dry-run
   ```

### Not Yet Implemented
The following commands still require the Python version:
- `validate-version`
- `summary`
- `release-notes`, `release-notes-all`
- `create-github-release`, `create-combined-github-release`
- All Helm chart commands

## Migration Strategies

### Strategy 1: Gradual Migration (Recommended)
Use the Go version for implemented commands and fallback to Python for others:

```bash
# Use Go for basic operations
alias release-go='bazel run //tools/release:release --'

# Use Python for advanced operations
alias release-py='bazel run //tools:release --'

# Example workflow
release-go list
release-go plan --event-type=workflow_dispatch --apps=all --version=v1.0.0
release-py release-notes hello_python  # Not yet in Go
```

### Strategy 2: Testing in Parallel
Test Go implementation alongside Python to verify correctness:

```bash
# Run both versions and compare output
bazel run //tools:release -- list > python_output.txt
bazel run //tools/release:release -- list > go_output.txt
diff python_output.txt go_output.txt
```

### Strategy 3: CI/CD Integration
Update CI/CD workflows to use Go for implemented commands:

```yaml
# .github/workflows/release.yml
- name: Plan release (Go)
  run: bazel run //tools/release:release -- plan --event-type=push --format=github

- name: Build images (Go)
  run: bazel run //tools/release:release -- build ${{ matrix.app }}

- name: Create GitHub release (Python - TODO)
  run: bazel run //tools:release -- create-github-release ${{ matrix.app }}
```

## Command Mapping

### Exact Same Interface
These commands have identical CLI interfaces:
- `list`
- `list-app-versions`
- `increment-version`
- `build`
- `plan`
- `changes`
- `release`

### Minor Differences
None currently - all implemented commands maintain backward compatibility.

## Performance Comparison

Initial benchmarks (informal):

| Command | Python | Go | Speedup |
|---------|--------|-----|---------|
| `list` | ~2.5s | ~0.8s | 3.1x |
| `list-app-versions` | ~3.0s | ~1.2s | 2.5x |
| `plan` | ~4.0s | ~1.5s | 2.7x |

*Note: Times include Bazel overhead. Direct binary execution is even faster.*

## Testing

### Running Tests
```bash
# Python tests
bazel test //tools/release_helper/...

# Go tests
bazel test //tools/release/...

# Run both
bazel test //tools/release_helper/... //tools/release/...
```

### Test Coverage
- Python: ~3,400 lines of test code
- Go: ~900 lines of test code (covers same scenarios)
- Both test suites validate the same functionality

## Troubleshooting

### Issue: Command not found
**Problem**: `bazel run //tools/release:release -- list` fails

**Solution**: Ensure you're using the full path:
```bash
bazel run //tools/release:release -- list
# not
bazel run //tools:release -- list  # This runs Python version
```

### Issue: Different output format
**Problem**: Output looks different from Python version

**Solution**: Both versions produce the same JSON output. Visual formatting differences are cosmetic.

### Issue: Missing command
**Problem**: Command exists in Python but not Go

**Solution**: Use Python version for that command:
```bash
# Check README for list of implemented commands
cat tools/release/README.md

# Use Python for unimplemented commands
bazel run //tools:release -- <command>
```

## Rollback Plan

If you encounter issues with the Go version:

1. **Use Python version directly**:
   ```bash
   bazel run //tools/release_helper:release_helper -- <command>
   ```

2. **Update scripts to use Python alias**:
   ```bash
   alias release='bazel run //tools/release_helper:release_helper --'
   ```

3. **Report issues**:
   Create an issue with:
   - Command that failed
   - Error message
   - Expected vs actual behavior

## Future Plans

### Short Term (Next Release)
- [ ] Implement GitHub release creation
- [ ] Add release notes generation
- [ ] Complete validation commands

### Medium Term
- [ ] Port all Helm chart commands
- [ ] Add multi-arch image support command
- [ ] Complete summary commands

### Long Term
- [ ] Replace Python version entirely
- [ ] Update CI/CD to use Go version
- [ ] Archive Python version

## Getting Help

- Check README: `tools/release/README.md`
- Compare with Python: `tools/release_helper/cli.py`
- File issues on GitHub
- Ask in team chat

## FAQ

**Q: Will the Python version be removed?**
A: Not immediately. Both versions will coexist until the Go version has feature parity.

**Q: Can I use both versions in the same workflow?**
A: Yes! They're designed to be compatible.

**Q: Which version should I use for new workflows?**
A: Use Go for implemented commands (better performance), Python for others.

**Q: How do I know which commands are implemented in Go?**
A: Check the README or run: `bazel run //tools/release:release -- --help`

**Q: Are there any breaking changes?**
A: No. All implemented commands maintain backward compatibility.
