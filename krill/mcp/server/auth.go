// Caller authentication and authorization for krill's spec-scoped MCP
// surface (FR10/NFR1, issue #2494) -- the human front door, via
// //libs/go/mcpauth's OAuth2-capable CredentialStore. See
// whagent_auth.go for the parallel agent front door and
// ../../ARCHITECTURE.md "The MCP spec surface" for the two-door design.
//
// NFR1 authorizes by **persona** (Swarm Operator / Requirement
// Contributor / Agent), never by individual identity -- there is no
// per-caller allow-list anywhere in this package. This file resolves a
// caller authenticated through the mcpauth front door to exactly one
// persona: PersonaSwarmOperator. That is not a scaffold shortcut to be
// widened casually -- krill/PRODUCT.md's Personas section is explicit
// that "The Requirement Contributor exists in the model and in
// permissions from M1, but has no unmediated path into krill until C12
// lands in M2", so M1's mcpauth door has no second human persona to
// distinguish yet. When C12 lands, this is the one place a real
// identity -> persona lookup replaces the constant below.
package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	// PersonaSwarmOperator is the admin persona -- today, every caller the
	// mcpauth (human OAuth2) front door authenticates resolves to this
	// persona; see this file's doc comment for why.
	PersonaSwarmOperator Persona = "swarm_operator"

	// PersonaRequirementContributor is reserved for when C12 (M2) gives
	// this persona its own unmediated path into krill (see this file's
	// doc comment) -- no code path produces it yet.
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
// (wired in server.New): it reads the caller identity the HTTP layer
// already verified via the mcpauth front door
// (req.GetExtra().TokenInfo, populated by transport.go's
// mcpauth.RequireBearerToken) and resolves it to PersonaSwarmOperator
// (see this file's doc comment for why that is the only persona this
// door produces in M1). A call with no resolved TokenInfo at all is
// rejected here and next is never invoked.
//
// Coexistence with the agent front door (whagent_auth.go): if a Persona
// is already on ctx when this middleware runs, WhagentPersonaMiddleware
// has already authenticated and resolved this call via the agent path,
// and this middleware must not try to re-resolve it -- see
// WhagentPersonaMiddleware's doc comment for the mounting order that
// guarantees this.
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

			return next(withPersona(ctx, PersonaSwarmOperator), method, req)
		}
	}
}
