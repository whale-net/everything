package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// TestSearchToolsNameLiteral guards against an accidental rename of the
// reserved constant -- ListToolDefinitions' enforcement (FR8) and any
// domain server's own tool naming both depend on this exact literal.
func TestSearchToolsNameLiteral(t *testing.T) {
	assert.Equal(t, "search_tools", tools.SearchToolsName)
}

// probeInput/probeOutput are a minimal, argument-free tool shape: these
// tests only care about tool *names* surfacing through ListToolDefinitions,
// never about actually invoking a tool.
type probeInput struct{}

type probeOutput struct{}

// newListDefsTestServer starts a plain, unauthenticated in-process MCP
// server exposing one no-op probe tool per name in names -- everything
// ListToolDefinitions needs to exercise FR8's reserved-name enforcement,
// without any of a real domain server's auth/store machinery.
func newListDefsTestServer(t *testing.T, names ...string) string {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "listdefs-test-server", Version: "0.1.0"}, nil)
	for _, name := range names {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: "probe tool " + name},
			func(_ context.Context, _ *mcp.CallToolRequest, _ probeInput) (*mcp.CallToolResult, probeOutput, error) {
				return nil, probeOutput{}, nil
			})
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// newListDefsTestFixture builds the whagent-net-side pieces
// ListToolDefinitions needs (a persona.Issuer over a throwaway signer, and a
// session) -- no ledger, no auth verification, since this file's fake
// servers have neither.
func newListDefsTestFixture(t *testing.T) (*persona.Issuer, *session.Session, string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, "https://whagent.example.test", "test-key-1")
	require.NoError(t, err)

	sess := &session.Session{
		SessionID: uuid.New(),
		Subject: session.Subject{
			Iss:  "https://keycloak.example.test/realms/humans",
			Sub:  "human-worker-actor-1",
			Kind: session.SubjectKindHuman,
		},
		OnBehalfOf: session.Subject{
			Iss:  "https://keycloak.example.test/realms/humans",
			Sub:  "human-listdefs-test-1",
			Kind: session.SubjectKindHuman,
		},
		AgentID: "listdefs-test-agent",
		Model:   "test-model",
		Status:  session.StatusRunning,
	}

	return persona.NewIssuer(signer), sess, sess.AgentID
}

// TestListToolDefinitions_ReservedNameRejected proves FR8's core case: a
// server exposing a tool literally named search_tools fails the whole call
// (naming that server) rather than silently including or omitting it.
func TestListToolDefinitions_ReservedNameRejected(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "other_tool", tools.SearchToolsName)
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeBulk, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), serverURL)
	assert.Contains(t, err.Error(), tools.SearchToolsName)
	assert.Empty(t, defs, "a reserved-name violation must not return a partial tool list")
}

// TestListToolDefinitions_ReservedNameRejected_EvenWhenAllowedToolsExcludesIt
// is the case a post-isAllowed check would wrongly let through: FR8 reserves
// the name against the server's own exposed catalog, not against what
// AllowedTools would have let the model see.
func TestListToolDefinitions_ReservedNameRejected_EvenWhenAllowedToolsExcludesIt(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "other_tool", tools.SearchToolsName)
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL, AllowedTools: []string{"other_tool"}}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeBulk, nil)

	require.Error(t, err, "AllowedTools excluding search_tools must not grant a pass")
	assert.Contains(t, err.Error(), serverURL)
	assert.Empty(t, defs)
}

// TestListToolDefinitions_ReservedNameOnSecondServer proves the reservation
// is enforced per-server across the whole tool set: a violation on the
// second server must fail the call outright, not silently merge the first
// server's already-collected tools into the result.
func TestListToolDefinitions_ReservedNameOnSecondServer(t *testing.T) {
	ctx := context.Background()
	firstURL := newListDefsTestServer(t, "first_tool")
	secondURL := newListDefsTestServer(t, tools.SearchToolsName)
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: firstURL}, {ServerURL: secondURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeBulk, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), secondURL)
	assert.Empty(t, defs, "must not silently merge the first server's tools when a later server violates FR8")
}

