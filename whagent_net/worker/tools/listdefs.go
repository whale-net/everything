package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// ListToolDefinitions resolves FR8's "which tools may this turn's model
// call": it connects to every entry of toolSet (a fresh, per-server
// credential minted for each, FR10 -- never reused across servers, the
// same rule resolveTarget above follows) and aggregates each server's
// exposed tool set (mcp.ClientSession.ListTools) into the
// llm.ToolDefinition list CallModelInput.Tools carries (activities.go's
// ListToolDefinitions activity, whagent_net/worker). Order matches
// toolSet's own order. A ToolServerRef with a non-empty AllowedTools is
// further narrowed to just that subset (C22, allowlist.go's isAllowed) --
// the same rule resolveTarget enforces for a dispatched call, so a model
// is never offered a tool name Dispatch would then refuse.
//
// A connect/list failure against any one server fails the whole call
// (returns the first error encountered) rather than silently omitting
// that server's tools -- FR8's tool set must be complete, not a
// best-effort partial list a model could be misled by.
//
// FR8 also reserves SearchToolsName globally: if any configured server's
// own exposed catalog contains a tool literally named search_tools, this
// call fails loudly naming the offending server, before the AllowedTools
// filter runs -- a ref whose AllowedTools would have excluded that tool
// anyway does not get a pass, since the reservation is against the
// server's own catalog, not against what a model would end up seeing.
func ListToolDefinitions(ctx context.Context, issuer *persona.Issuer, sess *session.Session, agentID string, toolSet []session.ToolServerRef) ([]llm.ToolDefinition, error) {
	if issuer == nil {
		return nil, fmt.Errorf("tools: ListToolDefinitions: issuer is nil")
	}
	if sess == nil {
		return nil, fmt.Errorf("tools: ListToolDefinitions: sess is nil")
	}

	return candidateDefinitions(ctx, issuer, sess, agentID, toolSet)
}

// candidateDefinitions is the Candidates path every ListToolDefinitions
// caller resolves through: it connects to every entry of toolSet, lists
// each server's exposed tools, rejects FR8's reserved SearchToolsName
// wherever it appears in a server's own catalog, and narrows the rest to
// each ref's non-empty AllowedTools (C22, isAllowed) -- the one place that
// narrowing happens. ListToolDefinitions' bulk path above calls this
// directly; issue #2669's search-mode path (added on top of this scaffold)
// calls it too, to build the pool search-mode unlock names are matched
// against, so a name search_tools unlocked can never surface a tool
// AllowedTools would not otherwise permit (NFR2).
func candidateDefinitions(ctx context.Context, issuer *persona.Issuer, sess *session.Session, agentID string, toolSet []session.ToolServerRef) ([]llm.ToolDefinition, error) {
	var defs []llm.ToolDefinition
	for _, ref := range toolSet {
		token, err := mintCredential(ctx, issuer, sess, agentID, ref.ServerURL)
		if err != nil {
			return nil, fmt.Errorf("tools: mint credential for %s: %w", ref.ServerURL, err)
		}

		cs, err := Connect(ctx, ref.ServerURL, token)
		if err != nil {
			return nil, fmt.Errorf("tools: connect to %s: %w", ref.ServerURL, err)
		}

		res, err := cs.ListTools(ctx, nil)
		if err != nil {
			cs.Close()
			return nil, fmt.Errorf("tools: list tools on %s: %w", ref.ServerURL, err)
		}
		for _, t := range res.Tools {
			if t.Name == SearchToolsName {
				cs.Close()
				return nil, fmt.Errorf("tools: server %s exposes reserved tool name %q", ref.ServerURL, SearchToolsName)
			}
			if !isAllowed(t.Name, ref.AllowedTools) {
				continue
			}
			defs = append(defs, llm.ToolDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  toParameters(t.InputSchema),
			})
		}
		cs.Close()
	}
	return defs, nil
}

// toParameters converts an mcp.Tool.InputSchema value (from the client
// side, always the default JSON marshaling of the server's schema -- see
// that field's doc comment, "a map[string]any") into the
// map[string]any llm.ToolDefinition.Parameters expects. Handles the
// already-decoded map[string]any case directly; anything else (a typed
// schema value, json.RawMessage, or nil) round-trips through
// encoding/json rather than assuming a concrete Go type, since InputSchema
// is declared `any`. A nil/unconvertible schema yields a nil Parameters
// rather than an error -- a tool with no declared parameters is not
// malformed.
func toParameters(schema any) map[string]any {
	if schema == nil {
		return nil
	}
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
