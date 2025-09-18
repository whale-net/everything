# ManMan Host Helm Chart Tests

This directory contains comprehensive tests for the ManMan Host Helm chart to ensure proper template rendering, resource creation, and Pod Disruption Budget functionality.

## Test Structure

### Test Scripts

1. **`test-chart.sh`** - Main integration test suite
   - Tests complete chart rendering with different configurations
   - Validates all resources are created correctly
   - Verifies Pod Disruption Budgets are properly configured

2. **`test-pdb.sh`** - Pod Disruption Budget specific tests
   - Tests PDB behavior with different replica counts
   - Validates conditional PDB creation based on service enablement
   - Verifies PDB selector labels match deployment labels

3. **`test-templates.sh`** - Unit tests for individual templates
   - Tests each template file separately
   - Validates conditional rendering logic
   - Checks template content structure

### Test Values

The `test-values/` directory contains different configuration scenarios:

- **`all-enabled.yaml`** - All services enabled with default settings
- **`minimal.yaml`** - Only essential services (experience + status APIs)
- **`production.yaml`** - Production-like setup with higher replicas and TLS
- **`dev-ingress.yaml`** - Development setup with ingress enabled

## Running the Tests

### Prerequisites

- `helm` CLI tool installed
- `kubectl` CLI tool installed (for YAML validation)
- Bash shell

### Running All Tests

```bash
# Make scripts executable
chmod +x *.sh

# Run the main test suite
./test-chart.sh

# Run PDB-specific tests
./test-pdb.sh

# Run template unit tests
./test-templates.sh
```

### Running Individual Tests

```bash
# Test specific configuration
helm template manman-test . -f test-values/production.yaml

# Test specific template
helm template manman-test . -s templates/experience-api-pdb.yaml

# Validate generated YAML
helm template manman-test . | kubectl --dry-run=client apply -f -
```

## What the Tests Validate

### Chart Structure Tests
- ✅ All templates render without errors
- ✅ Generated YAML is valid Kubernetes syntax
- ✅ Required resources are created for enabled services
- ✅ Disabled services don't create resources
- ✅ Configuration values are properly templated

### Pod Disruption Budget Tests
- ✅ PDBs are created for all enabled services
- ✅ PDBs have `minAvailable: 1` setting
- ✅ PDB selectors match deployment labels
- ✅ PDBs are conditionally created based on service enablement
- ✅ PDB names follow consistent naming convention

### Service-Specific Tests
- ✅ **Experience API**: Deployment, Service, and PDB created
- ✅ **Worker DAL API**: Deployment, Service, and PDB created
- ✅ **Status API**: Deployment, Service, and PDB created
- ✅ **Status Processor**: Deployment and PDB created (no service needed)

### Configuration Tests
- ✅ Environment variables are properly set
- ✅ Resource limits and requests are configured
- ✅ Image names and tags are templated correctly
- ✅ Namespace assignment works properly
- ✅ Ingress configuration (when enabled)

## Test Output

All test scripts create output in the `test-output/` directory:

```
test-output/
├── default-all-enabled.yaml       # Full chart with all services
├── minimal-config.yaml            # Minimal configuration
├── production-config.yaml         # Production-like setup
├── dev-with-ingress.yaml         # Development with ingress
├── pdb-replicas-*.yaml           # PDB tests with different replica counts
├── conditional.yaml              # Conditional service creation
└── *.yaml                        # Individual template outputs
```

## Pod Disruption Budget Validation

The tests specifically validate that:

1. **Always Available**: Each service has a PDB ensuring at least 1 pod remains available
2. **Selector Matching**: PDB selectors exactly match the deployment pod labels
3. **Conditional Creation**: PDBs are only created when the corresponding service is enabled
4. **Consistent Naming**: PDB names follow the pattern `{service-name}-{environment}-pdb`

### Example PDB Structure

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: manman-experience-dev-pdb
  namespace: manman
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: manman-experience-dev
```

## Troubleshooting

### Common Issues

1. **Template Rendering Fails**
   ```bash
   # Check for syntax errors in templates
   helm template manman-test . --debug
   ```

2. **PDB Not Created**
   ```bash
   # Verify service is enabled in values
   helm template manman-test . -s templates/experience-api-pdb.yaml
   ```

3. **Selector Mismatch**
   ```bash
   # Compare deployment and PDB labels
   helm template manman-test . | grep -A 5 -B 5 "matchLabels"
   ```

### Test Debugging

Enable debug output in test scripts:
```bash
# Add to any test script
set -x  # Enable command tracing
```

View detailed helm output:
```bash
helm template manman-test . --debug --dry-run
```

## Contributing

When adding new templates or modifying existing ones:

1. Update the relevant test values files
2. Add validation for new resources in the test scripts
3. Ensure new services include Pod Disruption Budgets
4. Run all tests to verify changes don't break existing functionality

## Test Results Interpretation

- ✅ **Green checkmarks**: Test passed
- ❌ **Red X marks**: Test failed - requires investigation
- ⚠️ **Yellow warnings**: Test completed but with warnings

The tests will exit with code 0 on success and non-zero on failure, making them suitable for CI/CD pipelines.
