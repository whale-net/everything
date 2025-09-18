apiVersion: v2
name: {{APP_NAME}}
description: {{DESCRIPTION}}
type: application
version: {{CHART_VERSION}}
appVersion: {{APP_VERSION}}
home: https://github.com/whale-net/everything
sources:
  - https://github.com/whale-net/everything
maintainers:
  - name: whale-net
    url: https://github.com/whale-net
keywords:
  - {{DOMAIN}}
  - {{APP_NAME}}
annotations:
  whale-net.io/domain: {{DOMAIN}}
  whale-net.io/language: {{LANGUAGE}}
  whale-net.io/image: {{IMAGE_REPO}}:{{APP_VERSION}}