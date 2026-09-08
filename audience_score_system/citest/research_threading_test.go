//go:build integration

// research_threading_test.go is root plan #1934's own whole-plan milestone
// e2e citest (issue #1950): the plan's user stories -- thread grouping,
// supersession, exclusion vs. non-retiring relation types, verdict
// staleness warnings, ordinary-browse relation visibility, web/MCP human
// parity, idempotent replay, and backfilled-history stability -- driven end
// to end against the FINAL (post-Stage-3, #1947) schema, across both `web`
// and `mcp` wherever a story names both. Modelled directly on
// m4_1_research_web_test.go's structure and doc-comment conventions: this
// file reuses the shared `world` (e2e_test.go), its mcpConnect/callTool/
// decode/postForm/get/establishSession helpers, and mounts nothing new --
// web/research.New(st) is already mounted by newWorld.
//
// Unlike m4_1/m4_2/m4_3, this file is not itself a single milestone's own
// acceptance test with a dedicated task issue at scaffold/implementation
// time -- it is the root plan's OWN final acceptance test, written once
// every task issue in the plan (#1934) has landed, so every story below
// cites the plan's FR/NFR numbers directly rather than one task's slice of
// them.
//
// # User story -> FR/NFR -> t.Run inventory
//
//   - Story 1, thread grouping (FR3/FR4): "1_thread_grouping".
//   - Story 2, supersession (FR5/FR7/FR8): "2_supersession".
//   - Story 3, exclusion vs. non-retiring types (FR7/FR8): "3_exclusion_vs_non_retiring".
//   - Story 4, verdict staleness warning (FR9/FR10): "4_verdict_staleness_warning".
//   - Story 5, relations on ordinary browse (FR11): "5_relations_on_ordinary_browse".
//   - Story 6, human parity (FR14/FR15/NFR2): "6_human_parity".
//   - Story 7, idempotent replay (NFR1): "7_idempotent_replay".
//   - Story 8, backfilled history (FR2 Stage 1): "8_backfilled_history".
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
)

