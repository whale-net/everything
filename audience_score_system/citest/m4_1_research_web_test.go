//go:build integration

// Package citest_test (M4.1 milestone half): the milestone acceptance test
// for M4.1 (`loop-1-web-save-browse`, root plan #1896, this task issue
// #1903) -- one Channel driven through *save a research note on `web` ->
// save a verdict on `web` -> browse both on `web` -> read the identical
// rows back over real MCP* end to end, proving M4.1's outcome sentence
// itself ("a Creator or Analyst can save and browse Loop 1 entirely from
// the web UI"), not any one task's own slice, each of which already has
// its own *_integration_test.go (web/research/research_integration_test.go
// in particular). Modelled directly on m3_calibration_test.go and
// e2e_test.go's own doc-comment/harness conventions: this file reuses the
// package's shared `world` (e2e_test.go), its mcpConnect/callTool/decode
// helpers, and e2e_test.go's own real-web-router mounting -- newWorld now
// also mounts web/research.New(st) alongside web/schedule, web/invite, and
// web/access, exactly like #1894's own precedent of wiring RegisterOutcomeBar
// into the shared world for its own milestone file.
//
// The load-bearing claim only this file can make is the plan's fourth user
// story: a note saved on `web` and a note saved via `save_research_note`
// are indistinguishable in storage and browsing -- same store, same
// rules, no second-class record. Phase 2 asserts this in the web-write/
// MCP-read direction; phase 3 asserts the mirror, MCP-write/web-read.
//
// # Deviation from this issue's own Testing section: no get_research_note
//
// This task's issue body names a `get_research_note` MCP tool for phase
// 2's read-back; no such tool exists in mcp/tools/research.go (only
// save_research_note, list_research_notes, create_idea, and list_ideas are
// registered -- see RegisterResearch). Phase 2 below reads back via
// list_research_notes scoped to the Idea instead, matching the saved note
// by its ID among the returned rows -- the identical read surface
// list_research_notes already proves elsewhere in this package (e.g.
// e2e_test.go step 3), and the only research-note read tool that actually
// exists. Every field the (non-existent) get_research_note would have
// exposed -- text, author, source_url, cited -- is asserted from that row.
//
// # FR/NFR coverage inventory
//
//   - FR1 (Channel research index: Idea list with note count/verdict
//     presence, plus a separate unattached-notes section) -- phase 3 (Idea
//     list) and phase 6 (unattached section).
//   - FR2 (Idea detail page: note list + current/history verdict, the
//     identical pair of calls get_viability_verdict makes) -- phase 3 and
//     phase 5.
//   - FR3 (save-research-note form/POST, calling the identical
//     store.ResearchStore.SaveNote save_research_note's mutate step calls)
//     -- phase 2.
//   - FR4 (save-viability-verdict form/POST, calling the identical
//     store.VerdictStore.Append save_viability_verdict's mutate step
//     calls) -- phase 4.
//   - FR5 (viability_verdict.source: web writes human, MCP writes agent,
//     both surfaced) -- phase 4 (source values) and phase 5 (source
//     labels rendered in the correct oldest-to-newest order).
//   - FR6 (server-generated idempotency key on both save forms, reusing
//     SaveNote/Append's existing dedupe) -- phase 9.
//   - FR7 (both POST routes re-derive store.CanWrite fresh from Postgres;
//     a forged POST from a non-member is rejected with nothing written)
//     -- phase 8.
//   - FR8 (Channel-detail page links to the research index) -- phase 7.
//   - FR9 (Idea detail renders every verdict version, oldest-to-newest,
//     each with its source) -- phase 5.
//   - FR10 (every rendered note shows an explicit cited/uncited indicator
//     read from store.ResearchNote.Cited()) -- phase 3.
//   - FR11 (ARCHITECTURE.md NFR3 amendment) and FR12 (validateSourceURL/
//     Cited moved into store) -- doc-only / already exercised by store's
//     own tests; not re-proven here (FR12's derivation IS exercised
//     indirectly by phase 3's cited/uncited assertions, which read
//     ResearchNoteOutput.Cited -- the same field toResearchNoteOutput sets
//     from n.Cited()).
//   - NFR1 (every write path here satisfies LB4's idempotency contract,
//     no session-held state) -- phase 9.
//   - NFR2 (both browse views default to the same 50-row page as
//     list_research_notes/list_ideas, no "load more" control) -- phase
//     10, cross-checked against list_research_notes' own truncated flag.
//   - NFR3 (authorization re-derived server-side on every request, never
//     a client-supplied flag) -- phase 8.
//   - NFR4 (FR5's migration backfills existing rows to source='agent'
//     deterministically) -- migration-level, not exercised here; see
//     migrate/schema's own migration test.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/components"
	"github.com/whale-net/everything/audience_score_system/web/pages"
)

