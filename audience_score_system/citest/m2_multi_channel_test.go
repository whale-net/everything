//go:build integration

// Package citest_test (M2 half): Audience Score System's M2 milestone
// acceptance test (issue #1728, part of the root plan #1709) -- the
// outcome sentence for M2: "a Founder with several Channels, and the
// Analysts and Co-Creators on them, all work across every Channel
// they're associated with." Modelled exactly on TestE2E_ThreeLoopsEndToEnd
// (e2e_test.go, M1's own acceptance test, issue #1583): every step below
// runs through the real store/handler/tool surfaces sharing this file's
// `world` (see e2e_test.go's newWorld, extended for M2's access/my-work
// wiring), never fixtures poked directly into tables. Every other M2
// task's own *_integration_test.go already proves its own slice in
// isolation (see the FR/NFR coverage inventory below for exactly which
// file); this file's job is proving the STEPS CONNECT across two
// Channels and three tiers, not re-litigating each task's own edge
// cases.
//
// # FR/NFR coverage inventory
//
//   - FR25 -- exercised here (steps 1-2: Channel A then Channel B, both
//     created via ChannelStore.Create for the SAME Person, no second
//     UpsertByGoogleSubject call -- "no second signup"). OAuth consent
//     mechanics: web/channel/channel_test.go (unchanged by M2).
//   - FR26 -- exercised here (step 11: store.AccessStore.
//     ChannelsWithRoleForPerson -- the exact data source GET /channels
//     calls -- cross-checked against list_channels for every Person, at
//     every point their access changes). HTTP/HTML-level coverage:
//     web/channels_integration_test.go.
//   - FR27/FR28/NFR9 -- exercised here (step 9: get_my_work and
//     store.MyWorkStore.SummariesForPerson -- GET /my-work's exact data
//     source -- for the Analyst N, cross-checked against each other,
//     before and after N's role on Channel A is revoked, on the SAME MCP
//     session with no reconnect). HTTP/HTML-level coverage:
//     web/my_work_integration_test.go. NFR9's no-N+1 budget:
//     mcp/tools/my_work_integration_test.go /
//     mcp/tools/list_channels_integration_test.go.
//   - FR29/NFR10 (exactly one Founder, DB backstop) -- pointer:
//     store/store_integration_test.go (constraint-level test); this file
//     exercises FR29's positive shape throughout (each Channel here has
//     exactly one Founder, never re-tiered or demoted).
//   - FR30/NFR11 -- exercised here (step 4: invite_co_creator's web
//     counterpart, POST /channels/{id}/access/invites, generates a live
//     Co-Creator invite while a live Analyst invite coexists on the same
//     Channel; a repeat call returns the identical code).
//   - FR31 -- exercised here (step 7: promote_to_co_creator (MCP) and
//     HandlePromote (web) agree on the same target -- one real
//     promotion via web, one idempotent no-op replay via MCP).
//   - FR32 -- exercised here (step 6: Co-Creator C alone -- no Founder
//     cookie or session touched in this subtest -- approves a schedule
//     draft and invites a second Analyst).
//   - FR33 -- exercised here (step 8: Co-Creator C's removal attempts
//     against the Founder and against another Co-Creator are rejected on
//     BOTH surfaces with no state change; step 9: the Founder's removal
//     of a Co-Creator succeeds).
//   - FR34/FR35 -- exercised here (step 10: get_channel_access's history
//     lists every grant/revoke above, newest first, with the right
//     actors -- including the documented granted_by/revoked_by gap on a
//     promotion's implicit revoke-half, issue #1787 -- and an Analyst is
//     refused on both the MCP tool and the web page).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //audience_score_system/citest:e2e_test --test_output=all
package citest_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptools "github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
)

