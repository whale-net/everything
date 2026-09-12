package tools

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clientName/clientVersion identify worker's MCP client identity to a
// domain server (mcp.Implementation) -- informational only, not part of
// the tool contract.
const (
	clientName    = "whagent-net-worker"
	clientVersion = "0.1.0"
)

// bearerRoundTripper injects a bearer credential as an "Authorization:
// Bearer <token>" header on every request to one target server --
// mirrors audience_score_system/mcp/server's own test-side precedent
// (server_integration_test.go's bearerRoundTripper) for the production
// dispatch path. Never carries the caller's Keycloak token (ARCHITECTURE.md
// "Identity and auth chaining") -- callers of Connect must pass the
// per-server persona credential minted by keys.go's mintCredential, not
// any other bearer value.
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return base.RoundTrip(req)
}

// Connect opens a streamable-HTTP MCP client session against serverURL,
// authenticated with token (the per-server persona credential, FR10) --
// the caller's own Keycloak token must never be passed here (ARCHITECTURE.md
// "Identity and auth chaining"). The returned session's lifecycle (Close)
// is the caller's responsibility -- dispatch.go's Implementation-phase
// Dispatch is expected to open one short-lived session per call, mirroring
// the "mint per target server, never reuse across servers" rule this
// package's credential (keys.go) already follows.
func Connect(ctx context.Context, serverURL, token string) (*mcp.ClientSession, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint:   serverURL,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("tools: connect to %s: %w", serverURL, err)
	}
	return cs, nil
}

// ListToolNames returns the set of tool names cs's server exposes (FR8) --
// callers apply their own ref-specific AllowedTools narrowing on top of
// this (C22, allowlist.go's isAllowed); ListToolNames itself has no
// ToolServerRef to filter against and returns the server's full exposed
// set verbatim. See this package's doc comment (dispatch.go, "Tool
// selection") for the combined server + whagent-side filter.
func ListToolNames(ctx context.Context, cs *mcp.ClientSession) (map[string]struct{}, error) {
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools: list tools: %w", err)
	}
	names := make(map[string]struct{}, len(res.Tools))
	for _, t := range res.Tools {
		names[t.Name] = struct{}{}
	}
	return names, nil
}

// CallTool invokes name on cs's server with args, returning the domain
// server's raw *mcp.CallToolResult unchanged -- notably including its own
// IsError flag (FR2), which callers must commit as an ordinary tool-result
// transcript event, never reinterpret as a whagent-net failure (see
// dispatch.go's package doc comment).
func CallTool(ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, error) {
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("tools: call tool %q: %w", name, err)
	}
	return res, nil
}
