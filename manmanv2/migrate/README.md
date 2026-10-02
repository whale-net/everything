# manmanv2-control-migration

Schema migration runner for the manmanv2 control-plane database. Built on
`libs/go/migrate` (golang-migrate v4) over the SQL in `schema/migrations/`.

## Pattern

Embedded SQL plus `libs/go/migrate`, with the `//go:embed` living in a small
`schema` sub-package (`schema.Migrations` / `schema.Dir`) rather than directly
in `main.go`, so the Postgres integration tests under
`api/repository/postgres` can apply the same real migrations instead of
duplicating the SQL as hand-written DDL.

```go
// migrate/schema/schema.go
//go:embed migrations/*.sql
var Migrations embed.FS
const Dir = "migrations"

// migrate/main.go
func main() { migrate.RunCLI(schema.Migrations, schema.Dir) }
```

This matches `tools/app_registry/migrate/schema`, `leaflab/migrate/schema`,
`whagent_net/migrate/schema`, and `audience_score_system/migrate/schema`.
manmanv2 was the last domain still keeping its embed inside `package main`.

## Migrations

`schema/migrations/` holds numbered `NNN_name.up.sql` / `NNN_name.down.sql`
pairs, `001_initial_schema` through `047_*`. Migration numbers must be unique:
golang-migrate fails on a duplicate version at **deploy** time, not build time,
so a collision is invisible to CI.

## Testing

Per-migration round-trip tests live in this package as
`migration_0NN_integration_test.go`. They're build-tagged `integration` and
`manual`-tagged, so they never run under `bazel test //...` or CI. Run one
explicitly (requires a working Docker daemon):

```bash
bazel test //manmanv2/migrate:migration_046_integration_test --test_output=all
```

Each applies the real embedded migration history through
`migrate.NewRunner`, so these tests also cover the `schema` package's embed.

## MCP allowlists (migration 047)

`mcp_gamer_start_allowlist` and `mcp_gamer_action_allowlist` are empty by
default and changed by admins outside MCP tools. `deployment_id` is an
`sgc_id`. Grant / revoke (SCD2: close the current row, never delete):

```sql
INSERT INTO mcp_gamer_start_allowlist (deployment_id, granted_by) VALUES (42, 'admin@example.com');
UPDATE mcp_gamer_start_allowlist SET valid_to = NOW() WHERE deployment_id = 42 AND valid_to IS NULL;

INSERT INTO mcp_gamer_action_allowlist (deployment_id, action_name, granted_by) VALUES (42, 'say', 'admin@example.com');
UPDATE mcp_gamer_action_allowlist SET valid_to = NOW()
WHERE deployment_id = 42 AND action_name = 'say' AND valid_to IS NULL;
```
