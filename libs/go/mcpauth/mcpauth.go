// Package mcpauth provides a reusable, DB-backed credential lifecycle for
// MCP (Model Context Protocol) server bearer authentication (mint, verify,
// revoke, list), together with its own OAuth2 authorization-server front
// end (RFC 9728/8414 discovery metadata, RFC 7591 dynamic client
// registration, and — see provider.go, landed by #1642 — an
// authorization-code + PKCE `/authorize` and `/token`) that an MCP client
// uses to obtain that credential.
//
// # What this library is
//
// The credential this library manages is a long-lived, high-entropy bearer
// token that an MCP client presents on every subsequent call. It is
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
// provider.go's Provider is the OAuth2 authorization-server front end that
// issues this credential: it is a genuine RFC 9728/8414/7591 (and, via
// #1642, authorization-code + PKCE) authorization server — MCP clients
// bootstrap against it exactly as they would against any other OAuth2
// authorization server, with no client-specific special-casing (NFR4).
//
// # What this library deliberately is not
//
//   - Not a verifier of *external* identity-provider tokens. mcpauth's own
//     `/authorize` endpoint never performs an OIDC/SAML round trip, a JWKS
//     fetch, or any other obtain-step protocol exchange against an
//     external IdP — Keycloak, Google OIDC, or anything else — to
//     establish who a caller is. Instead it defers to CallerResolver (see
//     resolver.go, #1640): by the time `/authorize` runs, the consuming
//     domain's own sign-in flow has already established the caller's
//     identity (a session cookie, a trusted proxy header, whatever that
//     domain's obtain-step machinery leaves on the request), and
//     CallerResolver's only job is to read it back off the request.
//     mcpauth's authorization-server role sits entirely downstream of that
//     already-established session — minting its own opaque bearer
//     credential for a caller CallerResolver already vouches for — not
//     upstream of it doing IdP verification itself.
//   - Not an access/refresh-token lifecycle. There is no expiry, rotation,
//     or refresh flow — a credential is live until it is explicitly
//     revoked. This is deliberately simpler than typical OAuth2 access
//     tokens because MCP bearer credentials are meant to be long-lived and
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
