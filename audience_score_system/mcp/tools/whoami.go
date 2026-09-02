// Package tools holds Audience Score System's MCP tool groups, one file
// per group (see ../../ARCHITECTURE.md "MCP server"). This task (#1575)
// lands the registry (../server/registry.go) and zero product tools --
// whoami below is the wiring proof: a read tool with no channel_id
// argument that reports the Person server.PersonMiddleware resolved for
// the calling credential, proving AddTool -> registry -> auth middleware
// -> handler actually connects end to end. Product tools for C4-C7/C9/C10
// land in later M1 tasks.
package tools

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
)

// WhoamiOutput is the whoami tool's structured result: the caller's
// resolved identity.
type WhoamiOutput struct {
	PersonID    string `json:"person_id" jsonschema:"The caller's resolved Person ID"`
	Email       string `json:"email" jsonschema:"The caller's email address"`
	DisplayName string `json:"display_name" jsonschema:"The caller's display name"`
}

// RegisterWhoami registers the whoami tool via server.RegisterRead.
func RegisterWhoami(reg *server.Registry) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "whoami",
		Description: "Report the calling credential's resolved Person identity.",
	}, whoami)
}

// whoami takes no input (In = any) and no channel_id -- it doesn't
// implement server.ChannelScoped, so server.RegisterRead never
// Channel-scopes it; it exists purely to prove the auth wiring connects
// end to end. See server.PersonMiddleware (../server/auth.go) for how
// PersonFromContext gets populated ahead of this handler running.
func whoami(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, WhoamiOutput, error) {
	person := server.PersonFromContext(ctx)
	if person == nil {
		return nil, WhoamiOutput{}, errors.New("not implemented: no Person resolved on context")
	}

	return nil, WhoamiOutput{
		PersonID:    person.ID.String(),
		Email:       person.Email,
		DisplayName: person.DisplayName,
	}, nil
}
