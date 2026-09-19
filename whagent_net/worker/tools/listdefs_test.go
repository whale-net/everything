package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// TestListToolDefinitions_Bulk_UnchangedFromPreM4Behavior is the FR2
// regression: a bulk (and, separately, an unset-mode) agent definition's
// Tools, over a multi-server, partly-allowlisted tool set, is exactly the
// candidate pool in toolSet order -- search_tools never appears, and the
// (ignored) unlocked argument has no effect at all.
func TestListToolDefinitions_Bulk_UnchangedFromPreM4Behavior(t *testing.T) {
	ctx := context.Background()
	firstURL := newListDefsTestServer(t, "alpha", "beta")
	secondURL := newListDefsTestServer(t, "gamma", "delta")
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{
		{ServerURL: firstURL, AllowedTools: []string{"alpha"}},
		{ServerURL: secondURL},
	}

	// The candidate pool's per-server tool ordering is whatever
	// candidateDefinitions (listdefs.go) collects from each server's own
	// ListTools call -- captured once here (bulk mode == the candidate pool
	// unchanged) and reused as the expectation below, rather than assuming
	// any particular order the MCP SDK itself is free to pick.
	want, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeBulk, nil)
	require.NoError(t, err)
	wantNames := make([]string, 0, len(want))
	for _, d := range want {
		wantNames = append(wantNames, d.Name)
	}
	require.Equal(t, []string{"alpha"}, wantNames[:1])
	require.ElementsMatch(t, []string{"gamma", "delta"}, wantNames[1:])

	for _, mode := range []session.ToolLoadingMode{session.ToolLoadingModeBulk, ""} {
		defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, mode, []string{"gamma"})
		require.NoError(t, err)

		names := make([]string, 0, len(defs))
		for _, d := range defs {
			names = append(names, d.Name)
		}
		assert.Equal(t, wantNames, names,
			"bulk (and unset-mode) Tools must be exactly the candidate pool in toolSet order, unaffected by unlocked")
		assert.NotContains(t, names, tools.SearchToolsName)
	}
}

// TestListToolDefinitions_Search_EmptyUnlocked_ReturnsOnlySearchTools is
// FR3: turn 1 of a search-mode session (empty unlocked) yields a Tools of
// exactly length 1, regardless of how large the candidate pool otherwise
// is.
func TestListToolDefinitions_Search_EmptyUnlocked_ReturnsOnlySearchTools(t *testing.T) {
	ctx := context.Background()
	names := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		names = append(names, "tool_"+string(rune('a'+i)))
	}
	serverURL := newListDefsTestServer(t, names...)
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeSearch, nil)

	require.NoError(t, err)
	require.Len(t, defs, 1)
	assert.Equal(t, tools.SearchToolsName, defs[0].Name)
}

// TestListToolDefinitions_Search_UnlockedToolsAppendedAfterSearchTools is
// FR7: a later search-mode turn with unlocked = [a, b] returns
// [search_tools, a, b] -- search_tools stays present even once tools have
// been unlocked.
func TestListToolDefinitions_Search_UnlockedToolsAppendedAfterSearchTools(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "a", "b", "c")
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeSearch, []string{"a", "b"})

	require.NoError(t, err)
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	assert.Equal(t, []string{tools.SearchToolsName, "a", "b"}, names)
}

// TestListToolDefinitions_Search_OrderPinned_DeterministicAcrossCallsAndPoolOrder
// is the ordering guarantee (root-plan #2602 scope note): the same
// unlocked set produces an identical Tools slice -- reflect.DeepEqual and
// identical JSON -- across repeated calls, and across two different
// candidate-pool iteration orders (here, two servers listed in opposite
// order across two toolSet values).
func TestListToolDefinitions_Search_OrderPinned_DeterministicAcrossCallsAndPoolOrder(t *testing.T) {
	ctx := context.Background()
	firstURL := newListDefsTestServer(t, "a", "b")
	secondURL := newListDefsTestServer(t, "c", "d")
	issuer, sess, agentID := newListDefsTestFixture(t)

	unlocked := []string{"d", "a", "c"}

	toolSetForward := []session.ToolServerRef{{ServerURL: firstURL}, {ServerURL: secondURL}}
	toolSetReversed := []session.ToolServerRef{{ServerURL: secondURL}, {ServerURL: firstURL}}

	defsForward1, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSetForward, session.ToolLoadingModeSearch, unlocked)
	require.NoError(t, err)
	defsForward2, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSetForward, session.ToolLoadingModeSearch, unlocked)
	require.NoError(t, err)
	defsReversed, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSetReversed, session.ToolLoadingModeSearch, unlocked)
	require.NoError(t, err)

	assert.Equal(t, defsForward1, defsForward2, "repeated calls with the same inputs must produce an identical Tools slice")
	assert.Equal(t, defsForward1, defsReversed, "the candidate pool's own iteration order must never affect the rendered order -- only unlocked's order does")

	jsonForward1, err := json.Marshal(defsForward1)
	require.NoError(t, err)
	jsonReversed, err := json.Marshal(defsReversed)
	require.NoError(t, err)
	assert.JSONEq(t, string(jsonForward1), string(jsonReversed))
	assert.Equal(t, jsonForward1, jsonReversed, "byte-level JSON output must match exactly, not just be JSON-equivalent -- this is the prompt-cache prefix-stability guarantee")

	names := make([]string, 0, len(defsForward1))
	for _, d := range defsForward1 {
		names = append(names, d.Name)
	}
	assert.Equal(t, []string{tools.SearchToolsName, "d", "a", "c"}, names, "search_tools first, then unlocked in exactly the given order")
}