// renderChannelDetail renders web/pages.ChannelDetail directly -- the
// EXACT templ component web/main.go's handleChannelDetail (package main,
// not importable from this package, mirroring e2e_test.go's own GET
// /channels and GET /my-work rationale) renders for GET /channels/{id} --
// so phase 7's FR8 nav-link assertion checks the genuine production
// template rather than a second copy of its markup. canReconnect/canInvite
// are computed the identical way that handler computes them
// (store.CanReconnect/store.CanInvite).
func (w *world) renderChannelDetail(t *testing.T, ch store.Channel, viewer store.Person) string {
	t.Helper()
	ctx := w.ctx

	canReconnect, err := store.CanReconnect(ctx, w.st.Roles(), ch.ID, viewer.ID)
	require.NoError(t, err)
	canInvite, err := store.CanInvite(ctx, w.st.Roles(), ch.ID, viewer.ID)
	require.NoError(t, err)

	data := components.LayoutData{Title: ch.Title, User: &viewer}
	req := httptest.NewRequest(http.MethodGet, "/channels/"+ch.ID.String(), nil)
	rec := httptest.NewRecorder()
	// activity (#2038, FR23-FR26) is a zero-value placeholder here too,
	// mirroring handleChannelDetail's own Scaffold-phase placeholder --
	// Implementation replaces both with the real
	// store.Dashboard().ChannelActivity read.
	require.NoError(t, components.Render(rec, req, ch.Title, pages.ChannelDetail(data, ch, canReconnect, canInvite, store.ChannelActivity{})))
	return rec.Body.String()
}

