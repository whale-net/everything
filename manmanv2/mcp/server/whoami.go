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
// Tool tasks add their tools to the returned server and declare them in reg.
func NewServer(reg *Registry, audit Auditor) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "manmanv2-mcp", Version: "v0"}, nil)
	srv.AddReceivingMiddleware(Middleware(reg, audit))
	mcp.AddTool(srv, &mcp.Tool{Name: WhoamiTool.Name, Description: "Report the caller's subject and effective persona."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoamiOut, error) {
			c := CallerFromContext(ctx)
			return nil, whoamiOut{Subject: c.Subject, Persona: c.Persona.String()}, nil
		})
	return srv
}
