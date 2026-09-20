# Migration numbering (M1)

Assigned up front in issue #2487 so parallel tasks under the same
milestone never collide on a migration version:

| Version | Contents | Task |
|---------|----------|------|
| `001` | `scope` | #2487 |
| `002` | Spec entities (Product/FeatureSet/Feature/FR/NFR/LoadBearingDecision/Persona/NonGoal) | #2488 |
| `003` | `session` (FR3's `init` gate) | #2489 |
| `004` | Milestone reference + association (FR17) | #2492 |
| `005` | Pointer artifact (FR20) | #2496 |
| `006` | mcpauth credential/client/auth-code (`mcp_credential`, `mcp_oauth_client`, `mcp_auth_code`) | mcpauth auth-flow gap |
| `007` | UI sessions (`ui_sessions`, `//libs/go/htmxauth`) | mcpauth auth-flow gap |

