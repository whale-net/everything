# Helm Deployment Refactoring

## Overview

Extracted common Helm chart deployment logic from domain-specific Tiltfiles into a reusable `deploy_helm_chart()` function in `tools/tilt/common.tilt`.

## Motivation

The Helm deployment pattern was duplicated across domain Tiltfiles with only minor variations:
- Building the Bazel chart target
- Setting global configuration (namespace, domain, environment)
- Configuring per-app settings (images, env vars, helm values)
- Rendering with `helm template` and deploying with `k8s_yaml()`

This refactoring centralizes the common logic while maintaining flexibility for domain-specific customization.

## Implementation

### Function Signature

```python
def deploy_helm_chart(
    domain,                  # Domain name (e.g., 'manman')
    namespace,              # Kubernetes namespace
    chart_bazel_target,     # Bazel target (e.g., '//manman:manman_chart')
    chart_name,             # Chart name (e.g., 'manman-host-services')
    apps_config,            # Dict of app configurations
    global_config={}        # Dict of global helm values
)
```

### Apps Configuration Structure

```python
apps_config = {
    'app-name': {
        'enabled_env': 'ENABLE_APP_NAME',    # Optional env var to check
        'image_name': 'domain-app-name',     # Docker image name
        'helm_key': 'domain-app-name',       # Key in helm values (optional)
        'env': {                             # Environment variables
            'VAR_NAME': 'value',
        },
        'helm_config': {                     # Additional helm config
            'ingress.tlsEnabled': 'false',
        }
    }
}
```

### Global Configuration

```python
global_config = {
    'ingressDefaults.enabled': 'true',
    'ingressDefaults.className': 'nginx',
    # Any other global helm values
}
```

## Usage Example

### Before (85 lines)

```python
# Build the Helm chart using Bazel
local('bazel build //manman:manman_chart')

chart_path = '../bazel-bin/manman/helm-manman-host-services_chart/manman-host-services'

# Build helm set arguments
helm_set_args = [
    'global.environment=dev',
    'global.domain=manman',
    'global.namespace={}'.format(namespace),
    'ingressDefaults.enabled=true',
    'ingressDefaults.className=manman-nginx',
]

# Configure images and environment for enabled apps
for app_name, config in APPS.items():
    if get_env_bool(config['enabled_env'], default='true'):
        app_key = 'manman-{}'.format(app_name)
        helm_set_args.extend([
            'apps.{}.image={}'.format(app_key, config['image_name']),
            'apps.{}.imageTag=latest'.format(app_key),
            'apps.{}.env.POSTGRES_URL={}'.format(app_key, db_url),
            # ... more env vars
        ])

# Disable TLS for local development
helm_set_args.extend([
    'apps.manman-experience-api.ingress.tlsEnabled=false',
    'apps.manman-worker-dal-api.ingress.tlsEnabled=false',
])

# Build the helm set arguments as a single string
helm_set_string = ' '.join(['--set {}'.format(arg) for arg in helm_set_args])

# Generate and deploy YAML
yaml_content = local('helm template manman-host {} --namespace {} {}'.format(
    chart_path, namespace, helm_set_string
))
k8s_yaml(yaml_content)
```

### After (43 lines)

```python
# Build apps configuration
apps_config = {}
for app_name, config in APPS.items():
    app_key = 'manman-{}'.format(app_name)
    
    env_vars = {'POSTGRES_URL': db_url}
    if app_name != 'migration':
        env_vars.update({
            'RABBITMQ_HOST': rabbitmq_host,
            'RABBITMQ_PORT': rabbitmq_port,
            'RABBITMQ_USER': rabbitmq_user,
            'RABBITMQ_PASSWORD': rabbitmq_password,
        })
    
    apps_config[app_name] = {
        'enabled_env': config['enabled_env'],
        'image_name': config['image_name'],
        'helm_key': app_key,
        'env': env_vars,
        'helm_config': {},
    }

# Disable TLS for ingress-exposed apps
apps_config['experience-api']['helm_config']['ingress.tlsEnabled'] = 'false'
apps_config['worker-dal-api']['helm_config']['ingress.tlsEnabled'] = 'false'

# Deploy
deploy_helm_chart(
    'manman',
    namespace,
    '//manman:manman_chart',
    'manman-host-services',
    apps_config,
    global_config={
        'ingressDefaults.enabled': 'true',
        'ingressDefaults.className': 'manman-nginx',
    }
)
```

