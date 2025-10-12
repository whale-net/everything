"""Bazel rule for generating OpenAPI Python clients in external/ directory.

This file provides a stable public API for the openapi_client macro.
The actual implementation is in openapi_client_rule.bzl to separate
the rule implementation from the public interface.

This allows us to:
1. Change implementation details without breaking users
2. Maintain a clean separation between rule internals and public API
3. Provide a stable load() path at //tools:openapi_client.bzl
"""

load(":openapi_client_rule.bzl", _openapi_client = "openapi_client")

# Re-export the implementation with automatic model discovery
openapi_client = _openapi_client
