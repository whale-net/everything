# krill/ui and the auth front door (the auth-flow gap)

Landing the migration above was necessary but not sufficient: `mcp`'s
auth door verifies credentials, but nothing in M1 ever *minted* one.
`krill/mcp/main.go` constructs no `auth.Provider` — only
`auth.NewCredentialStore` (verification) — so there was no
`/authorize`, `/token`, `/register`, or discovery metadata anywhere in
krill, and every other domain's own front door (`audience_score_system`,
`whagent_net`) solves this by setting `auth.ProviderConfig.SignInURL`
to its own web UI's `/login` route (`libs/go/auth/authorize.go`:
`/authorize` 401s outright when `SignInURL` is unset and the caller isn't
already resolved). krill had no UI to point at — `PRODUCT.md`'s roadmap
defers a real web UI to "Later" (C19).

`krill/ui` (`krill/ui/main.go`) is the minimum viable fix: a standalone
binary that does nothing but (1) Keycloak sign-in via `//libs/go/htmxauth`
(migration `007_ui_sessions`) and (2) construct and mount an
`auth.Provider` (migration `006_mcpauth_credential`, shared with `mcp`)
with `SignInURL: "/login"`. `krill/ui/auth.go`'s `mcpCallerResolver`
reads the signed-in operator's session and encodes their `(iss, sub)` pair
via the new `//krill/identity` package (mirroring
`whagent_net/mcpidentity`'s encoding exactly) — krill has no person/user
table to key an identity to instead (NFR1). This completes the
authorization-code + PKCE round trip end to end (discovery → registration
→ sign-in → `/authorize` → `/token` → a credential `mcp`'s
`NewCredentialStore` can verify), but deliberately does **not** touch
persona resolution: `krill/mcp/server/auth.go`'s `PersonaMiddleware` still
resolves every auth-authenticated caller to `PersonaSwarmOperator`
unconditionally, exactly as before. Widening that resolution to a real
identity → persona lookup is C12's own job (`PRODUCT.md`'s M2), not this
gap-fix's — see `auth.go`'s doc comment.

