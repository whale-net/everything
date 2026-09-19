package tools

import (
	"fmt"
	"strings"

	"github.com/whale-net/everything/whagent_net/llm"
)

// SearchToolsName is the reserved meta-tool name (FR8): no configured
// domain server may expose a real tool by this literal name, in any
// agent definition's tool set, bulk or search mode alike. The reservation
// is global, not search-mode-only, and is enforced once for every caller
// in ListToolDefinitions (listdefs.go) -- the shared per-server loop
// every agent definition's turn-1-and-later Tools resolution already
// goes through. whagent-net itself will later expose search_tools as an
// in-process meta-tool (#2669) for search-mode tool loading.
const SearchToolsName = "search_tools"

// searchToolsDefinition is SearchToolsDefinition's package-level literal:
// built once, at init, so every call to SearchToolsDefinition returns a
// byte-identical value -- required for the pinned, cache-stable render
// order a search-mode turn's Tools slice must hold (root plan #2602 scope
// note; see listdefs.go's ListToolDefinitions doc comment).
var searchToolsDefinition = llm.ToolDefinition{
	Name: SearchToolsName,
	Description: "Search the full catalog of tools available to this agent " +
		"and unlock any that match a natural-language query. A tool this " +
		"call matches becomes available to call directly for the rest of " +
		"this session -- it does not need to be searched for again. Call " +
		"this again later, with a different query, whenever the task's " +
		"needs change and none of the currently unlocked tools cover it.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "A natural-language description of the capability or tool needed right now.",
			},
		},
		"required": []string{"query"},
	},
}

// SearchToolsDefinition returns the reserved meta-tool's llm.ToolDefinition
// (FR3/FR4): a search-mode agent definition's turn-1 Tools is exactly this
// one definition (listdefs.go), and every later search-mode turn offers it
// alongside whatever has been unlocked so far (FR7). Always returns
// searchToolsDefinition's own value -- a struct of only value types
// (string/map[string]any built from literals), so two calls in the same
// process are byte-identical, and JSON-marshal identically, per this
// package's cache-stability requirement.
func SearchToolsDefinition() llm.ToolDefinition {
	return searchToolsDefinition
}

// Match returns the names of every candidate whose name or description
// contains query as a case-insensitive substring (FR4). This is the
// entire matching algorithm: no embeddings, no tokenization, no ranking
// -- the root plan's (#2602) explicit out-of-scope boundary for M4.
//
// Result order follows candidates' own order (which is tool_set order,
// post-allowed_tools, per candidateDefinitions in listdefs.go), so a
// given search's matched-name list is deterministic -- this is what
// ListToolDefinitions' pinned search-mode Tools order ultimately
// inherits (see that function's ordering-guarantee doc comment).
//
// An empty or whitespace-only query returns no matches, never every
// candidate -- a blank query is a caller error, not "match everything."
// A query that matches nothing returns an empty (non-nil) slice, never
// an error.
func Match(candidates []llm.ToolDefinition, query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return []string{}
	}

	matched := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Name), q) ||
			strings.Contains(strings.ToLower(c.Description), q) {
			matched = append(matched, c.Name)
		}
	}
	return matched
}

// RenderMatchResult renders a search_tools call's Match output into the
// tool_result event's model-readable payload content (FR4): "name:
// description" per matched name, in matched's own order, one per line, or
// an explicit "no tools matched" body on zero matches -- never an empty
// string, since a model must be able to tell "the search ran and found
// nothing" apart from a missing/truncated result. candidates supplies each
// matched name's description; callers pass the exact same candidate pool
// Match matched against.
func RenderMatchResult(matched []string, candidates []llm.ToolDefinition) string {
	if len(matched) == 0 {
		return "no tools matched"
	}

	byName := make(map[string]string, len(candidates))
	for _, c := range candidates {
		byName[c.Name] = c.Description
	}

	lines := make([]string, 0, len(matched))
	for _, name := range matched {
		lines = append(lines, fmt.Sprintf("%s: %s", name, byName[name]))
	}
	return strings.Join(lines, "\n")
}
