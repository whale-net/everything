// Caller authentication and authorization for krill's spec-scoped MCP
// surface (FR10/NFR1, issue #2494) -- the human front door, via
// //libs/go/auth's OAuth2-capable CredentialStore. See
// whagent_auth.go for the parallel agent front door and
// ../../ARCHITECTURE.md "The MCP spec surface" for the two-door design.
//
// NFR1 authorizes by **persona** (Swarm Operator / Reader / Agent),
// never by individual identity -- there is no per-caller allow-list
// anywhere in this package. A credential minted through this front door
// carries the persona krill/ui resolved from the signer's verified
// Keycloak realm roles at mint time (roles.go); PersonaMiddleware reads it
// back off the credential row and never falls back to a default.
package server

import (
	"context"
	"fmt"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/logging"
)

// logger is this package's shared logger.
var logger = logging.Get("krill/mcp/server")

// Persona is one of NFR1's three authorization personas -- the unit every
// tool call in this package is authorized against, never an individual
// caller identity (krill/PRODUCT.md's Personas section: Swarm Operator,
// Requirement Contributor, Agent).
type Persona string

const (
	// PersonaSwarmOperator is the admin persona, resolved from
	// KRILL_ROLE_OPERATOR; see RoleConfig.ResolvePersona.
	PersonaSwarmOperator Persona = "swarm_operator"

	// PersonaReader may call read tools only. Resolved from
	// KRILL_ROLE_READER; see RoleConfig.ResolvePersona.
	PersonaReader Persona = "reader"

	// PersonaRequirementContributor is reserved for when C12 (M2) gives
	// this persona its own unmediated path into krill (see this file's
	// doc comment) -- no code path produces it yet, and no registration
	// admits it.
	PersonaRequirementContributor Persona = "requirement_contributor"

	// PersonaAgent is every caller the whagent-net front door
	// authenticates (whagent_auth.go) -- a whagent Claim never carries a
	// human profile, so every such caller is this persona, unconditionally.
	PersonaAgent Persona = "agent"
)

// personaContextKey is the context.Value key withPersona/PersonaFromContext
// share.
type personaContextKey struct{}

// PersonaFromContext returns the Persona PersonaMiddleware or
// WhagentPersonaMiddleware resolved for the current call, or "" if none
// has been resolved yet (e.g. called before either middleware ran).
func PersonaFromContext(ctx context.Context) Persona {
	persona, _ := ctx.Value(personaContextKey{}).(Persona)
	return persona
}

// withPersona returns a context carrying persona, for PersonaFromContext
// to read back.
func withPersona(ctx context.Context, persona Persona) context.Context {
	return context.WithValue(ctx, personaContextKey{}, persona)
}

// PersonaMiddleware is the mcp.Middleware every request passes through
// (wired in server.New): it resolves the persona the credential row
// recorded at mint time, carried on TokenInfo.Extra by libs/go/auth's
// TokenVerifier. A credential with no recorded persona (minted before
// personas existed) or an unrecognized one is rejected here and next is
// never invoked -- there is no default persona.
//
// Coexistence with the agent front door (whagent_auth.go): if a Persona
// is already on ctx when this middleware runs, WhagentPersonaMiddleware
// has already authenticated and resolved this call via the agent path,
// and this middleware must not try to re-resolve it.
func PersonaMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if PersonaFromContext(ctx) != "" {
				return next(ctx, method, req)
			}

			extra := req.GetExtra()
			if extra == nil || extra.TokenInfo == nil || extra.TokenInfo.UserID == "" {
				logger.WarnContext(ctx, "mcp call rejected: no caller credential resolved", "method", method)
				return nil, fmt.Errorf("unauthenticated: no caller credential resolved")
			}

			persona, ok := credentialPersona(extra.TokenInfo)
			if !ok {
				logger.WarnContext(ctx, "mcp call rejected: credential carries no recognized persona", "method", method)
				return nil, fmt.Errorf("forbidden: credential carries no persona")
			}
			return next(withPersona(ctx, persona), method, req)
		}
	}
}

// credentialPersona reads the persona a verified credential recorded at
// mint time. ok is false for a missing or unrecognized value.
func credentialPersona(info *sdkauth.TokenInfo) (Persona, bool) {
	raw, _ := info.Extra[auth.TokenInfoPersonaKey].(string)
	switch p := Persona(raw); p {
	case PersonaSwarmOperator, PersonaReader:
		return p, true
	}
	return "", false
}
