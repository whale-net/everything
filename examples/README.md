# Helm Chart Examples

This directory contains examples of different Helm chart patterns available in the Everything monorepo.

## Examples

### Web Services Composite Chart (`web-services-composite/`)

Demonstrates how to create a composite Helm chart that deploys multiple web services together:

- **Purpose**: Reduce cross-deployment coordination for related web services
- **Pattern**: Multi-app deployment with shared ingress and configuration
- **Benefits**: Single deployment command, coordinated rollouts, simplified operations

**Usage:**
```bash
# Build the composite chart
bazel build //examples/web-services-composite:web_services_composite_chart

# Package the composite chart  
bazel build //examples/web-services-composite:web_services_composite_package

# Deploy the composite chart
helm install my-web-services everything/web-services
```

## When to Use Each Pattern

### Individual Charts (1:1 app-to-chart)
- ✅ Independent services with different release cycles
- ✅ Services owned by different teams
- ✅ Gradual rollout of individual components
- ✅ Fine-grained version control

### Composite Charts (multi-app deployment)
- ✅ Tightly coupled services that deploy together
- ✅ Complete application stacks (frontend + APIs)
- ✅ Development/staging environments
- ✅ Reduced operational complexity

## Creating New Examples

To add new examples:

1. Create a new directory under `examples/`
2. Add a `BUILD.bazel` file with the chart definition
3. Document the pattern and use case
4. Update this README with the new example

See the existing examples for patterns to follow.