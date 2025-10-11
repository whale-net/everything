"""Bazel rule for generating OpenAPI Python clients in external/ directory."""

load(":openapi_client_rule.bzl", _openapi_client = "openapi_client")

# Re-export the implementation with automatic model discovery
openapi_client = _openapi_client
