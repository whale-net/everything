package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// ListToolDefinitions resolves FR8's "which tools may this turn's model
// call": it connects to every entry of toolSet (a fresh, per-server
// credential minted for each, FR10 -- never reused across servers, the
// same rule resolveTarget above follows) and aggregates each server's
// exposed tool set (mcp.ClientSession.ListTools) into the candidate pool
// (candidateDefinitions below) both branches below render from. A
// ToolServerRef with a non-empty AllowedTools is further narrowed to just
// that subset (C22, allowlist.go's isAllowed) -- the same rule
// resolveTarget enforces for a dispatched call, so a model is never
// offered a tool name Dispatch would then refuse.
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
//
// mode selects between the two shapes this function has returned since
// M4 (root plan #2602):
//
//   - mode != session.ToolLoadingModeSearch, including the zero value
//     (FR2): returns exactly the candidate pool, in toolSet order --
//     byte-for-byte what this function returned before M4. unlocked is
//     ignored entirely on this path.
//   - mode == session.ToolLoadingModeSearch (FR3/FR6/FR7): returns
//     search.SearchToolsDefinition() first, then, for each name in
//     unlocked in the given order, that name's definition from the
//     candidate pool. A name with no match in the current pool -- the
//     agent definition's ToolSet/AllowedTools narrowed between the turn
//     that unlocked it and this one -- is silently skipped (logged at
//     WARNING, never surfaced as an error): NFR2 guarantees the unlocked
//     set only ever intersects with what AllowedTools permits right now,
//     never widens it.
//
// Ordering guarantee (root-plan #2602 scope note, prompt-cache prefix
// stability): the search-mode Tools slice is built by appending in a
// single pass over unlocked, in the order UnlockedTools (activities.go)
// returned it -- never re-sorted, never re-filtered by any later state,
// and never assembled via a Go map's iteration order. `tools` renders
// first in the provider request, so any byte-level reordering across
// turns invalidates the whole request's cache prefix (symptom:
// cache_read_input_tokens stays at zero). Future changes to this
// function must preserve that: build the result by iterating unlocked in
// order and looking values up in a name-keyed map, never by iterating a
// map or re-deriving the order from the candidate pool.
func ListToolDefinitions(ctx context.Context, issuer *persona.Issuer, sess *session.Session, agentID string, toolSet []session.ToolServerRef, mode session.ToolLoadingMode, unlocked []string) ([]llm.ToolDefinition, error) {
	if issuer == nil {
		return nil, fmt.Errorf("tools: ListToolDefinitions: issuer is nil")
	}
	if sess == nil {
		return nil, fmt.Errorf("tools: ListToolDefinitions: sess is nil")
	}

	candidates, err := candidateDefinitions(ctx, issuer, sess, agentID, toolSet)
	if err != nil {
		return nil, err
	}

	if mode != session.ToolLoadingModeSearch {
		return candidates, nil
	}

	byName := make(map[string]llm.ToolDefinition, len(candidates))
	for _, d := range candidates {
		byName[d.Name] = d
	}

	defs := make([]llm.ToolDefinition, 0, len(unlocked)+1)
	defs = append(defs, SearchToolsDefinition())
	for _, name := range unlocked {
		d, ok := byName[name]
		if !ok {
			slog.WarnContext(ctx, "search-mode tool unlock has no matching candidate in the current tool set; skipping",
				"tool_name", name, "agent_id", agentID)
			continue
		}
		defs = append(defs, d)
	}
	return defs, nil
}

// Candidates is candidateDefinitions exported for SearchTools
// (activities.go) to call directly: an in-process search_tools call needs
// the exact same candidate pool ListToolDefinitions' own search branch
// matches unlocked names against -- the agent definition's tool_set
// narrowed by allowed_tools, never a server's raw, unfiltered catalog
// (FR4, NFR2) -- but resolves it on its own schedule (once per
// search_tools call, not once per turn ahead of CallModel), so it calls
// the shared helper directly rather than going through
// ListToolDefinitions' bulk/search branching.
func Candidates(ctx context.Context, issuer *persona.Issuer, sess *session.Session, agentID string, toolSet []session.ToolServerRef) ([]llm.ToolDefinition, error) {
	return candidateDefinitions(ctx, issuer, sess, agentID, toolSet)
}

// candidateDefinitions is the Candidates path every ListToolDefinitions
// caller resolves through: it connects to every entry of toolSet, lists
// each server's exposed tools, rejects FR8's reserved SearchToolsName
// wherever it appears in a server's own catalog, and narrows the rest to
// each ref's non-empty AllowedTools (C22, isAllowed) -- the one place that
// narrowing happens. ListToolDefinitions' bulk branch returns this slice
// unchanged (FR2); its search branch builds the pool search-mode unlock
// names are matched against from the same slice, so a name search_tools
// unlocked can never surface a tool AllowedTools would not otherwise
// permit (NFR2).
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
