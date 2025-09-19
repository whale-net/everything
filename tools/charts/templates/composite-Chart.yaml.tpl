apiVersion: v2
name: {{COMPOSITE_NAME}}
description: {{DESCRIPTION}}
type: application
version: {{CHART_VERSION}}
appVersion: {{CHART_VERSION}}
home: https://github.com/whale-net/everything
sources:
  - https://github.com/whale-net/everything
maintainers:
  - name: whale-net
    url: https://github.com/whale-net
keywords:
  - {{DOMAIN}}
  - composite
  - multi-app
annotations:
  whale-net.io/domain: {{DOMAIN}}
  whale-net.io/type: composite
  whale-net.io/apps: "{{APP_LIST}}"