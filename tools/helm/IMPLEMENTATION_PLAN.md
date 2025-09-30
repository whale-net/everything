# Bazel Helm Chart Composition System - Implementation Plan

**Status**: Approved for Implementation  
**Date**: September 29, 2025  
**Owner**: whale-net/everything

---

## Executive Summary

This plan outlines the implementation of a **Bazel-native Helm chart composition system** that generates Kubernetes manifests from `release_app` definitions. The system will use **Go-based template rendering** with **file-based template storage** to compose charts declaratively based on app types (external-api, internal-api, worker, job).

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                     Bazel Starlark Layer                         │
│  helm_chart(apps=[external_api, internal_api, worker, job])     │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                  Go Template Composer Tool                       │
│  • Reads app_metadata JSON                                       │
│  • Determines app type (external-api, internal-api, etc.)        │
│  • Loads appropriate templates from //tools/helm/templates/      │
│  • Renders Helm chart with merged values                        │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Generated Helm Chart                          │
│  Chart.yaml + values.yaml + templates/*.yaml                    │
└─────────────────────────────────────────────────────────────────┘
```

## Core Principles

1. **Template transparency**: All Kubernetes YAML templates stored as files in `tools/helm/templates/`
2. **Type-based composition**: App type determines which template artifacts are included
3. **Bazel-native**: Fully integrated with existing `release_app` system
4. **Go tooling**: Single Go binary for template composition (similar to `release_helper`)
5. **Zero string concatenation**: Pure Go template rendering, no embedded strings

## Design Decisions (Approved)

### 1. App Type Naming ✅
**Decision**: Use `external-api`, `internal-api`, `worker`, `job`

### 2. Template Organization ✅
**Decision**: Organize by artifact with type variants
- Rationale: External-api and internal-api share most deployment configuration; external-api just bundles an ingress with it
- Structure: `templates/deployment.yaml.tmpl` with conditional logic for type-specific behavior

### 3. Values Structure ✅
**Decision**: Design a new values schema
- Existing old chart values.yaml should inform requirements
- No strict compatibility requirement with old structure

### 4. Ingress Strategy ✅
**Decision**: Aggregate all external-apis into one Ingress by default, but make it configurable
- Default: Single Ingress for convenience
- Support: Per-app Ingress when needed (separate subdomains, different TLS configs, etc.)

### 5. Migration Timeline ✅
**Decision**: Keep both systems in parallel
- Requirement: Write comprehensive migration strategy
- Allow gradual migration from manual charts to generated charts

---

## Milestone Breakdown

### ✅ = Complete | 🚧 = In Progress | ⏸️ = Blocked | ⏳ = Not Started

---

## 🎯 Milestone 1: Foundation & Template Structure ✅

**Goal**: Establish the file structure and base Helm templates

**Status**: COMPLETE - All tasks finished, tests passing

**Status**: Not Started  
**Estimated Duration**: 1-2 days

### Deliverables

1. **Directory Structure**
   ```
   tools/helm/
   ├── BUILD.bazel
   ├── composer.go           # Main Go template composer
   ├── types.go              # App type definitions
   ├── templates/            # All Helm template files
   │   ├── base/
   │   │   ├── Chart.yaml.tmpl
   │   │   └── values.yaml.tmpl
   │   ├── deployment.yaml.tmpl      # Shared by external-api, internal-api, worker
   │   ├── service.yaml.tmpl         # Shared by external-api, internal-api
   │   ├── ingress.yaml.tmpl         # Only for external-api
   │   ├── job.yaml.tmpl             # Only for job type
   │   └── pdb.yaml.tmpl             # Shared by all long-running apps
   ├── testdata/             # Test fixtures
   │   └── sample_metadata/
   └── IMPLEMENTATION_PLAN.md  # This document
   ```

2. **App Type System**
   - Define app types: `external-api`, `internal-api`, `worker`, `job`
   - Mapping logic: type → template artifacts
   - Extend `release_app` macro with optional `app_type` parameter
   - Default inference: apps with "api" in name → `internal-api`

3. **Base Templates**
   - `Chart.yaml.tmpl`: Basic chart metadata
   - `values.yaml.tmpl`: Merged values from all apps
   - Extract common patterns from old chart templates
   - Use conditionals to handle type variants within shared templates

### Testing Strategy

```bash
# Unit tests for type detection
go test //tools/helm:type_detection_test

# Template loading verification
go test //tools/helm:template_loader_test

# File structure validation
bazel test //tools/helm:structure_test
```

### Validation Criteria
- [ ] Directory structure created
- [ ] App types defined with clear semantics
- [ ] Base templates render without errors
- [ ] Unit tests pass with 100% coverage

### Notes
- **Template sharing**: deployment.yaml.tmpl should handle all deployment types (external-api, internal-api, worker) with conditional logic
- **Type variants**: Use Go template conditionals like `{{ if eq .Type "external-api" }}`

---

## 🎯 Milestone 2: Template Composer Tool ⏳

**Goal**: Build the Go binary that composes Helm charts from metadata

**Status**: Not Started  
**Estimated Duration**: 2-3 days

### Deliverables

1. **Go Composer Binary** (`//tools/helm:composer`)
   ```go
   // Command signature
   composer generate \
     --apps=//app1:metadata,//app2:metadata \
     --output=./chart \
     --namespace=manman \
     --env=dev
   ```

2. **Core Functionality**
   - Read `app_metadata` JSON files
   - Determine app type (explicit or inferred)
   - Load appropriate template files from `tools/helm/templates/`
   - Render templates with merged context
   - Generate Chart.yaml, values.yaml, templates/*.yaml

3. **Template Context Structure**
   ```go
   type ChartContext struct {
       ChartName    string
       ChartVersion string
       AppVersion   string
       Apps         []AppConfig
       Namespace    string
       Environment  string
       IngressMode  string  // "single" or "per-app"
   }
   
   type AppConfig struct {
       Name       string
       Type       AppType  // external-api, internal-api, worker, job
       Image      string
       Port       int
       Command    []string
       Env        map[string]string
       Resources  ResourceConfig
       IngressConfig *IngressConfig  // Only for external-api
   }
   
   type IngressConfig struct {
       Enabled      bool
       PathPrefix   string
       Host         string
       TLS          TLSConfig
   }
   ```

4. **Template Rendering**
   - Use Go's `text/template` package
   - Support Helm-style conditionals
   - Proper YAML indentation
   - Environment variable substitution
   - Template functions for common patterns

### Testing Strategy

```bash
# Integration test: single internal-api app
bazel run //tools/helm:composer -- generate \
  --apps=//demo/hello_python:hello_python_metadata \
  --output=/tmp/test_chart

helm lint /tmp/test_chart
helm template test /tmp/test_chart

# Integration test: mixed app types
bazel run //tools/helm:composer -- generate \
  --apps=//manman/experience_api:metadata,//manman/status_processor:metadata \
  --output=/tmp/manman_chart

helm lint /tmp/manman_chart
```

### Validation Criteria
- [ ] Composer binary builds successfully
- [ ] Single app chart renders correctly
- [ ] Multi-app chart merges values properly
- [ ] `helm lint` passes on generated charts
- [ ] `helm template` produces valid YAML
- [ ] Template files are loaded from disk (no embedded strings)

### Notes
- **No string concatenation**: All YAML content must come from template files
- **Template paths**: Use `bazel run` runfiles to locate templates at runtime

---

## 🎯 Milestone 3: Bazel Rule Integration ✅

**Goal**: Create Bazel rules to invoke composer declaratively

**Status**: COMPLETE - All deliverables implemented and tested  
**Estimated Duration**: 1-2 days

### Deliverables

1. **Bazel Rule: `helm_chart`** (`//tools:helm.bzl`)
   ```starlark
   helm_chart(
       name = "manman_chart",
       apps = [
           "//manman/experience_api:experience_api_metadata",
           "//manman/status_api:status_api_metadata",
           "//manman/worker_dal_api:worker_dal_api_metadata",
           "//manman/status_processor:status_processor_metadata",
       ],
       jobs = [
           "//manman/migrations:migrations_metadata",
       ],
       namespace = "manman",
       environment = "dev",
       chart_version = "0.1.0",
       ingress_mode = "single",  # or "per-app"
   )
   ```

2. **Rule Implementation**
   - Collect all `app_metadata` JSON files as inputs
   - Invoke `//tools/helm:composer` as action
   - Output: directory tree with chart files
   - Support for custom values overlay
   - Proper dependency tracking for rebuilds

3. **Integration with `release_app`**
   - Add `app_type` attribute to `release_app` macro
   - Auto-tag metadata for chart discovery
   - Query pattern: `kind('app_metadata', '//...')`
   - Default type inference based on naming patterns

### Testing Strategy

```bash
# Query all apps with metadata
bazel query "kind('app_metadata', //...)"

# Build a simple chart
bazel build //demo:hello_python_chart

# Validate chart structure
bazel run //demo:hello_python_chart.validate

# Integration test: complex chart
bazel build //manman:manman_host_chart
ls -la bazel-bin/manman/manman_host_chart/
```

### Validation Criteria
- [x] `helm_chart` rule builds successfully
- [x] Generated charts are reproducible (hermetic builds)
- [x] Query patterns work for app discovery
- [x] Charts validate with `helm lint`
- [x] Chart output directory structure matches Helm conventions
- [x] Rebuilds only when inputs change

---

## 🎯 Milestone 4: App Type Templates ✅

**Goal**: Implement all app type-specific templates with variants

**Status**: COMPLETE - All deliverables implemented and validated  
**Completion Date**: September 29, 2025
**Estimated Duration**: 2-3 days

### Deliverables

1. **Deployment Template** (`templates/deployment.yaml.tmpl`)
   - Handles all types: external-api, internal-api, worker
   - Conditional port exposure (not for worker)
   - HTTP probes for APIs, custom probes for workers
   - Resource requests/limits
   - Environment variables
   - ArgoCD sync-wave annotations

2. **Service Template** (`templates/service.yaml.tmpl`)
   - Only rendered for external-api and internal-api
   - ClusterIP service type
   - Port mapping from app config

3. **Ingress Template** (`templates/ingress.yaml.tmpl`)
   - Only for external-api
   - Two modes:
     - **Single**: Aggregate all external-apis into one Ingress
     - **Per-app**: Separate Ingress per app (for subdomain isolation)
   - TLS configuration support
   - Path-based routing
   - Configurable annotations

4. **Job Template** (`templates/job.yaml.tmpl`)
   - Batch Job with Helm/ArgoCD hooks
   - Pre-sync execution (for migrations)
   - Backoff and retry configuration
   - Timeout settings

5. **PodDisruptionBudget Template** (`templates/pdb.yaml.tmpl`)
   - For all long-running apps (not jobs)
   - Configurable min available replicas

### Template Feature Matrix

| Feature | external-api | internal-api | worker | job |
|---------|-------------|--------------|--------|-----|
| Deployment | ✅ | ✅ | ✅ | ❌ |
| Service | ✅ | ✅ | ❌ | ❌ |
| Ingress | ✅ | ❌ | ❌ | ❌ |
| Job | ❌ | ❌ | ❌ | ✅ |
| PDB | ✅ | ✅ | ✅ | ❌ |
| HTTP Probes | ✅ | ✅ | ❌* | ❌ |

*Workers may have HTTP health endpoints but no service exposure

### Testing Strategy

```bash
# Test each app type individually
bazel test //tools/helm:external_api_template_test
bazel test //tools/helm:internal_api_template_test
bazel test //tools/helm:worker_template_test
bazel test //tools/helm:job_template_test

# Render and validate each type
for type in external-api internal-api worker job; do
  bazel run //tools/helm:composer -- generate \
    --app-type=$type \
    --output=/tmp/test_$type
  helm lint /tmp/test_$type
done
```

### Validation Criteria
- [x] All templates use shared deployment.yaml.tmpl with conditionals
- [x] Templates include all necessary Kubernetes fields
- [x] Helm conditionals work correctly
- [x] Templates match functional requirements from old chart
- [x] Each type lints successfully
- [x] No duplicated YAML content between templates

### Test Apps Created
- [x] `demo/hello_fastapi` - external-api example
- [x] `demo/hello_internal_api` - internal-api example
- [x] `demo/hello_worker` - worker example
- [x] `demo/hello_job` - job example
- [x] `demo/multi_app_chart` - all types in one chart

### Validation Results
All charts built successfully and pass `helm lint`:
- ✅ external-api: Deployment + Service + Ingress + PDB
- ✅ internal-api: Deployment + Service + PDB (NO Ingress)
- ✅ worker: Deployment + PDB (NO Service or Ingress)
- ✅ job: Job ONLY (NO Deployment, Service, Ingress, or PDB)

### Notes
- **Shared logic**: External-api and internal-api should share 90%+ of deployment config
- **Conditional blocks**: Use `{{ if eq .Type "external-api" }}` for type-specific sections
- **Worker differences**: No service ports, potentially different health checks

---

## 🎯 Milestone 5: Multi-App Chart Composition ⏳

**Goal**: Support complex charts with multiple apps and types

**Status**: Not Started  
**Estimated Duration**: 2-3 days

### Deliverables

1. **Ingress Aggregation**
   - **Single mode**: Merge multiple external-api paths into one Ingress
   - **Per-app mode**: Generate separate Ingress per external-api
   - Support TLS configuration (shared or per-app)
   - Configurable path prefixes
   - Multi-host support for different environments

2. **Values Merging**
   - Aggregate all app configurations into single values.yaml
   - Namespace isolation
   - Shared environment variables (DB URLs, message queue config)
   - Per-app overrides (resources, replicas, custom env vars)
   - Structured values organization

3. **Dependency Ordering**
   - Jobs run before deployments (via ArgoCD sync-wave)
   - Proper ArgoCD annotations for phased rollout
   - Helm hook support for pre-install/pre-upgrade
   - Migration job patterns

4. **Chart.yaml Generation**
   - Include all apps in chart description
   - Version management (chart version vs app versions)
   - Metadata about composed apps
   - Dependencies tracking

### Example Generated Chart Structure

```
manman-host/
├── Chart.yaml
├── values.yaml              # Merged values from all apps
└── templates/
    ├── experience-api-deployment.yaml
    ├── experience-api-service.yaml
    ├── status-api-deployment.yaml
    ├── status-api-service.yaml
    ├── worker-dal-api-deployment.yaml
    ├── worker-dal-api-service.yaml
    ├── status-processor-deployment.yaml
    ├── ingress.yaml         # Aggregated paths for all external-apis
    ├── migrations-job.yaml
    └── pdbs.yaml            # All PodDisruptionBudgets
```

### Testing Strategy

```bash
# Test the full manman chart
bazel build //manman:manman_host_chart

# Validate with helm
helm lint bazel-bin/manman/manman_host_chart

# Test template rendering
helm template test bazel-bin/manman/manman_host_chart \
  --set env.app_env=dev \
  --set env.db.url=postgres://test

# Deploy to test cluster
kind create cluster --name helm-test
helm install manman-test bazel-bin/manman/manman_host_chart \
  --dry-run --debug

# Test ingress modes
bazel run //tools/helm:composer -- generate \
  --apps=//manman/experience_api:metadata,//manman/status_api:metadata \
  --ingress-mode=single \
  --output=/tmp/single_ingress

bazel run //tools/helm:composer -- generate \
  --apps=//manman/experience_api:metadata,//manman/status_api:metadata \
  --ingress-mode=per-app \
  --output=/tmp/per_app_ingress
```

### Validation Criteria
- [ ] Multi-app charts build successfully
- [ ] Ingress correctly aggregates all external-apis in single mode
- [ ] Per-app ingress mode generates separate Ingress resources
- [ ] Values file includes all app configurations
- [ ] Sync-wave ordering is correct (jobs → deployments)
- [ ] Chart deploys to test cluster without errors
- [ ] All apps start successfully in Kind cluster

### Notes
- **Ingress merging**: Single Ingress should have multiple path rules
- **Naming**: Generated file names should be `<app-name>-<resource-type>.yaml`
- **Values structure**: Organize by app, then by resource type

---

## 🎯 Milestone 6: Documentation & Migration Strategy ⏳

**Goal**: Comprehensive documentation and migration path from manual charts

**Status**: Not Started  
**Estimated Duration**: 1-2 days

### Deliverables

1. **Documentation**
   - `tools/helm/README.md`: System overview and quick start
   - `tools/helm/TEMPLATES.md`: Template development guide
   - `tools/helm/APP_TYPES.md`: App type reference
   - `tools/helm/MIGRATION.md`: Migration guide from manual charts
   - AGENT.md updates for chart composition
   - Copilot instructions updates

2. **Migration Strategy** (`tools/helm/MIGRATION.md`)
   - **Phase 1**: Parallel operation
     - Keep existing manual charts in place
     - Generate new charts for testing
     - Compare outputs side-by-side
   - **Phase 2**: Validation
     - Deploy generated charts to dev environment
     - Verify functionality matches manual charts
     - Identify and fix discrepancies
   - **Phase 3**: Gradual adoption
     - Migrate one chart at a time
     - Start with simple charts (single app)
     - Move to complex charts (manman)
   - **Phase 4**: Deprecation
     - Archive old manual charts
     - Update CI/CD to use generated charts
     - Remove manual chart references

3. **Example Charts**
   - Simple: Single internal-api (`demo/hello_python`)
   - Complex: Full manman chart (all types)
   - Mixed: External-api + worker combination
   - Job-only: Migration job chart

4. **Comparison Tool**
   ```bash
   # Compare manual vs generated chart
   bazel run //tools/helm:compare -- \
     --manual=manman/__manual_backup_of_old_chart/charts/manman-host \
     --generated=bazel-bin/manman/manman_host_chart
   ```

5. **CI Integration**
   - Add chart validation to CI pipeline
   - Helm lint checks on all generated charts
   - Chart versioning strategy
   - Automated testing of generated charts

### Migration Checklist Template

```markdown
## Chart Migration Checklist: [CHART_NAME]

### Pre-Migration
- [ ] Identify all apps in existing chart
- [ ] Document app types and roles
- [ ] List all custom configurations
- [ ] Note any special requirements

### Generation
- [ ] Create `helm_chart` rule
- [ ] Configure app metadata with types
- [ ] Set namespace and environment
- [ ] Configure ingress mode

### Validation
- [ ] Generated chart passes `helm lint`
- [ ] `helm template` output is valid
- [ ] Side-by-side comparison with manual chart
- [ ] All resources present in generated chart
- [ ] Values match or improve on manual chart

### Testing
- [ ] Deploy to dev environment
- [ ] Verify all pods start successfully
- [ ] Test service connectivity
- [ ] Validate ingress routing
- [ ] Check job execution (if applicable)

### Production
- [ ] Update CI/CD to use generated chart
- [ ] Deploy to staging
- [ ] Deploy to production
- [ ] Archive manual chart
- [ ] Update documentation
```

### Testing Strategy

```bash
# Documentation examples must work
bazel build //demo:hello_python_chart
bazel build //manman:manman_host_chart

# CI validation
bazel test //tools/helm:all
bazel run //tools/helm:validate_all_charts

# Migration validation
bazel run //tools/helm:migration_test -- \
  --chart=manman_host
```

### Validation Criteria
- [ ] Documentation is comprehensive and accurate
- [ ] All examples build successfully
- [ ] Migration guide provides clear step-by-step process
- [ ] Comparison tool highlights differences
- [ ] CI pipeline includes chart validation
- [ ] AGENT.md reflects new system
- [ ] Parallel operation is fully supported

### Notes
- **Migration timeline**: No hard deadline, but aim for gradual adoption
- **Backward compatibility**: Manual charts should continue working during migration
- **Rollback plan**: Keep manual charts as backup during initial production deployments

---

## Testing Strategy Summary

### Unit Tests
- Template loading and parsing
- App type detection logic
- Values merging algorithms
- YAML rendering correctness
- Ingress aggregation logic

### Integration Tests
- Single-app chart generation
- Multi-app chart composition
- Helm lint validation
- Helm template rendering
- Both ingress modes (single and per-app)

### End-to-End Tests
- Deploy to local Kind cluster
- Validate pod startup
- Test service connectivity
- Verify ingress routing
- Job execution and completion

### Regression Tests
- Compare generated charts with old manual charts
- Validate manman chart functionality
- Ensure all original features preserved
- Performance benchmarks for chart generation

---

## Implementation Order

1. **Milestone 1**: Foundation (1-2 days) ⏳
2. **Milestone 2**: Composer Tool (2-3 days) ⏳
3. **Milestone 3**: Bazel Integration (1-2 days) ⏳
4. **Milestone 4**: App Type Templates (2-3 days) ⏳
5. **Milestone 5**: Multi-App Composition (2-3 days) ⏳
6. **Milestone 6**: Documentation & Migration (1-2 days) ⏳

**Total Estimated Time**: 9-15 days

---

## Success Metrics

### Technical Metrics
- [ ] All demo apps can generate working charts
- [ ] Manman chart generates and deploys successfully
- [ ] Generated charts pass `helm lint`
- [ ] Charts deploy to Kind cluster without errors
- [ ] Zero template string concatenation (all from files)
- [ ] Charts are hermetic and reproducible

### Documentation Metrics
- [ ] System documented in tools/helm/README.md
- [ ] Integrated into AGENT.md
- [ ] Migration guide completed
- [ ] All examples working

### CI/CD Metrics
- [ ] CI pipeline validates all charts on every commit
- [ ] Chart generation is fast (<5s per chart)
- [ ] Proper caching for incremental builds

### Adoption Metrics
- [ ] At least one production chart migrated
- [ ] Manual charts archived
- [ ] Team can generate new charts without assistance

---

## Risk Mitigation

### Risk: Generated charts differ from manual charts
**Mitigation**: 
- Build comparison tool early (Milestone 6)
- Side-by-side testing in dev environment
- Incremental migration with rollback capability

### Risk: Template complexity becomes unmanageable
**Mitigation**:
- Keep templates simple with clear conditionals
- Strong separation of concerns (one template per resource type)
- Comprehensive testing at each milestone

### Risk: Performance issues with large charts
**Mitigation**:
- Benchmark chart generation time
- Optimize Go template rendering
- Proper Bazel caching

### Risk: Breaking changes in Helm/Kubernetes APIs
**Mitigation**:
- Version pin Helm in CI
- Test against multiple Kubernetes versions
- Clear upgrade path in documentation

---

## Future Enhancements (Post-MVP)

- [ ] Support for StatefulSets
- [ ] ConfigMap and Secret generation
- [ ] HPA (HorizontalPodAutoscaler) templates
- [ ] NetworkPolicy generation
- [ ] ServiceMonitor for Prometheus
- [ ] Helm chart dependencies (external charts)
- [ ] Multi-cluster deployment strategies
- [ ] ArgoCD ApplicationSet generation
- [ ] Chart testing framework integration
- [ ] Visual chart diff tool

---

## Change Log

| Date | Change | Milestone |
|------|--------|-----------|
| 2025-09-29 | Initial plan created and approved | All |
| | | |

---

## Approval

**Approved by**: User  
**Date**: September 29, 2025

**Key Decisions**:
1. ✅ App types: `external-api`, `internal-api`, `worker`, `job`
2. ✅ Template organization: By artifact with type variants
3. ✅ Values schema: New design, informed by old requirements
4. ✅ Ingress strategy: Aggregate by default, configurable for per-app
5. ✅ Migration: Parallel systems with comprehensive migration strategy

---

**Next Steps**: Begin Milestone 1 - Foundation & Template Structure