// TestE2E_ResearchThreading is root plan #1934's own milestone acceptance
// test: one Channel, driven through every user story in the plan, each in
// its own t.Run and (mostly) its own fixture Idea/thread so the stories
// never interfere with one another's assertions -- mirrored from
// m3_calibration_test.go/m4_1_research_web_test.go's sequential,
// state-sharing t.Run structure (a shared world/Channel/Persons, but each
// story's own Idea).
func TestE2E_ResearchThreading(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	creator, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-thread-e2e-creator", "thread-e2e-creator@example.com", "Threading Creator")
	require.NoError(t, err)
	analyst, _, err := w.st.Persons().UpsertByGoogleSubject(ctx, "sub-thread-e2e-analyst", "thread-e2e-analyst@example.com", "Threading Analyst")
	require.NoError(t, err)

	ch, err := w.st.Channels().Create(ctx, "yt-thread-e2e-channel", "Threading E2E Channel", creator.ID)
	require.NoError(t, err)
	require.NoError(t, w.st.Roles().AddRole(ctx, ch.ID, analyst.ID, store.RoleAnalyst, creator.ID))

	creatorCookie := w.establishSession(creator.ID)
	csCreator := w.mcpConnect(creator.ID)
	csAnalyst := w.mcpConnect(analyst.ID)

	// ── 1: FR3/FR4 -- thread grouping ──────────────────────────────────────
	t.Run("1_thread_grouping", func(t *testing.T) {
		ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(), Title: "Thread Grouping Idea",
		}))
		ideaID := ideaOut.IdeaID

		threadTitles := []string{"Thread Alpha", "Thread Beta", "Thread Gamma"}
		for _, title := range threadTitles {
			note := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
				ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Note for " + title, ThreadTitle: title,
				IdempotencyKeyArg: uuid.NewString(),
			}))
			require.NotNil(t, note.ThreadTitle)
			assert.Equal(t, title, *note.ThreadTitle)
		}

		threadsOut := decode[mcptools.ListResearchThreadsOutput](t, callTool(t, csCreator, "list_research_threads", mcptools.ListResearchThreadsInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID,
		}))
		require.Len(t, threadsOut.Threads, 3, "exactly the three distinct thread_titles saved above, no more, no fewer")
		byTitle := make(map[string]mcptools.ResearchThreadOutput, 3)
		for _, th := range threadsOut.Threads {
			byTitle[th.Title] = th
			assert.Equal(t, 1, th.NoteCount, "each thread has exactly the one note saved onto it so far")
		}
		alpha, ok := byTitle["Thread Alpha"]
		require.True(t, ok)

		// Web: the same three threads render on the Idea detail page.
		detailRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID)
		require.Equal(t, 200, detailRec.Code, "body: %s", detailRec.Body.String())
		detailBody := detailRec.Body.String()
		for _, title := range threadTitles {
			assert.Contains(t, detailBody, title)
		}

		// Attach a fourth note to Thread Alpha by thread_id -- its count
		// must move from 1 to 2, and no other thread's count changes.
		_ = decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Second note for Thread Alpha", ThreadID: alpha.ID,
			IdempotencyKeyArg: uuid.NewString(),
		}))
		threadsAfter := decode[mcptools.ListResearchThreadsOutput](t, callTool(t, csCreator, "list_research_threads", mcptools.ListResearchThreadsInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID,
		}))
		require.Len(t, threadsAfter.Threads, 3, "attaching by thread_id must not create a fourth thread")
		for _, th := range threadsAfter.Threads {
			if th.ID == alpha.ID {
				assert.Equal(t, 2, th.NoteCount, "Thread Alpha's count must move from 1 to 2")
			} else {
				assert.Equal(t, 1, th.NoteCount, "the other two threads' counts must not change")
			}
		}
	})

	// ── 2: FR5/FR7/FR8 -- supersession ─────────────────────────────────────
	t.Run("2_supersession", func(t *testing.T) {
		ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(), Title: "Supersession Idea",
		}))
		ideaID := ideaOut.IdeaID

		noteA := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Supersession note A -- old finding", ThreadTitle: "Supersession Thread",
			IdempotencyKeyArg: uuid.NewString(),
		}))
		noteB := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Supersession note B -- newer finding", ThreadID: *noteA.ThreadID,
			Relations:         []mcptools.SaveResearchNoteRelationInput{{RelatedNoteID: noteA.ID, RelationType: "supersedes"}},
			IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Len(t, noteB.Relations, 1)
		assert.Equal(t, "supersedes", noteB.Relations[0].RelationType)
		assert.Equal(t, "outgoing", noteB.Relations[0].Direction)
		assert.Equal(t, noteA.ID, noteB.Relations[0].RelatedNoteID)

		// list_research_notes (default) returns both.
		allOut := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID,
		}))
		gotIDs := map[string]bool{}
		for _, n := range allOut.Notes {
			gotIDs[n.ID] = true
		}
		assert.True(t, gotIDs[noteA.ID], "the default (non-current_only) listing must still include the superseded note")
		assert.True(t, gotIDs[noteB.ID])

		// current_only: true returns only B.
		currentOut := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, CurrentOnly: true,
		}))
		currentIDs := map[string]bool{}
		for _, n := range currentOut.Notes {
			currentIDs[n.ID] = true
		}
		assert.False(t, currentIDs[noteA.ID], "current_only must exclude the superseded note A")
		assert.True(t, currentIDs[noteB.ID], "current_only must still include the superseding note B")

		// Web: the same partition is observable on the Idea detail page's
		// note list -- both notes still render (the page has no
		// current_only toggle of its own), but A's own relation annotation
		// (FR11, an incoming supersedes edge) visibly marks it as retired,
		// while B's marks it as the one that retired A.
		detailRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID)
		require.Equal(t, 200, detailRec.Code, "body: %s", detailRec.Body.String())
		detailBody := detailRec.Body.String()
		assert.Contains(t, detailBody, "Supersession note A -- old finding")
		assert.Contains(t, detailBody, "Supersession note B -- newer finding")
		assert.Contains(t, detailBody, "superseded by note", "note A's own incoming relation must render, marking it retired")
		assert.Contains(t, detailBody, "supersedes note", "note B's own outgoing relation must render")
	})

	// ── 3: FR7/FR8 -- exclusion vs. non-retiring types ─────────────────────
	t.Run("3_exclusion_vs_non_retiring", func(t *testing.T) {
		ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(), Title: "Retention Idea",
		}))
		ideaID := ideaOut.IdeaID

		saveInThread := func(text, threadID string, rel *mcptools.SaveResearchNoteRelationInput) mcptools.ResearchNoteOutput {
			in := mcptools.SaveResearchNoteInput{
				ChannelID: ch.ID.String(), IdeaID: ideaID, Text: text, IdempotencyKeyArg: uuid.NewString(),
			}
			if threadID == "" {
				in.ThreadTitle = "Retention Thread"
			} else {
				in.ThreadID = threadID
			}
			if rel != nil {
				in.Relations = []mcptools.SaveResearchNoteRelationInput{*rel}
			}
			return decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", in))
		}

		excludeTarget := saveInThread("Retention target: excludes", "", nil)
		threadID := *excludeTarget.ThreadID
		caveatsTarget := saveInThread("Retention target: caveats", threadID, nil)
		followsUpTarget := saveInThread("Retention target: follows_up", threadID, nil)
		summarizesTarget := saveInThread("Retention target: summarizes", threadID, nil)

		saveInThread("excludes the target", threadID, &mcptools.SaveResearchNoteRelationInput{RelatedNoteID: excludeTarget.ID, RelationType: "excludes"})
		saveInThread("caveats the target", threadID, &mcptools.SaveResearchNoteRelationInput{RelatedNoteID: caveatsTarget.ID, RelationType: "caveats"})
		saveInThread("follows up the target", threadID, &mcptools.SaveResearchNoteRelationInput{RelatedNoteID: followsUpTarget.ID, RelationType: "follows_up"})
		saveInThread("summarizes the target", threadID, &mcptools.SaveResearchNoteRelationInput{RelatedNoteID: summarizesTarget.ID, RelationType: "summarizes"})

		currentOut := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), ThreadID: threadID, CurrentOnly: true,
		}))
		current := map[string]bool{}
		for _, n := range currentOut.Notes {
			current[n.ID] = true
		}
		assert.False(t, current[excludeTarget.ID], "a note targeted by excludes must drop out under current_only")
		assert.True(t, current[caveatsTarget.ID], "a note targeted only by caveats must remain current")
		assert.True(t, current[followsUpTarget.ID], "a note targeted only by follows_up must remain current")
		assert.True(t, current[summarizesTarget.ID], "a note targeted only by summarizes must remain current")
	})

	// ── 4: FR9/FR10 -- verdict staleness warning ───────────────────────────
	t.Run("4_verdict_staleness_warning", func(t *testing.T) {
		ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(), Title: "Verdict Staleness Idea",
		}))
		ideaID := ideaOut.IdeaID

		noteA := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Verdict staleness note A", ThreadTitle: "Verdict Staleness Thread",
			IdempotencyKeyArg: uuid.NewString(),
		}))

		v1 := decode[mcptools.VerdictOutput](t, callTool(t, csAnalyst, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Verdict: string(store.VerdictNeedsMoreResearch),
			Reasoning: "v1 reasoning citing A", CitedResearchNoteIDs: []string{noteA.ID}, IdempotencyKeyArg: uuid.NewString(),
		}))
		v2 := decode[mcptools.VerdictOutput](t, callTool(t, csAnalyst, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Verdict: string(store.VerdictViable),
			Reasoning: "v2 reasoning also citing A", CitedResearchNoteIDs: []string{noteA.ID}, IdempotencyKeyArg: uuid.NewString(),
		}))

		// Supersede A with a fresh note B, in the same thread.
		_ = decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Verdict staleness note B -- supersedes A", ThreadID: *noteA.ThreadID,
			Relations:         []mcptools.SaveResearchNoteRelationInput{{RelatedNoteID: noteA.ID, RelationType: "supersedes"}},
			IdempotencyKeyArg: uuid.NewString(),
		}))

		got := decode[mcptools.GetViabilityVerdictOutput](t, callTool(t, csCreator, "get_viability_verdict", mcptools.GetViabilityVerdictInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID,
		}))
		require.NotNil(t, got.Current)
		assert.Equal(t, v2.ID, got.Current.ID)
		require.Len(t, got.Current.CitedResearchNotes, 1)
		assert.Equal(t, noteA.ID, got.Current.CitedResearchNotes[0].ID)
		assert.Equal(t, "Verdict staleness note A", got.Current.CitedResearchNotes[0].TextExcerpt, "the current verdict must still return A's own text, unchanged")
		assert.Contains(t, got.Current.CitedResearchNotes[0].RetiredBy, "supersedes", "the current verdict's citation of A must be marked superseded")

		require.Len(t, got.History, 2)
		require.Equal(t, v1.ID, got.History[0].ID)
		require.Len(t, got.History[0].CitedResearchNotes, 1)
		assert.Equal(t, noteA.ID, got.History[0].CitedResearchNotes[0].ID)
		assert.Equal(t, "Verdict staleness note A", got.History[0].CitedResearchNotes[0].TextExcerpt, "the history entry must still return A's own text, unchanged")
		assert.Contains(t, got.History[0].CitedResearchNotes[0].RetiredBy, "supersedes", "the history entry's citation of A must ALSO be marked superseded")

		// Web (#2034, FR4/FR8): the Idea page now renders the CURRENT
		// verdict only, so its citation's staleness warning renders once
		// there.
		detailRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID)
		require.Equal(t, 200, detailRec.Code, "body: %s", detailRec.Body.String())
		detailBody := detailRec.Body.String()
		assert.Equal(t, 1, strings.Count(detailBody, "Superseded"), "the current verdict's citation must surface its own staleness warning once on the Idea page")

		// The history entry's OWN citation warning moved to the
		// verdict-details page (GET .../verdicts) -- FR7's version-select
		// resolves it via ?version=1, the older (needs-more-research)
		// version that also cited A.
		verdictsRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID+"/verdicts?version="+strconv.Itoa(v1.Version))
		require.Equal(t, 200, verdictsRec.Code, "body: %s", verdictsRec.Body.String())
		verdictsBody := verdictsRec.Body.String()
		assert.Contains(t, verdictsBody, "Superseded", "the history entry's citation must ALSO surface the staleness warning, on the verdict-details page")
	})

	// ── 5: FR11 -- relations on ordinary browse, no verdict involved ───────
	t.Run("5_relations_on_ordinary_browse", func(t *testing.T) {
		ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(), Title: "Relation Browse Idea",
		}))
		ideaID := ideaOut.IdeaID

		// Attached pair: an Idea-attached thread, relation type caveats
		// (non-retiring, so it's a pure "is this visible" check, unrelated
		// to story 3's exclusion semantics).
		noteC := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Browse note C -- attached", ThreadTitle: "Browse Thread Attached",
			IdempotencyKeyArg: uuid.NewString(),
		}))
		noteD := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Browse note D -- caveats C", ThreadID: *noteC.ThreadID,
			Relations:         []mcptools.SaveResearchNoteRelationInput{{RelatedNoteID: noteC.ID, RelationType: "caveats"}},
			IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Len(t, noteD.Relations, 1)

		// Unattached pair: no Idea at all, relation type follows_up.
		noteE := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), Text: "Browse note E -- unattached", ThreadTitle: "Browse Thread Unattached",
			IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Nil(t, noteE.IdeaID)
		noteF := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), Text: "Browse note F -- follows up E", ThreadID: *noteE.ThreadID,
			Relations:         []mcptools.SaveResearchNoteRelationInput{{RelatedNoteID: noteE.ID, RelationType: "follows_up"}},
			IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Len(t, noteF.Relations, 1)

		// A plain list_research_notes read (no current_only, no verdict
		// anywhere in this story) shows both relation pairs inline.
		plainOut := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(),
		}))
		byID := make(map[string]mcptools.ResearchNoteOutput, len(plainOut.Notes))
		for _, n := range plainOut.Notes {
			byID[n.ID] = n
		}
		require.Contains(t, byID, noteC.ID)
		require.Len(t, byID[noteC.ID].Relations, 1, "note C's own incoming caveats relation must be visible on a plain read")
		assert.Equal(t, "incoming", byID[noteC.ID].Relations[0].Direction)
		require.Contains(t, byID, noteE.ID)
		require.Len(t, byID[noteE.ID].Relations, 1, "note E's own incoming follows_up relation must be visible on a plain read")
		assert.Equal(t, "incoming", byID[noteE.ID].Relations[0].Direction)

		// Web, Idea detail page (the attached pair).
		detailRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+ideaID)
		require.Equal(t, 200, detailRec.Code, "body: %s", detailRec.Body.String())
		assert.Contains(t, detailRec.Body.String(), "caveated by note", "note C's incoming caveats relation must render on the Idea detail page, no verdict involved")

		// Web, Channel index page (the unattached pair).
		indexRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research")
		require.Equal(t, 200, indexRec.Code, "body: %s", indexRec.Body.String())
		assert.Contains(t, indexRec.Body.String(), "followed up by note", "note E's incoming follows_up relation must render in the Channel index's unattached section")
	})

	// ── 6: FR14/FR15/NFR2 -- human parity ──────────────────────────────────
	t.Run("6_human_parity", func(t *testing.T) {
		ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(), Title: "Human Parity Idea",
		}))
		ideaID := ideaOut.IdeaID

		// The MCP-saved target this task's web submission will supersede,
		// via ThreadTitle's find-or-create (FR4) -- deliberately saved with
		// a differently-cased, differently-spaced title than the web form
		// will submit, so a match proves FR4's case/whitespace-insensitive
		// convergence, not merely an exact string match.
		target := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Human parity target note", ThreadTitle: "human PARITY thread",
			IdempotencyKeyArg: uuid.NewString(),
		}))

		// The web form submits the SAME thread by a new-thread-title text
		// field carrying different case/whitespace ("  Human Parity Thread  ")
		// -- FR14's "new thread title" input, converging on the EXISTING
		// thread rather than creating a fourth one, plus a supersedes
		// relation to the MCP-saved target (FR15).
		webKey := uuid.NewString()
		rec := w.postForm(creatorCookie, "/channels/"+ch.ID.String()+"/research/notes", url.Values{
			"idempotency_key":            {webKey},
			"text":                       {"Human parity web-saved note"},
			"idea_id":                    {ideaID},
			"thread_title":               {"  Human Parity Thread  "},
			"relation_type_" + target.ID: {"supersedes"},
		})
		require.Equal(t, 303, rec.Code, "body: %s", rec.Body.String())

		webListOut := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID,
		}))
		var webNote mcptools.ResearchNoteOutput
		found := false
		for _, n := range webListOut.Notes {
			if n.Text == "Human parity web-saved note" {
				webNote = n
				found = true
				break
			}
		}
		require.True(t, found, "the web-saved note must read back over MCP")
		require.NotNil(t, webNote.ThreadID)
		assert.Equal(t, *target.ThreadID, *webNote.ThreadID, "the web form's thread_title must have converged on the SAME thread the MCP-saved target used, not created a new one")
		require.Len(t, webNote.Relations, 1, "the web form's relation pick must have been written")
		assert.Equal(t, "supersedes", webNote.Relations[0].RelationType)
		assert.Equal(t, "outgoing", webNote.Relations[0].Direction)
		assert.Equal(t, target.ID, webNote.Relations[0].RelatedNoteID)
		assert.Equal(t, creator.ID.String(), webNote.AuthorPersonID, "the author is the signed-in web caller")

		// The equivalent save_research_note call (same thread, same
		// relation shape) produces a structurally identical
		// ResearchNoteOutput/relation shape -- same field set populated the
		// same way, proving the web write is not a second-class record.
		mcpNote := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Human parity MCP-saved note", ThreadID: *target.ThreadID,
			Relations:         []mcptools.SaveResearchNoteRelationInput{{RelatedNoteID: target.ID, RelationType: "supersedes"}},
			IdempotencyKeyArg: uuid.NewString(),
		}))
		require.Len(t, mcpNote.Relations, 1)
		assert.Equal(t, webNote.Relations[0].RelationType, mcpNote.Relations[0].RelationType)
		assert.Equal(t, webNote.Relations[0].Direction, mcpNote.Relations[0].Direction)
		require.NotNil(t, mcpNote.ThreadID)
		assert.Equal(t, *webNote.ThreadID, *mcpNote.ThreadID, "both the web-saved and MCP-saved notes converged on the same thread")
		require.NotNil(t, mcpNote.ThreadTitle)
		require.NotNil(t, webNote.ThreadTitle)
		assert.Equal(t, *webNote.ThreadTitle, *mcpNote.ThreadTitle)
	})

	// ── 7: NFR1 -- idempotent replay ────────────────────────────────────────
	t.Run("7_idempotent_replay", func(t *testing.T) {
		ideaOut := decode[mcptools.IdeaOutput](t, callTool(t, csAnalyst, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: ch.ID.String(), Title: "Idempotent Replay Idea",
		}))
		ideaID := ideaOut.IdeaID

		// note1 pre-exists in "Idempotent Thread" so the replayed call
		// below has a same-thread target for its one relation.
		note1 := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Idempotent replay note 1", ThreadTitle: "Idempotent Thread",
			IdempotencyKeyArg: uuid.NewString(),
		}))

		replayKey := uuid.NewString()
		replayInput := mcptools.SaveResearchNoteInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID, Text: "Idempotent replay note 2", ThreadTitle: "Idempotent Thread",
			Relations:         []mcptools.SaveResearchNoteRelationInput{{RelatedNoteID: note1.ID, RelationType: "caveats"}},
			IdempotencyKeyArg: replayKey,
		}

		first := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", replayInput))
		second := decode[mcptools.ResearchNoteOutput](t, callTool(t, csAnalyst, "save_research_note", replayInput))
		assert.Equal(t, first.ID, second.ID, "replaying the identical (channel, author, idempotency_key) must return the SAME note, not a new one")

		threadsOut := decode[mcptools.ListResearchThreadsOutput](t, callTool(t, csCreator, "list_research_threads", mcptools.ListResearchThreadsInput{
			ChannelID: ch.ID.String(), IdeaID: ideaID,
		}))
		require.Len(t, threadsOut.Threads, 1, "exactly one thread: the replay must not have find-or-created a second one")
		assert.Equal(t, 2, threadsOut.Threads[0].NoteCount, "exactly one thread note count: note1 plus the ONE replayed note2, never a duplicate")

		notesOut := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), ThreadID: threadsOut.Threads[0].ID,
		}))
		require.Len(t, notesOut.Notes, 2, "exactly note1 and the one replayed note2 -- no duplicate note row")
		for _, n := range notesOut.Notes {
			if n.ID == second.ID {
				require.Len(t, n.Relations, 1, "exactly one relation row -- the replay must not have inserted a duplicate")
			}
		}
	})

	// ── 8: FR2 Stage 1 -- backfilled history stays flat and current ────────
	t.Run("8_backfilled_history", func(t *testing.T) {
		idea, err := w.st.Ideas().Create(ctx, ch.ID, "Backfilled History Idea", creator.ID)
		require.NoError(t, err)

		// A direct store fixture insert emulating a pre-migration/backfilled
		// row: a single flat note, its own thread, no relations at all --
		// research_note.idea_id no longer exists as a column at all
		// (migration 018/#1947 dropped it), so there is no genuine
		// pre-migration SHAPE left to seed distinctly; this is the closest
		// fixture to it (root plan #1934, Out of scope: pre-migration notes
		// stay flat and current, never retroactively structured).
		backfilled, err := w.st.Research().SaveNote(ctx, store.SaveNoteInput{
			ChannelID: ch.ID, IdeaID: &idea.ID, Text: "Backfilled pre-threading note", AuthorPersonID: creator.ID,
			ThreadTitle: "Backfilled Thread",
		})
		require.NoError(t, err)

		detailRec := w.get(creatorCookie, "/channels/"+ch.ID.String()+"/research/ideas/"+idea.ID.String())
		require.Equal(t, 200, detailRec.Code, "body: %s", detailRec.Body.String())
		assert.Contains(t, detailRec.Body.String(), "Backfilled pre-threading note", "the backfilled note must render on its Idea's detail page")

		currentOut := decode[mcptools.ListResearchNotesOutput](t, callTool(t, csCreator, "list_research_notes", mcptools.ListResearchNotesInput{
			ChannelID: ch.ID.String(), IdeaID: idea.ID.String(), CurrentOnly: true,
		}))
		found := false
		for _, n := range currentOut.Notes {
			if n.ID == backfilled.ID.String() {
				found = true
			}
		}
		assert.True(t, found, "a backfilled note with no relations must remain current -- it is never retroactively superseded or excluded")
	})
}