// TestListToolDefinitions_Search_UnlockedNameNotInAllowedTools_SilentlySkipped
// is half of NFR2: a name in unlocked that the ref's AllowedTools does not
// currently permit is not offered, and the call does not error -- the
// unlocked set only ever intersects with what AllowedTools permits right
// now, it never widens it.
func TestListToolDefinitions_Search_UnlockedNameNotInAllowedTools_SilentlySkipped(t *testing.T) {
	ctx := context.Background()
	serverURL := newListDefsTestServer(t, "a", "b")
	issuer, sess, agentID := newListDefsTestFixture(t)

	// AllowedTools now only permits "a" -- "b" was unlocked earlier, before
	// AllowedTools narrowed.
	toolSet := []session.ToolServerRef{{ServerURL: serverURL, AllowedTools: []string{"a"}}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeSearch, []string{"a", "b"})

	require.NoError(t, err, "a no-longer-permitted unlocked name must be silently skipped, never surfaced as an error")
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	assert.Equal(t, []string{tools.SearchToolsName, "a"}, names)
}

// TestListToolDefinitions_Search_UnlockedNameFromRemovedServer_SilentlySkipped
// is the other half of NFR2: a name unlocked from a server later removed
// from tool_set entirely is not offered, without erroring.
func TestListToolDefinitions_Search_UnlockedNameFromRemovedServer_SilentlySkipped(t *testing.T) {
	ctx := context.Background()
	remainingURL := newListDefsTestServer(t, "a")
	issuer, sess, agentID := newListDefsTestFixture(t)

	// "gone" was unlocked from a server that tool_set no longer includes.
	toolSet := []session.ToolServerRef{{ServerURL: remainingURL}}
	defs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeSearch, []string{"gone", "a"})

	require.NoError(t, err)
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	assert.Equal(t, []string{tools.SearchToolsName, "a"}, names)
}

// TestListToolDefinitions_Search_NeverOffersWhatBulkWouldNotHave is NFR2's
// composition check: for a fixture tool set and an unlocked set that
// includes names bulk mode itself would never have exposed (excluded by
// AllowedTools), search mode's non-search_tools definitions are always a
// subset of what bulk mode returns for the same agent definition.
func TestListToolDefinitions_Search_NeverOffersWhatBulkWouldNotHave(t *testing.T) {
	ctx := context.Background()
	firstURL := newListDefsTestServer(t, "a", "b", "c")
	secondURL := newListDefsTestServer(t, "d", "e")
	issuer, sess, agentID := newListDefsTestFixture(t)

	toolSet := []session.ToolServerRef{
		{ServerURL: firstURL, AllowedTools: []string{"a", "b"}},
		{ServerURL: secondURL, AllowedTools: []string{"d"}},
	}

	bulkDefs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeBulk, nil)
	require.NoError(t, err)
	bulkNames := make(map[string]bool, len(bulkDefs))
	for _, d := range bulkDefs {
		bulkNames[d.Name] = true
	}

	// c/e were never in AllowedTools; unlocking them anyway (an
	// AllowedTools narrowing between unlock and this turn) must not surface
	// them here even though search mode is asked for them explicitly.
	searchDefs, err := tools.ListToolDefinitions(ctx, issuer, sess, agentID, toolSet, session.ToolLoadingModeSearch, []string{"a", "c", "d", "e"})
	require.NoError(t, err)

	for _, d := range searchDefs {
		if d.Name == tools.SearchToolsName {
			continue
		}
		assert.True(t, bulkNames[d.Name], "search mode returned %q, which bulk mode would not have -- NFR2 violated", d.Name)
	}
}
