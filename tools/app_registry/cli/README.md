# app-registry (CLI)

Thin gRPC client for the App Registry. Skeleton in **AR-1**, commands added
alongside the RPCs they call in **AR-2** / **AR-3**. All commands below are
implemented as of **AR-3d**.

## Thin means thin

**No business logic lives here.** No version math, no promotability rules, no
promotion-legality checks — those are server-side so a future UI cannot drift
from the CLI. The one exception is `diff`, which does real client-side work,
but only comparison of two already-computed server responses — see below.

The CLI validates argument shape, calls one RPC (or two, for `diff`/`history`),
and formats the response.

## Commands

```
app-registry apps list [--domain D] [--status S] [--deploy-unit U]
app-registry apps get <domain-name>
app-registry apps set-status <domain-name> --status archived --reason "..."
app-registry apps reconcile --from-plan <file> --idempotency-key K [--dry-run]     # CI

app-registry artifacts list <domain-name> [--kind image|chart] [--promotable]
app-registry artifacts get <domain-name> --version vX.Y.Z
app-registry artifacts resolve <digest|artifact-id>            # chart -> images
app-registry artifacts record ... --idempotency-key K          # CI

app-registry builds record ... --idempotency-key K             # CI

app-registry promote <domain-name> <version> --env prod --reason "..."
                     [--allow-override] [--dry-run] [--idempotency-key K]
app-registry rollback <domain-name> --env prod --reason "..."
                     [--dry-run] [--idempotency-key K]
app-registry status <env> [--domain D] [--at <RFC3339>]
app-registry history <domain-name> [--env E]
app-registry diff <env-a> <env-b>

app-registry env list | upsert | archive                       # admin
```

`--promotable` on `artifacts list` is the answer to "what can I actually
promote?" — it filters by the derived promotability described in
[`../ARCHITECTURE.md`](../ARCHITECTURE.md#promotability).

### `promote` / `rollback`

`--idempotency-key` is optional on both: if omitted, the CLI generates a UUID
(`promoteIdempotencyKey` in `promote.go`), matching ARCHITECTURE.md
"Idempotency" — CI derives a key from the workflow run, humans get a
client-generated one. `promote`'s response carries `already_promoted` when the
requested artifact is already current in that environment; the CLI prints an
explicit note to stderr rather than letting that look like a write happened.
Both commands print a `dry run: no write performed` note to stderr on
`--dry-run`, for the same reason.

### `status`

Renders `GetEnvironmentState`. Because a drifted environment (a `VIA_CHART`
image promoted directly with `--allow-override`, now diverged from its
chart's pin — see ARCHITECTURE.md "Promotability") is the exact failure mode
this command exists to surface, any `DriftEntry` on the response is printed as
a prominent stderr banner *in addition to* the JSON body on stdout, which
still carries the same data under each entry's `drift` field. The stderr
banner never affects `--format json` piped into a script — machine parsing
sees only stdout. `--at <RFC3339>` reads historical state via the SCD2
window; omit it for current state.

### `history`

Calls both `ListPromotions` (`include_history=true`) and
`ListPromotionEvents` for the given target and prints them together as one
JSON object (`{"promotions": ..., "events": ...}`) — there is no single RPC
or response message that returns both, so `printCombinedResponse` formats
each with `protojson` and combines them.

### `diff`

Calls `GetEnvironmentState` for each of `<env-a>` and `<env-b>` and computes
the difference client-side (`diffEnvironmentStates` in `promote.go`) — per
`PLAN.md`'s AR-3d scope note, there is deliberately no server-side diff RPC.
Entries are matched by promotion target (`image:<app_id>` /
`chart:<chart_id>`, the only stable identity `GetEnvironmentStateResponse`
carries); entries whose digest is identical on both sides are omitted, so the
output reads like a diff rather than a full state dump (that's what `status`
is for). Each remaining entry is tagged `only_a`, `only_b`, or `different`.

## Conventions

- Cobra, matching `tools/release_helper_go`.
- Invoked in CI as `bazel run //tools/app_registry/cli:app-registry -- ...`,
  the same pattern as `release_helper_go` in `.github/workflows/release.yml`.
- `--format json|table`; JSON output is what CI parses.
- Client auth via `libs/go/grpcauth` client credentials. Promotion commands use
  a different credential than recording commands — see the auth split in
  [`../ARCHITECTURE.md`](../ARCHITECTURE.md#authorization).
