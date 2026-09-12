package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/whagent"
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

// ListTools returns cs's server-exposed tools keyed by name (FR8) --
// callers apply their own ref-specific AllowedTools narrowing on top of
// this (C22, allowlist.go's isAllowed); ListTools itself has no
// ToolServerRef to filter against and returns the server's full exposed
// set verbatim. Returning the full *mcp.Tool (not just the name) also lets
// resolveTarget inspect a matched tool's InputSchema via
// acceptsIdempotencyKey below. See this package's doc comment (dispatch.go,
// "Tool selection") for the combined server + whagent-side filter.
func ListTools(ctx context.Context, cs *mcp.ClientSession) (map[string]*mcp.Tool, error) {
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools: list tools: %w", err)
	}
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, t := range res.Tools {
		byName[t.Name] = t
	}
	return byName, nil
}

// acceptsIdempotencyKey reports whether tool's InputSchema declares
// whagent.IdempotencyKeyArgument as a property -- the client-side signal
// that tool is one of the domain server's write tools expecting FR11's
// idempotency_key argument (see audience_score_system/mcp/server/
// registry.go's RegisterWrite and IdempotencyKeyed: only a write tool's
// input type implements that interface and gets the field in its
// generated schema). dispatch.go's Dispatch uses this to decide whether to
// attach the key at all -- a read tool's schema has no such property and,
// per its RegisterRead-generated schema's additionalProperties: false,
// rejects the call outright if an unexpected key is attached anyway.
// InputSchema arrives as whatever shape the transport decoded it into
// (typically map[string]any from JSON), so this round-trips through
// encoding/json rather than assuming a concrete Go type.
func acceptsIdempotencyKey(tool *mcp.Tool) bool {
	if tool == nil || tool.InputSchema == nil {
		return false
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return false
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return false
	}
	_, ok := schema.Properties[whagent.IdempotencyKeyArgument]
	return ok
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