// TestE2E_M2_MultiChannelMultiTier is M2's own acceptance test (issue
// #1728): one Founder, two Channels, a Co-Creator, and two Analysts,
// driven through the real store/handler/tool surfaces this world exposes.
// t.Run subtests run sequentially (none call t.Parallel()), each seeing
// exactly the state the previous one left behind.
func TestE2E_M2_MultiChannelMultiTier(t *testing.T) {
	w := newWorld(t)
	ctx := w.ctx

	var (
		f, n, c, n2                       store.Person
		chA, chB                          store.Channel
		fCookie, nCookie, cCookie, n2C    *http.Cookie
		coCreatorInviteCode, analystCode2 string
		ideaID, verdictID                 uuid.UUID
		entryID                           uuid.UUID
	)

	// ── Step 1: Founder connects Channel A ──────────────────────────────
	t.Run("1_founder_connects_channel_a", func(t *testing.T) {
		var created bool
		var err error
		f, created, err = w.st.Persons().UpsertByGoogleSubject(ctx, "sub-founder-m2", "founder@example.com", "Founder Person")
		require.NoError(t, err)
		assert.True(t, created)
		fCookie = w.establishSession(f.ID)

		chA, err = w.st.Channels().Create(ctx, "yt-m2-channel-a", "Channel A", f.ID)
		require.NoError(t, err)

		roles, err := w.st.Roles().RolesFor(ctx, chA.ID, f.ID)
		require.NoError(t, err)
		assert.Equal(t, []store.Role{store.RoleCreator}, roles, "FR29: connecting Person is Channel A's sole Founder")

		// FR34: the grant is self-recorded -- the connecting Person is
		// their own granted_by_person_id, since there is no other actor
		// who could have granted the Founder role (store/channel.go's
		// Create doc comment).
		history, err := w.st.Access().AuditTrail(ctx, chA.ID, 0)
		require.NoError(t, err)
		require.Len(t, history, 1)
		assert.Equal(t, "granted", history[0].Event)
		assert.Equal(t, store.RoleCreator, history[0].Role)
		require.NotNil(t, history[0].ActorPersonID)
		assert.Equal(t, f.ID, *history[0].ActorPersonID, "FR34: Channel-connect records the connecting Person as their own grant's actor")
	})

	// ── Step 2: Founder connects Channel B -- same sign-in ──────────────
	t.Run("2_founder_connects_channel_b_no_second_signup", func(t *testing.T) {
		// No second UpsertByGoogleSubject call: f is reused as-is,
		// proving FR25's "reusing the existing OAuth-connect flow ...
		// the connecting Person becomes that new Channel's Founder" needs
		// no fresh identity.
		var err error
		chB, err = w.st.Channels().Create(ctx, "yt-m2-channel-b", "Channel B", f.ID)
		require.NoError(t, err)

		rows, err := w.st.Access().ChannelsWithRoleForPerson(ctx, f.ID)
		require.NoError(t, err)
		require.Len(t, rows, 2, "FR26: F now has an open role on both Channels")
		byChannel := map[uuid.UUID]store.Role{}
		for _, row := range rows {
			byChannel[row.Channel.ID] = row.Role
		}
		assert.Equal(t, store.RoleCreator, byChannel[chA.ID])
		assert.Equal(t, store.RoleCreator, byChannel[chB.ID], "FR25: F is Founder on Channel B too, same identity")

		// list_channels (MCP) must agree with the store.AccessStore read
		// GET /channels itself uses (see newWorld's doc comment on why
		// this file drives the store call directly instead of the web
		// handler for this one capability).
		lc := decode[mcptools.ListChannelsOutput](t, callTool(t, w.mcpConnect(f.ID), "list_channels", struct{}{}))
		require.Len(t, lc.Channels, 2)
		for _, ch := range lc.Channels {
			assert.Equal(t, string(store.RoleCreator), ch.Roles[0])
		}
	})

	// ── Step 3: Founder invites Analyst N to A (and to B, for step 9) ───
	t.Run("3_invite_analyst_n", func(t *testing.T) {
		// F generates an Analyst invite on A via the real web route
		// (C3/FR5, symmetric authority per FR32 -- exercised for a
		// Founder here, for a Co-Creator in step 6).
		rec := w.postForm(fCookie, "/channels/"+chA.ID.String()+"/invites", nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		invA, live, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleAnalyst)
		require.NoError(t, err)
		require.True(t, live)

		var created bool
		n, created, err = w.st.Persons().UpsertByGoogleSubject(ctx, "sub-analyst-n-m2", "analyst-n@example.com", "Analyst N")
		require.NoError(t, err)
		require.True(t, created)
		nCookie = w.establishSession(n.ID)

		rec = w.get(nCookie, "/invites/"+invA.Code+"/resume")
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())

		roles, err := w.st.Roles().RolesFor(ctx, chA.ID, n.ID)
		require.NoError(t, err)
		assert.Equal(t, []store.Role{store.RoleAnalyst}, roles, "FR29: N is Analyst on A")

		// N is also given a role on Channel B -- purely so step 9 can
		// prove that revoking N's role on A leaves B untouched.
		rec = w.postForm(fCookie, "/channels/"+chB.ID.String()+"/invites", nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		invB, live, err := w.st.Invites().LiveForRole(ctx, chB.ID, store.RoleAnalyst)
		require.NoError(t, err)
		require.True(t, live)
		rec = w.get(nCookie, "/invites/"+invB.Code+"/resume")
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		rolesB, err := w.st.Roles().RolesFor(ctx, chB.ID, n.ID)
		require.NoError(t, err)
		assert.Equal(t, []store.Role{store.RoleAnalyst}, rolesB, "N also holds Analyst on B, independent of A")
	})

	// ── Step 4: coexisting Co-Creator + Analyst invites on A (NFR11) ────
	t.Run("4_coexisting_invites_and_idempotent_reissue", func(t *testing.T) {
		// F generates a Co-Creator invite on A (FR30, web counterpart of
		// invite_co_creator -- MCP coverage of the same tool:
		// mcp/tools/access_integration_test.go).
		rec := w.postForm(fCookie, "/channels/"+chA.ID.String()+"/access/invites", nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		coInv1, live, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleCoCreator)
		require.NoError(t, err)
		require.True(t, live)
		coCreatorInviteCode = coInv1.Code

		// N's Analyst invite path is exercised again: the invite N
		// redeemed in step 3 is already consumed, so this mints a FRESH
		// live Analyst invite on A (used by the second Analyst, n2, in
		// step 6) -- while the Co-Creator invite above is still live.
		rec = w.postForm(fCookie, "/channels/"+chA.ID.String()+"/invites", nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		analystInv2, live, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleAnalyst)
		require.NoError(t, err)
		require.True(t, live)
		analystCode2 = analystInv2.Code

		// NFR11: both tiers are live on Channel A at the same time.
		_, coLive, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleCoCreator)
		require.NoError(t, err)
		assert.True(t, coLive, "NFR11: a live Co-Creator invite and a live Analyst invite coexist on one Channel")
		_, analystLive, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleAnalyst)
		require.NoError(t, err)
		assert.True(t, analystLive)

		// FR30/LB4: re-issuing the Co-Creator invite returns the SAME
		// code, not a new one.
		rec = w.postForm(fCookie, "/channels/"+chA.ID.String()+"/access/invites", nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		coInv1Again, live, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleCoCreator)
		require.NoError(t, err)
		require.True(t, live)
		assert.Equal(t, coInv1.Code, coInv1Again.Code, "FR30/LB4: re-issuing a live Co-Creator invite returns the existing code")
	})

	// ── Step 5: Person C accepts the Co-Creator invite ──────────────────
	t.Run("5_person_c_becomes_co_creator", func(t *testing.T) {
		var err error
		var created bool
		c, created, err = w.st.Persons().UpsertByGoogleSubject(ctx, "sub-person-c-m2", "person-c@example.com", "Person C")
		require.NoError(t, err)
		require.True(t, created)
		cCookie = w.establishSession(c.ID)

		rec := w.get(cCookie, "/invites/"+coCreatorInviteCode+"/resume")
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())

		roles, err := w.st.Roles().RolesFor(ctx, chA.ID, c.ID)
		require.NoError(t, err)
		assert.Equal(t, []store.Role{store.RoleCoCreator}, roles, "FR29: C is now Co-Creator on A")

		canApprove, err := store.CanApprove(ctx, w.st.Roles(), chA.ID, c.ID)
		require.NoError(t, err)
		assert.True(t, canApprove, "FR32: Co-Creator holds approve authority, symmetric with the Founder")

		_, live, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleCoCreator)
		require.NoError(t, err)
		assert.False(t, live, "the Co-Creator invite is consumed")
	})

	// ── Step 6: Co-Creator authority alone -- no Founder involvement ────
	t.Run("6_co_creator_approves_and_invites_second_analyst", func(t *testing.T) {
		csAnalystN := w.mcpConnect(n.ID)

		idea := decode[mcptools.IdeaOutput](t, callTool(t, csAnalystN, "create_idea", mcptools.CreateIdeaInput{
			ChannelID: chA.ID.String(),
			Title:     "M2 Story Idea",
		}))
		ideaID = uuid.MustParse(idea.IdeaID)

		verdict := decode[mcptools.VerdictOutput](t, callTool(t, csAnalystN, "save_viability_verdict", mcptools.SaveViabilityVerdictInput{
			ChannelID:         chA.ID.String(),
			IdeaID:            ideaID.String(),
			Verdict:           string(store.VerdictViable),
			Reasoning:         "Clear enough for M2's story.",
			IdempotencyKeyArg: uuid.NewString(),
		}))
		verdictID = uuid.MustParse(verdict.ID)

		draft := decode[mcptools.SaveScheduleDraftOutput](t, callTool(t, csAnalystN, "save_schedule_draft", mcptools.SaveScheduleDraftInput{
			ChannelID:         chA.ID.String(),
			IdeaID:            ideaID.String(),
			ProposedPublishAt: time.Now().Add(72 * time.Hour).Format(time.RFC3339),
			VerdictID:         verdictID.String(),
			IdempotencyKeyArg: uuid.NewString(),
		}))
		entryID = uuid.MustParse(draft.ScheduleEntryID)

		// C alone approves it -- only cCookie is used in this subtest,
		// fCookie never touched (FR32: "no consensus, voting, or
		// Founder-tiebreak mechanic").
		rec := w.postForm(cCookie, "/schedule/"+entryID.String()+"/approve", nil)
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		entry, err := w.st.Schedules().GetByID(ctx, entryID)
		require.NoError(t, err)
		assert.Equal(t, store.ScheduleStateCommitted, entry.State, "FR32: Co-Creator alone can commit a draft")

		// C also invites the second Analyst (FR32's invite-authority
		// half) -- the tier-4 invite generated in step 4 is still live,
		// so this converges on the SAME code (FR30/LB4), issued this
		// time by a Co-Creator rather than the Founder.
		rec = w.postForm(cCookie, "/channels/"+chA.ID.String()+"/invites", nil)
		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		liveInvite, live, err := w.st.Invites().LiveForRole(ctx, chA.ID, store.RoleAnalyst)
		require.NoError(t, err)
		require.True(t, live)
		assert.Equal(t, analystCode2, liveInvite.Code, "FR32: Co-Creator's own invite call succeeds, converging on the already-live code")

		var created bool
		n2, created, err = w.st.Persons().UpsertByGoogleSubject(ctx, "sub-analyst-n2-m2", "analyst-n2@example.com", "Analyst N2")
		require.NoError(t, err)
		require.True(t, created)
		n2C = w.establishSession(n2.ID)

		rec = w.get(n2C, "/invites/"+analystCode2+"/resume")
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		canWrite, err := store.CanWrite(ctx, w.st.Roles(), chA.ID, n2.ID)
		require.NoError(t, err)
		assert.True(t, canWrite, "N2 is now Analyst on A")
	})

	// ── Step 7: Co-Creator promotes N; repeat promote is a no-op ────────
	t.Run("7_promote_n_to_co_creator", func(t *testing.T) {
		// Real promotion via the web surface.
		rec := w.postForm(cCookie, "/channels/"+chA.ID.String()+"/access/promote", url.Values{"person_id": {n.ID.String()}})
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		roles, err := w.st.Roles().RolesFor(ctx, chA.ID, n.ID)
		require.NoError(t, err)
		assert.Equal(t, []store.Role{store.RoleCoCreator}, roles, "FR31: N is promoted to Co-Creator")

		// The MCP tool agrees and treats a repeat as an idempotent no-op
		// (FR31) -- proving web and MCP agree on the same target.
		out := decode[mcptools.PromoteToCoCreatorOutput](t, callTool(t, w.mcpConnect(c.ID), "promote_to_co_creator", mcptools.PromoteToCoCreatorInput{
			ChannelID: chA.ID.String(),
			PersonID:  n.ID.String(),
		}))
		assert.Equal(t, "co_creator", out.Role)
		assert.False(t, out.Changed, "FR31: promoting an already-promoted Co-Creator is a no-op success, not an error")
	})

	// ── Step 8: Co-Creator cannot remove the Founder or another Co-Creator ──
	t.Run("8_co_creator_removal_matrix_rejections", func(t *testing.T) {
		// C attempts to remove F (the Founder) -- MCP and web both refuse.
		res := callTool(t, w.mcpConnect(c.ID), "remove_channel_person", mcptools.RemoveChannelPersonInput{
			ChannelID: chA.ID.String(),
			PersonID:  f.ID.String(),
		})
		assert.True(t, res.IsError, "FR33: a Co-Creator may never remove the Founder")
		rec := w.postForm(cCookie, "/channels/"+chA.ID.String()+"/access/remove", url.Values{"person_id": {f.ID.String()}})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		roles, err := w.st.Roles().RolesFor(ctx, chA.ID, f.ID)
		require.NoError(t, err)
		assert.Equal(t, []store.Role{store.RoleCreator}, roles, "no state change: F is still Founder")

		// C attempts to remove N -- now a fellow Co-Creator -- MCP and
		// web both refuse.
		res = callTool(t, w.mcpConnect(c.ID), "remove_channel_person", mcptools.RemoveChannelPersonInput{
			ChannelID: chA.ID.String(),
			PersonID:  n.ID.String(),
		})
		assert.True(t, res.IsError, "FR33: a Co-Creator may never remove another Co-Creator")
		rec = w.postForm(cCookie, "/channels/"+chA.ID.String()+"/access/remove", url.Values{"person_id": {n.ID.String()}})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		rolesN, err := w.st.Roles().RolesFor(ctx, chA.ID, n.ID)
		require.NoError(t, err)
		assert.Equal(t, []store.Role{store.RoleCoCreator}, rolesN, "no state change: N is still Co-Creator")
	})

	// ── Step 9: Founder removes N; N's aggregate view drops A, keeps B ──
	t.Run("9_founder_removes_n_aggregate_view_updates_live", func(t *testing.T) {
		// One MCP session for N, opened ONCE and reused for both the
		// before and after calls below -- proving FR28's "no
		// re-authentication or session change required".
		csAnalystN := w.mcpConnect(n.ID)

		before := decode[mcptools.GetMyWorkOutput](t, callTool(t, csAnalystN, "get_my_work", mcptools.GetMyWorkInput{}))
		beforeChannels := map[string]bool{}
		for _, ch := range before.Channels {
			beforeChannels[ch.Channel.ChannelID] = true
		}
		assert.True(t, beforeChannels[chA.ID.String()], "N's aggregate view includes A before removal")
		assert.True(t, beforeChannels[chB.ID.String()], "N's aggregate view includes B before removal")

		storeBefore, err := w.st.MyWork().SummariesForPerson(ctx, n.ID, 3)
		require.NoError(t, err)
		assert.Len(t, storeBefore, len(before.Channels), "get_my_work agrees with store.MyWorkStore.SummariesForPerson (GET /my-work's own data source)")

		// F removes N (now a Co-Creator) from A -- FR33: a Founder may
		// remove a Co-Creator.
		rec := w.postForm(fCookie, "/channels/"+chA.ID.String()+"/access/remove", url.Values{"person_id": {n.ID.String()}})
		require.Equal(t, http.StatusSeeOther, rec.Code, "body: %s", rec.Body.String())
		roles, err := w.st.Roles().RolesFor(ctx, chA.ID, n.ID)
		require.NoError(t, err)
		assert.Empty(t, roles, "N's role on A is revoked")

		// The exact same, never-reconnected MCP session immediately
		// reflects the change (FR28) -- no reconnect, no new credential.
		after := decode[mcptools.GetMyWorkOutput](t, callTool(t, csAnalystN, "get_my_work", mcptools.GetMyWorkInput{}))
		afterChannels := map[string]bool{}
		for _, ch := range after.Channels {
			afterChannels[ch.Channel.ChannelID] = true
		}
		assert.False(t, afterChannels[chA.ID.String()], "FR28: A drops out of N's aggregate view on the very next call, no re-auth")
		assert.True(t, afterChannels[chB.ID.String()], "N's association with B is unaffected by A's revoke")

		storeAfter, err := w.st.MyWork().SummariesForPerson(ctx, n.ID, 3)
		require.NoError(t, err)
		assert.Len(t, storeAfter, len(after.Channels), "get_my_work still agrees with GET /my-work's own data source after the change")

		// list_channels (FR26) on the same session shows the same drop.
		lc := decode[mcptools.ListChannelsOutput](t, callTool(t, csAnalystN, "list_channels", struct{}{}))
		require.Len(t, lc.Channels, 1)
		assert.Equal(t, chB.ID.String(), lc.Channels[0].ChannelID)
		rows, err := w.st.Access().ChannelsWithRoleForPerson(ctx, n.ID)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, chB.ID, rows[0].Channel.ID, "GET /channels' own data source agrees: only B remains for N")
	})

	// ── Step 10: audit trail (FR34/FR35), Analyst refused on both surfaces ──
	t.Run("10_audit_trail", func(t *testing.T) {
		out := decode[mcptools.GetChannelAccessOutput](t, callTool(t, w.mcpConnect(f.ID), "get_channel_access", mcptools.GetChannelAccessInput{
			ChannelID: chA.ID.String(),
		}))
		require.NotEmpty(t, out.History)

		// Newest first: the most recent event is F's removal of N above.
		latest := out.History[0]
		assert.Equal(t, "revoked", latest.Event)
		assert.Equal(t, n.ID.String(), latest.SubjectPersonID)
		assert.Equal(t, f.DisplayName, latest.ActorDisplayName, "FR34/FR35: the removal is attributed to F")

		// Every grant/revoke walked through this story is present.
		type key struct{ event, subject, role string }
		seen := map[key]mcptools.AuditEventOutput{}
		for _, ev := range out.History {
			seen[key{ev.Event, ev.SubjectPersonID, ev.Role}] = ev
		}
		wantGrants := []key{
			{"granted", f.ID.String(), "creator"},
			{"granted", n.ID.String(), "analyst"},
			{"granted", c.ID.String(), "co_creator"},
			{"granted", n2.ID.String(), "analyst"},
			{"granted", n.ID.String(), "co_creator"}, // step 7's promotion
			{"revoked", n.ID.String(), "co_creator"}, // step 9's removal
		}
		for _, wk := range wantGrants {
			_, ok := seen[wk]
			assert.True(t, ok, "expected audit event %+v in Channel A's history", wk)
		}
		assert.Equal(t, f.DisplayName, seen[key{"granted", f.ID.String(), "creator"}].ActorDisplayName)
		assert.Equal(t, f.DisplayName, seen[key{"granted", n.ID.String(), "analyst"}].ActorDisplayName)
		assert.Equal(t, f.DisplayName, seen[key{"granted", c.ID.String(), "co_creator"}].ActorDisplayName)
		assert.Equal(t, c.DisplayName, seen[key{"granted", n.ID.String(), "co_creator"}].ActorDisplayName, "step 7's promotion is attributed to C")

		// Step 7's promotion closed N's OLD analyst row via addRoleTx,
		// which never stamps revoked_by_person_id on a promotion's
		// implicit revoke-half (documented, real gap -- issue #1787,
		// explicitly out of scope for this task): that "revoked
		// analyst" event renders "unknown", not a fabricated actor, and
		// not F or C.
		revokedAnalystN, ok := seen[key{"revoked", n.ID.String(), "analyst"}]
		require.True(t, ok, "N's analyst row must show as revoked (closed by the promotion)")
		assert.Equal(t, "unknown", revokedAnalystN.ActorDisplayName, "issue #1787: promotion's implicit revoke-half records no actor")

		// Channel B's own history is untouched by anything on A.
		outB := decode[mcptools.GetChannelAccessOutput](t, callTool(t, w.mcpConnect(f.ID), "get_channel_access", mcptools.GetChannelAccessInput{
			ChannelID: chB.ID.String(),
		}))
		for _, ev := range outB.History {
			assert.NotEqual(t, c.ID.String(), ev.SubjectPersonID, "Channel B's audit trail must never mention Channel A's Co-Creator activity")
		}

		// An Analyst is refused on both surfaces -- neither roster nor
		// history, not a partial response.
		res := callTool(t, w.mcpConnect(n2.ID), "get_channel_access", mcptools.GetChannelAccessInput{ChannelID: chA.ID.String()})
		assert.True(t, res.IsError, "FR35: an Analyst must not see the audit trail via MCP")
		rec := w.get(n2C, "/channels/"+chA.ID.String()+"/access")
		assert.Equal(t, http.StatusForbidden, rec.Code, "FR35: an Analyst must not see the access-management page")

		// F (still Founder) sees it fine on the web surface too.
		rec = w.get(fCookie, "/channels/"+chA.ID.String()+"/access")
		assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	})

	// ── Step 11: Channel isolation -- a read on A never returns B's data ──
	t.Run("11_channel_isolation", func(t *testing.T) {
		ideaOnB, err := w.st.Ideas().Create(ctx, chB.ID, "Channel B Only Idea", f.ID)
		require.NoError(t, err)

		ideasA, err := w.st.Ideas().ListByChannel(ctx, chA.ID)
		require.NoError(t, err)
		for _, idea := range ideasA {
			assert.NotEqual(t, ideaOnB.ID, idea.ID, "FR26: a Channel-scoped read on A must never surface Channel B's data")
		}

		ideasB, err := w.st.Ideas().ListByChannel(ctx, chB.ID)
		require.NoError(t, err)
		var foundOnB bool
		for _, idea := range ideasB {
			if idea.ID == ideaOnB.ID {
				foundOnB = true
			}
		}
		assert.True(t, foundOnB, "the idea is genuinely readable, scoped correctly, on its own Channel")

		// Final tier snapshot, every Person, both Channels -- FR26.
		type want struct {
			channelID uuid.UUID
			role      store.Role
		}
		cases := map[string][]want{
			"founder":    {{chA.ID, store.RoleCreator}, {chB.ID, store.RoleCreator}},
			"person_c":   {{chA.ID, store.RoleCoCreator}},
			"analyst_n":  {{chB.ID, store.RoleAnalyst}},
			"analyst_n2": {{chA.ID, store.RoleAnalyst}},
		}
		personIDs := map[string]uuid.UUID{"founder": f.ID, "person_c": c.ID, "analyst_n": n.ID, "analyst_n2": n2.ID}
		for name, expected := range cases {
			rows, err := w.st.Access().ChannelsWithRoleForPerson(ctx, personIDs[name])
			require.NoError(t, err)
			require.Len(t, rows, len(expected), "%s: unexpected channel count", name)
			got := map[uuid.UUID]store.Role{}
			for _, row := range rows {
				got[row.Channel.ID] = row.Role
			}
			for _, e := range expected {
				assert.Equal(t, e.role, got[e.channelID], "%s on channel %s", name, e.channelID)
			}
		}
	})
}

// Trailing comment added to re-trigger CI (empty-commit synchronize did not fire).
