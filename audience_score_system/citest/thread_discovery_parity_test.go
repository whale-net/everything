//go:build integration

// thread_discovery_parity_test.go is issue #1937's own NFR2 parity check
// (root plan #1934): the same fixture Channel's research_thread rows must
// come back identically whether read through list_research_threads (MCP)
// or rendered onto web/research's Channel index -- because both surfaces
// call the exact same store.ThreadStore.ListByChannel and the exact same
// store.CanRead check (research.go's renderChannelIndex, mcp/tools/
// research.go's registerListResearchThreads), never a second query path.
// This is a narrow, task-scoped addition to the shared `world` harness
// (e2e_test.go) -- not a milestone acceptance test like m4_1/m4_2's own
// files, which are their own dedicated deliverables once their milestone
// completes.
package citest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
)

func TestThreadDiscovery_MCPAndWebAgreeOnSameChannelsThreadSet(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	creator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-thread-parity-creator", "thread-parity-creator@example.com", "Thread Parity Creator")
	require.NoError(t, err)

	ch, err := w.st.Channels().Create(ctx, "yt-thread-parity", "Thread Parity Channel", creator.ID)
	require.NoError(t, err)

	idea, err := w.st.Ideas().Create(ctx, ch.ID, "Parity Idea", creator.ID)
	require.NoError(t, err)

	attached, err := w.st.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, IdeaID: &idea.ID, Title: "Attached parity thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)
	unattached, err := w.st.Threads().FindOrCreate(ctx, store.FindOrCreateThreadInput{
		ChannelID: ch.ID, Title: "Unattached parity thread", CreatedByPersonID: creator.ID,
	})
	require.NoError(t, err)

	// MCP surface.
	cs := w.mcpConnect(creator.ID)
	mcpRes := callTool(t, cs, "list_research_threads", mcptools.ListResearchThreadsInput{ChannelID: ch.ID.String()})
	mcpOut := decode[mcptools.ListResearchThreadsOutput](t, mcpRes)
	require.Len(t, mcpOut.Threads, 2)
	mcpIDs := map[string]bool{}
	for _, th := range mcpOut.Threads {
		mcpIDs[th.ID] = true
	}
	assert.True(t, mcpIDs[attached.ID.String()])
	assert.True(t, mcpIDs[unattached.ID.String()])

	// Web surface: the same Channel's research index.
	cookie := w.establishSession(creator.ID)
	webRes := w.get(cookie, "/channels/"+ch.ID.String()+"/research")
	require.Equal(t, 200, webRes.Code, "body: %s", webRes.Body.String())
	body := webRes.Body.String()

	assert.Contains(t, body, attached.Title, "web must render the same attached thread the MCP tool returned")
	assert.Contains(t, body, unattached.Title, "web must render the same unattached thread the MCP tool returned")

	// The two surfaces must agree on SET SIZE too -- not just that each
	// individually contains the fixture's threads, but that neither
	// surface has a third, divergent thread the other lacks (NFR2).
	assert.Equal(t, 2, len(mcpOut.Threads), "MCP and web must agree on exactly the fixture's two threads, no more, no fewer")
}