// TestMatch covers Match's entire matching algorithm (FR4, issue #2671's
// Testing phase): case-insensitive substring on name, on description, on
// both at once (the matched name is still reported exactly once), no match
// at all, an empty/whitespace-only query, a substring that is not a word
// boundary, a multi-word natural-language query matching on any one of its
// words, short filler words being ignored rather than over-matching, and
// that result order follows candidates' own order rather than match
// strength or alphabetical order.
func TestMatch(t *testing.T) {
	// Each candidate's name and description deliberately share no words
	// with any other candidate's, except where a case explicitly needs a
	// name/description overlap ("gadget") -- so a break in either half of
	// Match's "name OR description" check changes exactly the cases it
	// should and none of the others.
	candidates := []llm.ToolDefinition{
		{Name: "list_gadgets", Description: "Enumerate every entry in the catalog."},
		{Name: "purge_cache", Description: "Deletes stale gadget entries from cache."},
		{Name: "sync_gadgets", Description: "Refresh gadget records from upstream."},
		{Name: "list_schedules", Description: "Enumerate upcoming release plans."},
		{Name: "rename_folder", Description: "Rename a folder in the workspace."},
	}

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{
			// "gadgets" (plural) appears in list_gadgets/sync_gadgets' NAMES
			// only -- neither candidate's description contains it.
			name:  "case-insensitive hit on name",
			query: "GADGETS",
			want:  []string{"list_gadgets", "sync_gadgets"},
		},
		{
			// "stale" appears only in purge_cache's DESCRIPTION -- no
			// candidate's name contains it.
			name:  "case-insensitive hit on description",
			query: "STALE",
			want:  []string{"purge_cache"},
		},
		{
			// "gadget" (singular) hits list_gadgets by name, purge_cache by
			// description, and sync_gadgets by both -- each name reported
			// exactly once regardless of which half (or both) matched.
			name:  "hit on name only, description only, and both at once",
			query: "gadget",
			want:  []string{"list_gadgets", "purge_cache", "sync_gadgets"},
		},
		{
			name:  "no match returns an empty, non-nil slice",
			query: "nonexistent",
			want:  []string{},
		},
		{
			name:  "empty query returns no matches",
			query: "",
			want:  []string{},
		},
		{
			name:  "whitespace-only query returns no matches",
			query: "   ",
			want:  []string{},
		},
		{
			// "sched" is a mid-word substring of list_schedules' name only
			// ("SCHEDules") -- not a whole word in any candidate.
			name:  "substring match is not word-boundary limited",
			query: "sched",
			want:  []string{"list_schedules"},
		},
		{
			// A realistic natural-language query (the shape searchToolsDefinition
			// itself asks for): "list" hits list_gadgets/list_schedules by name,
			// "gadgets" additionally hits sync_gadgets by name -- "please"/"all"/
			// "now" hit nothing. Each candidate reported once even when more
			// than one of its words matches (list_gadgets matches both "list"
			// and "gadgets").
			name:  "multi-word query matches on any one word",
			query: "please list all gadgets now",
			want:  []string{"list_gadgets", "sync_gadgets", "list_schedules"},
		},
		{
			// Every word is shorter than minMatchWordLen (2 letters), so none
			// of them is searched on at all -- this must not degrade into
			// "match everything," which is what unfiltered short-word
			// substring matching would do (nearly every candidate contains
			// "a" or "of" somewhere).
			name:  "query of only short filler words matches nothing",
			query: "a to of",
			want:  []string{},
		},
		{
			// "to" (2 letters) is dropped as too short to search on; "gadget"
			// (6 letters) still matches exactly as it does on its own --
			// short filler words are ignored, not combined with real words
			// to broaden the match.
			name:  "short filler word alongside a real word is ignored",
			query: "to gadget",
			want:  []string{"list_gadgets", "purge_cache", "sync_gadgets"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tools.Match(candidates, tc.query)
			assert.Equal(t, tc.want, got, "result order must follow candidates' own order")
			assert.NotNil(t, got, "Match must never return a nil slice")
		})
	}
}

// TestCandidates_AllowedToolsExcludesEvenAMatchingQuery is FR4/NFR2: a tool
// a ToolServerRef's AllowedTools excludes is never a candidate at all, so a
// query that would otherwise match its name never surfaces it -- the
// exclusion happens before Match ever runs, not as a post-filter on its
// output.
func TestCandidates_AllowedToolsExcludesEvenAMatchingQuery(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "list_widgets", "delete_widgets")
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL, AllowedTools: []string{"list_widgets"}}}
	candidates, err := tools.Candidates(ctx, issuer, sess, agentID, toolSet)
	require.NoError(t, err)

	matched := tools.Match(candidates, "widgets")
	assert.Equal(t, []string{"list_widgets"}, matched, "delete_widgets is excluded by AllowedTools and must never be matched, even though its name matches the query")
}

// TestCandidates_SearchToolsNeverACandidate proves search_tools can never
// appear in its own search results: it is never a real domain server's tool
// (candidateDefinitions/Candidates rejects any server that tries to expose
// it, FR8), so a query that literally names it still matches nothing.
func TestCandidates_SearchToolsNeverACandidate(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "list_widgets", "delete_widgets")
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}
	candidates, err := tools.Candidates(ctx, issuer, sess, agentID, toolSet)
	require.NoError(t, err)

	matched := tools.Match(candidates, tools.SearchToolsName)
	assert.Empty(t, matched, "search_tools is never a candidate, so searching for its own name must match nothing")
}

// TestListToolDefinitions_SimilarNamesNotReserved proves the check is exact,
// not prefix or case-insensitive -- a domain server is free to use names
// that merely resemble the reserved one.
func TestListToolDefinitions_SimilarNamesNotReserved(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "search_tools_v2", "Search_Tools")
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeBulk, nil)

	require.NoError(t, err)
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	assert.ElementsMatch(t, []string{"search_tools_v2", "Search_Tools"}, names)
}
