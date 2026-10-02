package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WhoamiTool is the pipeline-check tool: it returns the caller's subject and
// effective persona and makes no backend call.
var WhoamiTool = Tool{Name: "whoami", MinPersona: PersonaGamer}

type whoamiOut struct {
	Subject string `json:"subject"`
	Persona string `json:"persona"`
}

// NewServer builds the MCP server with persona gating and auditing installed.
// idem backs write-tool idempotency keys; it runs inside the persona
// middleware (caller known) and ahead of any confirmation middleware.
// Tool tasks add their tools to the returned server and declare them in reg.
func NewServer(reg *Registry, audit Auditor, idem IdempotencyStore) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "manmanv2-mcp", Version: "v0"}, nil)
	// One call: the first middleware is outermost, so persona runs before idempotency.
	mws := []mcp.Middleware{Middleware(reg, audit)}
	if idem != nil {
		mws = append(mws, Idempotency(reg, idem))
	}
	srv.AddReceivingMiddleware(mws...)
	mcp.AddTool(srv, &mcp.Tool{Name: WhoamiTool.Name, Description: "Report the caller's subject and effective persona."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoamiOut, error) {
			c := CallerFromContext(ctx)
			return nil, whoamiOut{Subject: c.Subject, Persona: c.Persona.String()}, nil
		})
	return srv
}
