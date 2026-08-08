# app-registry (CLI)

Thin gRPC client for the App Registry. Skeleton in **AR-1**, commands added
alongside the RPCs they call in **AR-2** / **AR-3**.

Not yet implemented.

## Thin means thin

**No business logic lives here.** No version math, no promotability rules, no
promotion-legality checks — those are server-side so a future UI cannot drift
from the CLI. This file exists mostly to state that constraint.

The CLI validates argument shape, calls one RPC, and formats the response.

## Intended commands

```
app-registry apps list [--domain D] [--status S] [--deploy-unit U]
app-registry apps get <domain-name>
app-registry apps set-status <domain-name> --status archived --reason "..."
app-registry apps reconcile --from-plan <file> [--dry-run]     # CI

app-registry artifacts list <domain-name> [--kind image|chart] [--promotable]
app-registry artifacts get <domain-name> --version vX.Y.Z
app-registry artifacts resolve <digest|artifact-id>            # chart -> images
app-registry artifacts record ...                              # CI

app-registry promote <domain-name> <version> --env prod --reason "..."
                     [--allow-override] [--dry-run]
app-registry rollback <domain-name> --env prod --reason "..."
app-registry status <env> [--domain D] [--at <RFC3339>]
app-registry history <domain-name> [--env E]
app-registry diff <env-a> <env-b>

app-registry env list | upsert | archive                       # admin
```

`--promotable` on `artifacts list` is the answer to "what can I actually
promote?" — it filters by the derived promotability described in
[`../ARCHITECTURE.md`](../ARCHITECTURE.md#promotability).

## Conventions

- Cobra, matching `tools/release_helper_go`.
- Invoked in CI as `bazel run //tools/app_registry/cli:app-registry -- ...`,
  the same pattern as `release_helper_go` in `.github/workflows/release.yml`.
- `--format json|table`; JSON output is what CI parses.
- Client auth via `libs/go/grpcauth` client credentials. Promotion commands use
  a different credential than recording commands — see the auth split in
  [`../ARCHITECTURE.md`](../ARCHITECTURE.md#authorization).