// TestE2E_M41_ResearchWebSaveBrowse is M4.1's own acceptance test (issue
// #1903, milestone loop-1-web-save-browse): one Channel, one Idea, driven
// through web saves, MCP saves, and cross-surface reads in both
// directions, sharing this function's state across every phase in order
// -- mirrored from TestE2E_M3_CalibrationTrend's own sequential, state-
// sharing t.Run structure.
func TestE2E_M41_ResearchWebSaveBrowse(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	creator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m41-creator", "m41-creator@example.com", "M4.1 Creator")
	require.NoError(t, err)
	coCreator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m41-cocreator", "m41-cocreator@example.com", "M4.1 Co-Creator")
	require.NoError(t, err)
	analyst, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m41-analyst", "m41-analyst@example.com", "M4.1 Analyst")
	require.NoError(t, err)
	outsider, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-m41-outsider", "m41-outsider@example.com", "M4.1 Outsider")
	require.NoError(t, err)

	ch, err := w.st.Channels().Create(ctx, "yt-m41-channel", "M4.1 Research Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, coCreator.ID, store.RoleCoCreator, creator.ID))
	require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	creatorCookie := w.establishSession(creator.ID)
	analystCookie := w.establishSession(analyst.ID)
	outsiderCookie := w.establishSession(outsider.ID)

	csCreator := w.mcpConnect(creator.ID)
	csAnalyst := w.mcpConnect(analyst.ID)

	ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
		ChannelID: ch.ID.String(), Title: "M4.1 Loop 1 Idea",
	}))
	ideaID := uuid.MustParse(ideaOut.IdeaID)

	const (
		webNoteText   = "Web-saved: competitor analysis shows a 3x lift"
		webNoteSource = "https://example.com/m41-web-source"
	)
	webNoteKey := uuid.NewString()
	webNoteForm := url.Values{
		"idempotency_key": {webNoteKey},
		"text":            {webNoteText},
		"source_url":      {webNoteSource},
		"idea_id":         {ideaID.String()},
		"thread_title":    {"Research"},
	}
	var noteWebID string

	// ── 2: FR3 -- web write, MCP read (the story-4 assertion, direction 1) ──
	t.Run("2_fr3_web_write_mcp_read", func(t *testing.T) {
		rec := w.postForm(analystCookie, "/channels/"+ch.ID.String()+"/research/notes", webNoteForm)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String(), rec.Header().Get("Location"))

		list := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID.String(),
		}))
		require.Len(t, list.Notes, 1, "the web-saved note must be the only note on this Idea so far")
		got := list.Notes[0]
		noteWebID = got.ID

		assert.Equal(t, webNoteText, got.Text, "the note saved on web must read back byte-for-byte identical over MCP")
		assert.Equal(t, analyst.ID.String(), got.AuthorPersonID, "the author is the signed-in web caller, not the Channel's Creator")
		require.NotNil(t, got.SourceURL)
		assert.Equal(t, webNoteSource, *got.SourceURL)
		assert.True(t, got.Cited, "FR10: a note with a source_url renders cited -- indistinguishable from an agent-written cited note")
	})

	const (
		citedMCPText   = "MCP-saved cited: analytics dashboard export"
		citedMCPSource = "https://example.com/m41-mcp-source"
		uncitedMCPText = "MCP-saved uncited: gut feeling"
		unattachedText = "MCP-saved unattached note, no Idea"
	)
	var citedMCPID string

	// ── 3: FR1, FR2, FR10 -- MCP write, web read (the mirror, direction 2) ──
	t.Run("3_fr1_fr2_fr10_mcp_write_web_read", func(t *testing.T) {
		cited := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID.String(), IdeaID: ideaID.String(), Text: citedMCPText, SourceURL: citedMCPSource, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.True(t, cited.Cited)
		citedMCPID = cited.ID

		uncited := decode[mcptools.ResearchNoteOutput](t, callTool(t, csCreator, "save_research_note", mcptools.SaveResearchNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID.String(), IdeaID: ideaID.String(), Text: uncitedMCPText, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.False(t, uncited.Cited)

		// FR1: the Channel research index shows this Idea with its note
		// count and no verdict yet.
		indexRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research")
		require.Equal(t, http.StatusOK, indexRec.Code, "body: %s", indexRec.Body.String())
		indexBody := indexRec.Body.String()
		assert.Contains(t, indexBody, ideaOut.Title)
		assert.Regexp(t, `M4\.1 Loop 1 Idea[\s\S]{0,400}>3<`, indexBody, "the Idea's note count (3: 1 web + 2 mcp) must render on the index")
		assert.Contains(t, indexBody, `title="No verdict yet"`, "no verdict has been recorded for this Idea yet (FR31/FR32 glyph, not the old \"None yet\" badge text)")

		// FR2, FR10: the Idea detail page shows all three notes, each with
		// the correct cited/uncited indicator -- never a local nil-check,
		// only store.ResearchNote.Cited() (see views.templ's citedBadge).
		detailRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String())
		require.Equal(t, http.StatusOK, detailRec.Code, "body: %s", detailRec.Body.String())
		detailBody := detailRec.Body.String()
		assert.Contains(t, detailBody, webNoteText)
		assert.Contains(t, detailBody, citedMCPText)
		assert.Contains(t, detailBody, uncitedMCPText)
		assert.Contains(t, detailBody, webNoteSource, "a cited note's source_url renders as a link")
		assert.Contains(t, detailBody, citedMCPSource)
		assert.Contains(t, detailBody, "Cited")
		assert.Contains(t, detailBody, "Uncited")
	})

	const verdictReasoningHuman = "Web-recorded: strong evidence to proceed"
	verdictKey := uuid.NewString()
	verdictForm := url.Values{
		"idempotency_key": {verdictKey},
		"verdict":         {string(store.VerdictViable)},
		"reasoning":       {verdictReasoningHuman},
		"cited_note_ids":  {noteWebID},
	}
	var verdict1ID string
	const verdictReasoningAgent = "Agent-appended: additional confirmation from a second source"
	var verdict2ID string

	// ── 4: FR4, FR5 -- web verdict, MCP read; then an MCP-appended second
	// version (append-only, cross-surface) ──────────────────────────────
	t.Run("4_fr4_fr5_web_verdict_mcp_read_then_mcp_append", func(t *testing.T) {
		rec := w.postForm(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/verdicts", verdictForm)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		assert.Equal(t, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String(), rec.Header().Get("Location"))

		got1 := decode[mcptools.GetViabilityVerdictOutput](t, callTool(t, csAnalyst, "get_viability_verdict", mcptools.GetViabilityVerdictInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID.String(),
		}))
		require.NotNil(t, got1.Current)
		assert.Equal(t, 1, got1.Current.Version)
		assert.Equal(t, string(store.VerdictViable), got1.Current.Verdict)
		assert.Equal(t, verdictReasoningHuman, got1.Current.Reasoning)
		assert.Equal(t, string(store.VerdictSourceHuman), got1.Current.Source, "FR5: a verdict saved through the web form is source=human")
		require.Len(t, got1.Current.CitedResearchNotes, 1)
		assert.Equal(t, noteWebID, got1.Current.CitedResearchNotes[0].ID)
		verdict1ID = got1.Current.ID

		v2 := decode[mcptools.VerdictOutput](t, callTool(t, csAnalyst, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID.String(), Verdict: string(store.VerdictViable),
			Reasoning: verdictReasoningAgent, CitedResearchNoteIDs: []string{citedMCPID}, IdempotencyKeyArg: uuid.NewString(),
		}))
		assert.Equal(t, string(store.VerdictSourceAgent), v2.Source, "FR5: MCP's save_viability_verdict always writes source=agent")
		assert.Equal(t, 2, v2.Version)
		verdict2ID = v2.ID

		got2 := decode[mcptools.GetViabilityVerdictOutput](t, callTool(t, csAnalyst, "get_viability_verdict", mcptools.GetViabilityVerdictInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID.String(),
		}))
		require.NotNil(t, got2.Current)
		assert.Equal(t, verdict2ID, got2.Current.ID, "current is now the MCP-appended version")
		assert.Equal(t, string(store.VerdictSourceAgent), got2.Current.Source)
		require.Len(t, got2.History, 2, "M1 FR12 append-only: the human version is never overwritten, only superseded as current")
		assert.Equal(t, verdict1ID, got2.History[0].ID, "history is oldest-to-newest")
		assert.Equal(t, verdictReasoningHuman, got2.History[0].Reasoning, "the first version's own fields must be byte-for-byte unchanged")
		assert.Equal(t, string(store.VerdictSourceHuman), got2.History[0].Source)
		assert.Equal(t, verdict2ID, got2.History[1].ID)
	})

	// ── 5: FR2, FR4, FR9, FR5 -- browse agreement, split across the two
	// pages #2034 introduced: the Idea page renders the CURRENT verdict
	// ONLY (never the superseded reasoning inline), while the
	// verdict-details page (GET .../verdicts) owns the full version
	// count/ordering/source cross-check against get_viability_verdict's
	// own history ─────────────────────────────────────────────────────────
	t.Run("5_fr2_fr9_fr5_browse_agreement", func(t *testing.T) {
		// FR4: the Idea page shows only the current (highest-version,
		// agent-sourced) verdict -- its reasoning and source render, but
		// the superseded human-sourced version's reasoning must not appear
		// inline anymore.
		ideaRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String())
		require.Equal(t, http.StatusOK, ideaRec.Code, "body: %s", ideaRec.Body.String())
		ideaBody := ideaRec.Body.String()

		assert.Contains(t, ideaBody, verdictReasoningAgent, "the Idea page must show the CURRENT verdict's reasoning")
		assert.Contains(t, ideaBody, "Agent", "the current verdict's source label must render")
		assert.NotContains(t, ideaBody, verdictReasoningHuman, "FR4: the Idea page no longer inlines superseded verdict versions")
		assert.Contains(t, ideaBody, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/verdicts", "the Idea page links out to the verdict-details page for history")

		// The verdict-details page's default view (no ?version) resolves
		// to the SAME current verdict -- same reasoning, same source, and
		// still no superseded reasoning rendered inline.
		verdictsRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/verdicts")
		require.Equal(t, http.StatusOK, verdictsRec.Code, "body: %s", verdictsRec.Body.String())
		verdictsBody := verdictsRec.Body.String()

		assert.Contains(t, verdictsBody, "Current")
		assert.Contains(t, verdictsBody, verdictReasoningAgent)
		assert.NotContains(t, verdictsBody, verdictReasoningHuman, "the default (current) view must not also render the older version's reasoning")

		// FR7's version-select lists every version, oldest-to-newest --
		// matching get_viability_verdict's own len(History) == 2 exactly,
		// never drifting from it.
		v1Idx := strings.Index(verdictsBody, "Version 1")
		v2Idx := strings.Index(verdictsBody, "Version 2")
		require.Greater(t, v1Idx, 0, "the version select must list Version 1")
		require.Greater(t, v2Idx, 0, "the version select must list Version 2")
		assert.Less(t, v1Idx, v2Idx, "the version select lists versions oldest-to-newest")

		// FR7: selecting the older version (?version=1) renders ITS OWN
		// reasoning and source -- the human-sourced v1 -- and not the
		// current (agent-sourced) version's reasoning.
		v1Rec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/verdicts?version=1")
		require.Equal(t, http.StatusOK, v1Rec.Code, "body: %s", v1Rec.Body.String())
		v1Body := v1Rec.Body.String()

		assert.Contains(t, v1Body, verdictReasoningHuman, "?version=1 must render the human-sourced first version's reasoning")
		assert.Contains(t, v1Body, "Human")
		assert.NotContains(t, v1Body, verdictReasoningAgent, "selecting an older version must not also render the current version's reasoning")
		assert.Contains(t, v1Body, "Version 1")
		assert.NotContains(t, v1Body, "Current", "version 1 is not current, so its card must be labeled by version number, not \"Current\"")
	})

	// ── 6: FR1 -- unattached notes: an MCP-saved note with no Idea lists
	// in the Channel index's unattached section, not under any Idea ──────
	t.Run("6_fr1_unattached_notes", func(t *testing.T) {
		unattached := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{ThreadTitle: "Research",
			ChannelID: ch.ID.String(), Text: unattachedText, IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Nil(t, unattached.IdeaID)

		rec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research")
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := rec.Body.String()

		sectionIdx := strings.Index(body, "Unattached notes")
		require.Greater(t, sectionIdx, 0, "the unattached-notes section heading must render")
		noteIdx := strings.Index(body, unattachedText)
		require.Greater(t, noteIdx, 0)
		assert.Greater(t, noteIdx, sectionIdx, "the unattached note must render inside the unattached-notes section, not under the Idea's own row")
	})

	// ── 7: FR8 -- the Channel-detail page links to the research index ───
	t.Run("7_fr8_channel_detail_links_to_research", func(t *testing.T) {
		body := w.renderChannelDetail(t, ch, creator)
		assert.Contains(t, body, `href="/channels/`+ch.ID.String()+`/research"`)
	})

	// ── 7b: FR22 -- the Channel-detail page's scripts link targets the
	// renamed /scripts path, not the retired /schedule path ─────────────
	t.Run("7b_fr22_channel_detail_links_to_scripts", func(t *testing.T) {
		body := w.renderChannelDetail(t, ch, creator)
		assert.Contains(t, body, `href="/channels/`+ch.ID.String()+`/scripts"`)
		assert.NotContains(t, body, `/channels/`+ch.ID.String()+`/schedule"`)
	})

	// ── 8: FR7, NFR3 -- a Person with no role on the Channel is rejected
	// by both GET routes and both POST routes; nothing is written ────────
	t.Run("8_fr7_nfr3_outsider_rejected_no_write", func(t *testing.T) {
		beforeNotes := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{ChannelID: ch.ID.String()}))
		beforeVerdict := decode[mcptools.GetViabilityVerdictOutput](t, callTool(t, csCreator, "get_viability_verdict", mcptools.GetViabilityVerdictInput{ChannelID: ch.ID.String(), IdeaID: ideaID.String()}))

		getIndex := w.get(outsiderCookie, "/channels/"+ch.ID.String()+"/research")
		assert.Equal(t, http.StatusForbidden, getIndex.Code)

		getDetail := w.get(outsiderCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String())
		assert.Equal(t, http.StatusForbidden, getDetail.Code)

		postNote := w.postForm(outsiderCookie, "/channels/"+ch.ID.String()+"/research/notes", url.Values{
			"idempotency_key": {uuid.NewString()}, "text": {"forged note"},
		})
		assert.Equal(t, http.StatusForbidden, postNote.Code, "body: %s", postNote.Body.String())
		assert.NotContains(t, postNote.Body.String(), "forged note")

		postVerdict := w.postForm(outsiderCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/verdicts", url.Values{
			"idempotency_key": {uuid.NewString()}, "verdict": {string(store.VerdictViable)}, "reasoning": {"forged verdict"},
		})
		assert.Equal(t, http.StatusForbidden, postVerdict.Code, "body: %s", postVerdict.Body.String())

		afterNotes := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{ChannelID: ch.ID.String()}))
		afterVerdict := decode[mcptools.GetViabilityVerdictOutput](t, callTool(t, csCreator, "get_viability_verdict", mcptools.GetViabilityVerdictInput{ChannelID: ch.ID.String(), IdeaID: ideaID.String()}))
		assert.Equal(t, len(beforeNotes.Notes), len(afterNotes.Notes), "the outsider's rejected POSTs must not have created a note")
		assert.Equal(t, len(beforeVerdict.History), len(afterVerdict.History), "the outsider's rejected POST must not have appended a verdict version")
	})

	// ── 9: FR6, NFR1 -- replaying the exact note POST and the exact
	// verdict POST with their original idempotency_key creates no
	// duplicate note and no extra verdict version, verified via MCP ─────
	t.Run("9_fr6_nfr1_idempotent_replay_no_duplicates", func(t *testing.T) {
		notesBefore := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{ChannelID: ch.ID.String(), IdeaID: ideaID.String()}))

		replayNote := w.postForm(analystCookie, "/channels/"+ch.ID.String()+"/research/notes", webNoteForm)
		assert.Equal(t, http.StatusSeeOther, replayNote.Code, "a replayed submit must still redirect, not error, body: %s", replayNote.Body.String())

		notesAfter := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{ChannelID: ch.ID.String(), IdeaID: ideaID.String()}))
		assert.Equal(t, len(notesBefore.Notes), len(notesAfter.Notes), "replaying the identical idempotency_key must not create a duplicate note")

		replayVerdict := w.postForm(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID.String()+"/verdicts", verdictForm)
		assert.Equal(t, http.StatusSeeOther, replayVerdict.Code, "a replayed submit must still redirect, not error, body: %s", replayVerdict.Body.String())

		gotAfterReplay := decode[mcptools.GetViabilityVerdictOutput](t, callTool(t, csCreator, "get_viability_verdict", mcptools.GetViabilityVerdictInput{ChannelID: ch.ID.String(), IdeaID: ideaID.String()}))
		require.Len(t, gotAfterReplay.History, 2, "replaying the identical idempotency_key must not append an extra verdict version")
	})

	// ── 10: NFR2 -- with more than 50 notes on one Idea, the detail page
	// renders 50 and no paging control, matching list_research_notes'
	// own truncation ──────────────────────────────────────────────────────
	t.Run("10_nfr2_fifty_plus_notes_web_and_mcp_agree_on_truncation", func(t *testing.T) {
		nfrIdea, err := w.st.Ideas().Create(ctx, ch.ID, "M4.1 NFR2 Idea", creator.ID)
		require.NoError(t, err)
		for i := 0; i < 51; i++ {
			// Fixture volume, not itself under test -- direct store call,
			// matching m3_calibration_test.go's stated precedent.
			_, err := w.st.Research().SaveNote(ctx, store.SaveNoteInput{
				ChannelID: ch.ID, IdeaID: &nfrIdea.ID, Text: fmt.Sprintf("nfr2 note %d", i), AuthorPersonID: creator.ID,
				ThreadTitle: "Research",
			})
			require.NoError(t, err)
		}

		rec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+nfrIdea.ID.String())
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		body := rec.Body.String()
		// Each of the 50 rendered notes appears THREE times (once in the
		// note list, once in the save-verdict form's citation multi-select,
		// once in the save-note form's relation picker, mirroring
		// research_integration_test.go's identical count).
		assert.Equal(t, 150, strings.Count(body, "nfr2 note "), "exactly 50 notes must render")
		assert.Contains(t, body, "most recent", "a truncation note must appear")
		assert.NotContains(t, strings.ToLower(body), "load more", "no load-more control may appear (NFR2)")

		mcpList := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), IdeaID: nfrIdea.ID.String(),
		}))
		assert.True(t, mcpList.Truncated, "list_research_notes must also report truncation past its own 50-row default page")
		assert.Len(t, mcpList.Notes, 50, "web and MCP must agree on the SAME 50-row default page size (NFR2)")
	})
}
