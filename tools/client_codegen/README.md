# OpenAPI Client Codegen — Tests

This directory no longer defines any OpenAPI client generation targets. It
contains only a test (`test_experience_api_client.py`) that exercises a
generated client to make sure the codegen pipeline produces something
usable.

All client generation targets — both production (ManMan) and demo/example —
now live entirely under `//generated/...`. See
[`generated/README.md`](../../generated/README.md) for directory layout,
import patterns, and how to add a new client.

## Rule implementation

- Python: `//tools/openapi:openapi_client_rule.bzl`
- Go: `//tools/openapi:openapi_go_client.bzl`

See [`../openapi/README.md`](../openapi/README.md) for usage.

## Running the test

```bash
bazel test //tools/client_codegen:test_experience_api_client
```