**Reduction**: 85 lines → 43 lines (49% reduction)

## Benefits

### 1. Consistency
- All domains use the same deployment pattern
- Global configuration is standardized
- App configuration follows consistent structure

### 2. Maintainability
- Changes to deployment logic happen in one place
- Domain Tiltfiles focus on domain-specific configuration
- Clear separation between "what" (domain config) and "how" (deployment mechanics)

### 3. Readability
- Intent is clearer with declarative configuration
- Less boilerplate in domain Tiltfiles
- Easier to understand what's being configured

### 4. Extensibility
- Easy to add new global configuration options
- Per-app customization through `helm_config`
- Can add domain-specific overrides if needed

## Chart Path Resolution

The function automatically resolves the chart path from Bazel's output:

**Pattern**:
- Input: `//manman:manman_chart` + chart name `manman-host-services`
- Bazel output: `bazel-bin/manman/helm-manman-host-services_chart/manman-host-services/`
- Chart path: `../bazel-bin/manman/helm-manman-host-services_chart/manman-host-services`

**Formula**:
```
bazel-bin/{domain_path}/helm-{chart_name}_chart/{chart_name}
```

Where:
- `domain_path`: Extracted from bazel target (e.g., `manman` from `//manman:target`)
- `chart_name`: Provided as parameter (e.g., `manman-host-services`)

## Global Configuration

The function automatically sets these globals:
- `global.environment=dev`
- `global.domain={domain}`
- `global.namespace={namespace}`

Additional globals can be passed via `global_config` parameter.

## App Configuration

For each app in `apps_config`:
1. Check if enabled via `enabled_env` (if provided)
2. Set image name and tag (always `latest` for Tilt)
3. Apply environment variables from `env` dict
4. Apply additional helm config from `helm_config` dict

The helm key defaults to `{domain}-{app-name}` but can be overridden with `helm_key`.

## Migration Path

To migrate a domain Tiltfile to use `deploy_helm_chart()`:

1. **Add the import**:
   ```python
   load('../tools/tilt/common.tilt', 'deploy_helm_chart')
   ```

2. **Build apps_config dict**:
   ```python
   apps_config = {}
   for app_name, config in APPS.items():
       apps_config[app_name] = {
           'enabled_env': config['enabled_env'],
           'image_name': config['image_name'],
           'env': { /* env vars */ },
           'helm_config': { /* helm settings */ },
       }
   ```

3. **Call deploy_helm_chart()**:
   ```python
   deploy_helm_chart(
       'domain-name',
       namespace,
       '//domain:chart_target',
       'chart-name',
       apps_config,
       global_config={ /* globals */ }
   )
   ```

4. **Remove old helm deployment code**:
   - Remove `local('bazel build ...')`
   - Remove chart path construction
   - Remove helm_set_args building
   - Remove `helm template` and `k8s_yaml()` calls

## Future Enhancements

### Optional: Values Files
Support passing values files in addition to --set arguments:

```python
deploy_helm_chart(
    ...,
    values_files=['./values-dev.yaml', './values-local.yaml']
)
```

### Optional: Helm Hooks
Support adding pre/post helm hooks:

```python
deploy_helm_chart(
    ...,
    pre_deploy_hook=lambda: print("Pre-deploy tasks"),
    post_deploy_hook=lambda: print("Post-deploy tasks")
)
```

### Optional: Conditional Deployment
Support deploying only if certain conditions are met:

```python
deploy_helm_chart(
    ...,
    deploy_if=get_env_bool('DEPLOY_MANMAN', default='true')
)
```

## Related Documentation

- [Tilt Quick Reference](./TILT_QUICK_REFERENCE.md)
- [Tilt Final Summary](./TILT_FINAL_SUMMARY.md)
- [Watch Path Heuristics](./WATCH_PATH_HEURISTICS.md)
- [Helm Release Tool](./HELM_RELEASE.md)
