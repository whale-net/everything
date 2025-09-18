# Helm Chart Templates

This directory contains the template files used to generate Helm charts for applications in the Everything monorepo.

## Template Files

- `Chart.yaml.tpl` - Chart metadata template
- `values.yaml.tpl` - Default values template with baked-in image versions
- `deployment.yaml.tpl` - Kubernetes Deployment manifest
- `service.yaml.tpl` - Kubernetes Service manifest
- `serviceaccount.yaml.tpl` - Kubernetes ServiceAccount manifest
- `ingress.yaml.tpl` - Kubernetes Ingress manifest (optional)
- `_helpers.tpl.tpl` - Helm template helpers for consistent naming

## Template Variables

Templates use the following substitution variables:

- `{{APP_NAME}}` - Application name
- `{{DESCRIPTION}}` - Application description
- `{{CHART_VERSION}}` - Helm chart version
- `{{APP_VERSION}}` - Application/image version
- `{{DOMAIN}}` - Application domain
- `{{LANGUAGE}}` - Programming language
- `{{IMAGE_REPO}}` - Container image repository

## Customization

To customize charts for specific applications:

1. Create app-specific template files
2. Use the same variable substitution format
3. Reference custom templates in the `release_app` call

See the main [Helm Charts Documentation](../../docs/HELM_CHARTS.md) for usage details.