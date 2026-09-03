// Package mcpauth provides a reusable, DB-backed credential lifecycle for
// MCP (Model Context Protocol) server bearer authentication: mint, verify,
// revoke, and list.
//
// # What this library is
//
// OAuth2 (or any other identity-provider flow) is the *obtain* step only —
// it establishes who a caller is, once, interactively. The credential this
// library manages is what happens after that: a long-lived, high-entropy
// bearer token that an MCP client presents on every subsequent call. It is
// database-backed (so it survives process restarts and can be inspected),
// individually revocable (so a single compromised or retired credential can
// be closed without affecting any other), and hashed at rest (NFR1) so a
// database compromise alone does not leak usable bearer tokens.
//
// This mirrors the shape audience_score_system's `mcp_credential` table and
// `store.CredentialStore` already used in production (see
// audience_score_system/store/credential.go) — this package lifts that
// behavior into a reusable, non-domain-specific library (NFR2) so other MCP
// servers in this monorepo do not have to re-derive it.
//
// # What this library deliberately is not
//
//   - Not an identity-provider (IdP) integration. It does not talk OIDC,
//     SAML, or any obtain-step protocol. The consuming domain decides how a
//     caller proves who they are before minting a credential (e.g. an
//     authenticated web session revealing the raw token once).
//   - Not an access/refresh-token lifecycle. There is no expiry, rotation,
//     or refresh flow — a credential is live until it is explicitly
//     revoked. This is deliberately simpler than OAuth2 access tokens
//     because MCP bearer credentials are meant to be long-lived and
//     managed by the human who minted them, not silently rotated
//     underneath a running client.
//
// # NFR2 boundary — zero domain-specific types
//
// Every type in this package is generic across whatever "identity" a
// consuming domain wants to key credentials on: a Person UUID (ASS), a
// service-account name, an opaque string, anything. Nothing in this
// package's source imports, names, or references a domain package,
// audience_score_system, "person", or any other domain-specific concept
// outside of README/comment examples. See credential.go's Identity string
// type and StoreConfig for how that boundary is expressed in code.
//
// # Schema ownership (NFR5)
//
// This library does NOT own, embed, or run any migration. It names an
// unqualified table (configurable via StoreConfig.TableName, default
// "mcp_credential") and probes it at construction time, exactly like
// libs/go/htmxauth's DBSessionManager does for ui_sessions
// (libs/go/htmxauth/db_session.go). The consuming domain's own migration
// tooling is responsible for creating that table before the first call to
// NewCredentialStore. See README.md for the exact schema contract a
// consuming migration must satisfy.
package mcpauth
