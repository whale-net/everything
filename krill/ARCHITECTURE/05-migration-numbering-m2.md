# Migration numbering (M2)

Assigned up front in issue #2542, but M1's own auth-flow-gap work
(`006_mcpauth_credential`, `007_ui_sessions` above) landed on `main` first
and claimed `006`/`007` before M2's tasks merged -- M2 renumbers to the
next free slots so parallel tasks under M2 never collide on a migration
version:

| Version | Contents | Task |
|---------|----------|------|
| `008` | `design_session` + `revision_event` (FR1-FR4, NFR1) | #2542 |
| `009` | Import-completion marker (FR12) | filed separately on this plan |

