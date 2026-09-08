# NFR3 interface allocation

The web UI is **limited to six UI-only surfaces** — C1/C2/C3 (OAuth-consent
flows) plus C18's edit slice, C20, and C21 (the latter three added by the
batch tracked under issue #2027; see the amendments at the end of this
section for each) — everything else is MCP-exposed too, per the product
brief's MCP-agent-first interface decision:

- **C1** — OAuth signup/login (Google OAuth consent → Person record).
- **C2** — Channel connect (YouTube OAuth consent → Channel + `role=creator`
  join row, LB2).
- **C3** — Analyst invite/accept (invite code generation and
  accept/decline).
- **C18, edit slice only** — `video_script` content editing
  (`store.VideoScriptStore.UpdateContent`, issue #2037, PR #2069). C18's
  *propose* slice is dual-surface (see the #1914 and #2036 amendments
  below) -- only the edit slice has no MCP counterpart.
- **C20** — Channel dashboard 24h/7d activity (`store.DashboardStore.
  ChannelActivity`, issue #2038); no MCP tool exists or is planned.
- **C21** — Published-videos browse with title/date/sync-status filters
  (`store.SyncStore.ListPublishedWithMetrics`, issue #2031); no MCP tool
  exists or is planned.

C1/C2/C3 are OAuth-consent flows tied to a browser redirect and cannot be
MCP tools by construction (there is no meaningful "call this MCP tool to
complete a Google consent screen") -- see the amendments below for why
C18-edit, C20, and C21 are each UI-only for a different, non-OAuth reason.
Every other capability — C4 (research notes), C5 (viability verdicts), C6
(schedule sync reads), C9 (outcome comparison, pending-match
confirm/reject), C10 (browsing), C18 (propose), **C19 (video_script
greenlight/deny/archive)** — is exposed as `mcp` tools, whether or not
`web` also renders a UI for it. C7 (schedule drafting, pacing policy) and
the original C8 (schedule-draft commit/un-commit/edit) were retired
outright by the video-script-model milestone (FR41/FR46/FR47, issues
#1823-#1835) -- see the amendment below.

**NFR3 amendment (issue #1648): C8 was no longer a `web`-only surface
(historical -- C8 itself is now retired, see the next amendment).** M1
originally kept all of C8 (approve, un-approve, edit) `web`-only, on the
theory that committing a schedule was a deliberate human action best gated
behind a UI click. In practice this made the FR16→FR19→FR22/FR23 pipeline
(draft → commit → auto/pending-match → resolve) structurally unreachable
from an MCP-only client: `mcp`'s outcome matcher and `resolve_pending_match`
only ever consider *committed* entries, and nothing in `mcp` could ever
produce one. `mcp/tools/schedule_draft.go` (deleted by #1832) exposed the
full set -- `commit_schedule_draft`, `uncommit_schedule_draft`,
`update_schedule_draft` -- each calling the exact same `store.ScheduleStore`
method and `store.CanApprove` (Creator-tier: Founder or Co-Creator,
symmetrically per FR32) check `web`'s approve/unapprove/edit handlers
already used, so the authority boundary was unchanged: an Analyst
credential was rejected on either surface. The two surfaces were two
independent, equally-capable front ends onto the same `store.ScheduleStore`,
not a primary (`web`) and a read-only shadow (`mcp`) -- until the whole
`store.ScheduleStore` surface was retired outright by the next amendment.

**NFR3 amendment (milestone video-script-model, issues #1823-#1835): C6/C7/
C8 retired outright, C18/C19 replace C7/C8 under `video_script`.** FR41
retires C7's schedule-draft/pacing-policy MCP tool surface and FR46 retires
C6's read-only YouTube-schedule tool (`get_channel_schedule`) with no
successor -- neither is reintroduced as a new capability; the underlying
YouTube sync job (`worker/sync`) is unaffected, only the two read/write
surfaces presenting synced data *as a schedule* are gone. FR47 drops
Strategy's `cadence` field, the only remaining input `generate_schedule_plan`
(C7's slot-proposal tool) read, so that tool is retired too. In their place,
C18 (propose a `video_script`, `save_video_script`, Founder/Co-Creator/
Analyst via `store.CanWrite`) and C19 (greenlight/deny/archive a
`video_script`, `greenlight_video_script`/`deny_video_script`/
`archive_video_script` plus `web/schedule`'s rebuilt UI, Creator-tier via
`store.CanApprove`) are dual-surface from the start (FR48/FR49) -- `mcp`
and `web` call the exact same `store.VideoScriptStore` methods, the same
"two independent, equally-capable front ends" shape the retired C8
amendment above established, just re-anchored onto `video_script` instead
of `schedule_entry`. `web/schedule`'s route paths and package name are
unchanged (FR49's route-and-package-naming note -- `{scriptID}` replaces
`{entryID}` as the path parameter's referent, not its literal spelling);
`HandleUnapprove` and `HandleEdit` have no `video_script` analog and were
retired outright, not rebuilt (FR40 defines no `greenlit→proposed`
transition, and a `video_script`'s target date is set once at propose
time). Migration 013 (issue #1835) drops `schedule_entry`/`pacing_policy`
outright once every reader was retargeted -- `store.ScheduleStore` and
`store.PacingStore` no longer exist.

**NFR3 amendment (milestone video-script-model, issue #1823): C18/C19/C10
are dual-surface under `video_script`; C6/C7/C8 retired outright, no C20
adopted.** This appends the specific FR-level allocation the amendment
above established in outline:

- **C18 (propose, historical -- superseded by the #1914 and #2036
  amendments below; C18 propose is dual-surface as of this batch, #2027):**
  `save_video_script` (`mcp/tools/video_script.go`) is
  `mcp`-only -- FR48/FR49 rebuild `web/schedule`'s read and write surfaces
  in place, but do not add a web-side propose action (see "Out of scope"
  on the milestone's root plan issue), matching M1's shape where drafts
  were also only ever created via `mcp`, never `web`.
- **C19 (greenlight/deny/archive):** dual-surface (FR49) --
  `greenlight_video_script`/`deny_video_script`/`archive_video_script`
  (`mcp/tools/video_script.go`) and `web/schedule.Handlers.
  HandleGreenlight`/`HandleDeny`/`HandleArchive` (`POST
  /scripts/{scriptID}/approve|deny|archive`, renamed from `/schedule` by
  FR20/FR22, #2030) call the identical
  `store.VideoScriptStore` transition methods and the identical
  `store.CanApprove` (Creator-tier) check -- the same "two independent,
  equally-capable front ends" relationship the retired-C8 amendment above
  established, re-anchored onto `video_script`.
- **C10 (browsing):** dual-surface under `video_script` (FR42/FR48) --
  `get_channel_overview`'s `video_scripts` section (`mcp/tools/browse.go`,
  FR42) and `web/schedule.Handlers.HandleList` (`GET
  /channels/{id}/scripts`, FR48; the route was renamed to this by
  FR20/FR22, #2030) both read a Channel's `video_script`
  rows (title, status, target date if set, bound verdict) in place of the
  retired `schedule_entry` listing; `web`'s list view stays `store.CanRead`
  (Founder/Co-Creator/Analyst), unchanged from its pre-amendment
  authorization shape.
- **C6/C7/C8 retired outright, no successor:** C7's schedule-draft/
  pacing-policy tool surface (FR41) and C6's read-only YouTube-schedule
  tool `get_channel_schedule` plus `get_channel_overview`'s
  `SyncedSchedule` field (FR46) are both gone from the live registry --
  `mcp/server/registry_tools_test.go`'s
  `TestRegistry_RetiredScheduleDraftAndPacingTools_NotRegistered` and
  `mcp/tools/browse_integration_test.go`'s `get_channel_schedule`-retirement
  coverage assert this by name against the real tool registry, not by
  grepping deleted source. C6's capability-map text resolves to the "cut
  entirely" branch of its two-option pending text (FR46) -- no C20
  (decoupled read-only schedule view) is adopted as a replacement; the
  underlying `worker/sync` YouTube sync job is unaffected, only the two
  surfaces that presented synced data *as a schedule* are gone. C8 was
  already retired by the amendment above; FR47 additionally drops
  Strategy's `cadence` field, the one remaining input C7's
  `generate_schedule_plan` read.

**NFR3 amendment (M2, issue #1728): C11/C12/C13 are dual-surface, except
Channel-connect.** M2 adds three capabilities on top of M1's allocation
above:

- **C11 (multi-Channel management):** Channel-connect (FR25) stays
  `web`-only, for the identical OAuth-consent reason C2 always was -- there
  is no more "call this MCP tool to complete a Google consent screen" for
  a second Channel than there was for the first. The Channel list/switcher
  page (FR26, `GET /channels`) is `web`-only by the FR text itself (no MCP
  tool is named for it) -- distinct from `list_channels` (issue #1631,
  predating M2), an MCP tool that already answered a similar "which
  Channels can I see" question for an MCP-only client; M2 repoints
  `list_channels` onto the same `store.AccessStore.
  ChannelsWithRoleForPerson` query FR26's page uses (issue #1719), so the
  two now agree by construction, without FR26 itself requiring a new MCP
  tool.
- **C12 (cross-Channel aggregate, FR27/FR28):** dual-surface by FR27's own
  text -- `GET /my-work` (`web/main.go`'s `handleMyWork`) and `get_my_work`
  (`mcp/tools/my_work.go`) both call `store.MyWorkStore.
  SummariesForPerson` directly, re-deriving the caller's currently-open
  roles on every call (FR28) -- neither surface caches or requires a
  reconnect for a just-revoked Channel to disappear from the very next
  call.
- **C13 (three-tier authority, FR30/FR31/FR33/FR35):** invite Co-Creator,
  promote, remove, and the audit trail are each dual-surface -- `web/
  access.Handlers` (`GET/POST /channels/{id}/access...`) and `mcp/tools/
  access.go`'s `invite_co_creator`/`promote_to_co_creator`/
  `remove_channel_person` plus `mcp/tools/access_audit.go`'s
  `get_channel_access` call the identical `store.InviteStore`/
  `store.RoleStore`/`store.AccessStore` methods and the identical
  `store.CanInvite`/`CanRemove`/`CanViewAudit` authorization checks as
  their web counterparts -- the same "two independent, equally-capable
  front ends" relationship C8's amendment above established, not a
  primary/shadow pair.

The existing NFR3 list (C1/C2/C3 web-only; everything else MCP-exposed
too) and the C8 amendment above are both unchanged by this addition --
this is an appended clarification of three new capabilities' allocation,
not a rewrite of the ones already there.

**NFR3 amendment (M3, issue #1880): C14 is delivered as MCP-only, no
`web` surface.** `set_outcome_bar` (FR1, write) and `get_outcome_bar`
(FR2, read) manage the per-Channel outcome bar, and `get_calibration_trend`
(FR5/FR6/FR7, read) returns the bucketed calibration trend classified
against it (`mcp/tools/outcome_bar.go`, issues #1882-#1885); none of the
three has a `web` counterpart in this milestone. This matches C14's
roadmap allocation (`product/03-roadmap.md`'s M3 entry): M3's FR budget
is read-side-aggregate-plus-one-narrow-write, not a UI milestone, and
LB5's dual-surface parity mechanism explicitly schedules C14's `web`
surface for **M4.3**, not M3 -- so `web`'s current three-surface list
(C1/C2/C3) is genuinely unchanged by M3, unlike C11/C12/C13 and C18/C19
above, which each added `web` routes when they landed. Per LB5's own
text, this paragraph is the concrete MCP baseline (which store methods,
which `store.CanX` check -- `store.CanWrite`/`store.CanRead`, same tier
as every other write in this package, not Creator-only) that M4.3's own
`**NFR3 amendment (issue #<M4.3 root plan issue>): C14 is dual-surface.**`
paragraph will supersede once that milestone ships a `web` front end
calling these same `store.OutcomeBarStore`/`store.CalibrationStore`
methods.

**NFR3 amendment (issue #1896): C4 and C5 (save/browse slice) are
dual-surface.** `web` now renders a Channel research index and an Idea
detail page (`GET /channels/{id}/research`, `GET
/channels/{id}/research/ideas/{ideaID}`), plus save-note and
save-verdict forms (`POST /channels/{id}/research/notes`, `POST
/channels/{id}/research/ideas/{ideaID}/verdicts`) in
`audience_score_system/web/research`. This does **not** make C4/C5
web-only, and does not remove or narrow any MCP tool --
`save_research_note`, `list_research_notes`, `create_idea`,
`list_ideas`, `save_viability_verdict`, and `get_viability_verdict` are
unchanged.
NFR3's UI-only-surfaces rule (C1, C2, C3 as of this amendment -- since
grown to six, see the amendments at the end of this section) is untouched
by this paragraph: this amendment adds a second surface onto capabilities
that remain MCP-exposed, which is what NFR3 already permits ("whether or
not `web` also renders a UI for it"). The shared seams `web` and `mcp` both call
identically (LB5) are `store.ResearchStore.SaveNote`,
`store.VerdictStore.Append`, `store.VerdictStore.Current`,
`store.VerdictStore.History`, and the `store.CanWrite`/`store.CanRead`
authorization checks -- one implementation each. `source_url`
validation and the cited/uncited derivation live inside `store` for
exactly this reason (M4.1 FR12, #1897), so neither surface maintains
its own copy. `viability_verdict.source` (migration 015, FR5)
distinguishes an agent-authored version from a human-authored one --
`web/research`'s save-verdict form always writes
`store.VerdictSourceHuman` -- so dual-surface writes stay attributable;
it does not fork the write path. What stays MCP-only: the
research/viability *reasoning conversation* itself, per the standing
no-hosted-agent-loop non-goal -- `web` offers the save and browse
surface, it does not host an agent loop.

**NFR3 amendment (issue #1914): C18 is dual-surface.** `web` now renders
a propose form on the Idea detail page (`GET
/channels/{id}/research/ideas/{ideaID}`), submitting to `POST
/channels/{id}/research/ideas/{ideaID}/video-scripts`
(`audience_score_system/web/research.Handlers.HandleProposeVideoScript`,
issue #1915, FR1-FR5/NFR1-NFR3). This does **not** make C18 web-only, and
removes or narrows no MCP tool -- `save_video_script`
(`mcp/tools/video_script.go`) is unchanged. The shared seam `web` and
`save_video_script` both call identically (LB5) is
`store.VideoScriptStore.Propose` and the identical `store.CanWrite`
(Founder/Co-Creator/Analyst, matching `save_video_script`'s NFR13 tier)
authorization check -- the same "two independent, equally-capable front
ends" relationship the retired-C8 amendment above established, not a
primary/shadow pair. This **supersedes** the `#1823` amendment's C18
bullet above, which stated `save_video_script` is `mcp`-only; that
statement was true as of #1823 and is no longer true as of this
milestone, per the same "amendment record, never rewritten" convention
every prior paragraph in this section follows -- the `#1823` paragraph
itself is left untouched. For the record, C19 (greenlight/deny/archive)
and C10 (video-scripts browsing slice) are **unchanged** by this
milestone: both have been dual-surface since #1823/#1834
(`web/schedule.Handlers.HandleGreenlight`/`HandleDeny`/`HandleArchive`
and `HandleList`). M4.2's only allocation change is C18.

**NFR3 amendment (issue #1924): C9, C10 (prediction-vs-outcome slice),
and C14 are dual-surface.** `web` now renders `GET
/channels/{id}/outcomes` (`audience_score_system/web/outcomes`, issue
#1928) and `GET /channels/{id}/matches` plus `POST
/channels/{id}/matches/{matchID}/resolve` (`audience_score_system/web/
matches`, issues #1926/#1927), alongside `web/outcomes`'s inline
set-outcome-bar form (`POST /channels/{id}/outcome-bar`, issue #1929).
None of this makes C9, C10, or C14 web-only, and none of it narrows an
MCP tool -- `get_prediction_vs_outcome` (`mcp/tools/browse.go`),
`list_pending_matches`/`resolve_pending_match` (`mcp/tools/matches.go`),
and `get_outcome_bar`/`set_outcome_bar`/`get_calibration_trend`
(`mcp/tools/outcome_bar.go`) are unchanged. The shared seams `web` and
`mcp` both call identically (LB5) are `store.BrowseStore.
PredictionVsOutcome` (C10's prediction-vs-outcome slice, gated by
`store.CanRead` on both surfaces), `store.MatchStore.ListPending` and
`store.MatchStore.Resolve` (C9, `store.CanRead` for the list and
`store.CanWrite` for the resolve, on both surfaces), `store.
OutcomeBarStore.GetByChannel` and `store.OutcomeBarStore.Upsert` (C14's
read and write, `store.CanRead` and `store.CanWrite` respectively --
Creator, Co-Creator, or Analyst, not Creator-only), and `store.
CalibrationStore.MonthlyTrend` (C14's trend read, `store.CanRead`,
classified against the Channel's current bar only) -- the same "two
independent, equally-capable front ends onto the same store methods and
the same authorization checks" relationship the retired-C8 amendment
above established: not a primary and a read-only shadow, so an
agent-only client and a browser-only user have the same authority. This
**supersedes** the `#1880` amendment's statement that C14 is MCP-only
with no `web` surface -- that statement was true as of M3 and is no
longer true as of this milestone, per the same "amendment record, never
rewritten" convention every prior paragraph in this section follows; the
`#1880` paragraph itself is left untouched. Note FR5's flagged scope
extension: `set_outcome_bar`'s write is now dual-surface, which the
M4.3 roadmap entry did not originally anticipate (see #1924's FR5 scope
note).

**NFR3 amendment (issue #1934): thread discovery/save, typed relations,
`current_only` filtering, and the cited-notes superseded/excluded warning
are dual-surface.** This plan (root #1934) adds no new capability number
(product/02-capability-map.md, root plan "Out of scope") -- it amends
already-dual-surface C4 (write) and C10 (browse), and touches C5's
citation-warning surface, so this paragraph records FR-level allocation
rather than a capability-list change. None of the following is
implemented once for `mcp` and once, differently, for `web` (NFR2/LB5) --
each bullet names the one `store` method and the one `store.CanX` check
both surfaces call:

- **Thread discovery + find-or-create (FR3/FR4):** `list_research_threads`
  (`mcp/tools/research.go`, issue #1937) and `web/research`'s thread
  select on the save-note form (`web/research.Handlers.HandleChannelIndex`/
  `HandleIdeaDetail`, issue #1945) both call `store.ThreadStore.
  ListByChannel` under `store.CanRead`. On the save path, `save_research_note`
  (issue #1938) and `HandleSaveNote` (issue #1945) both resolve-or-create
  the thread via `store.ThreadStore.FindOrCreate`, called from inside
  `store.ResearchStore.SaveNote`'s own transaction so thread creation and
  note insert commit atomically -- gated by `store.CanWrite` on both
  surfaces.
- **Relation-typed save (FR5):** `save_research_note` (issue #1938) and
  `HandleSaveNote`'s relation picker (issue #1945) both write through the
  same `store.ResearchStore.SaveNote` call (its `Relations` field), under
  the identical `store.CanWrite` check every other write in this package
  uses.
- **The `current_only` filter (FR8):** `list_research_notes`'s
  `current_only` argument (issue #1941) and `web/research`'s browse pages
  (index and Idea detail) both read through `store.ResearchStore.
  ListFiltered`, gated by `store.CanRead`.
- **Verdict cited-notes rendering (FR9) and its superseded/excluded
  warning (FR10):** `get_viability_verdict`'s `resolveCitedNotes`
  (`mcp/tools/verdict.go`) and `web/research.Handlers.renderIdeaDetail`'s
  `retiredCitedResearchNotes` (issue #1943 renders the base cited-notes
  list, issue #1944 adds the warning) both resolve a Verdict's cited-note
  id union and then call `store.ResearchStore.RetiredNoteIDs` ONCE over
  that union, gated by `store.CanRead` -- the same batched-resolution
  shape on both surfaces, so `mcp` and `web` can never disagree on which
  notes are flagged retired.
- **Relation visibility on ordinary browse (FR11):** `get_channel_overview`'s
  relations section (`mcp/tools/browse.go`, issue #1942) and
  `web/research`'s Related lines on the index/Idea-detail pages
  (`Handlers.relationsForNotes`, issue #1942) both batch-resolve via ONE
  `store.ResearchStore.ListRelationsForNotes` call, gated by
  `store.CanRead`.

This is the same "two independent, equally-capable front ends onto the
same store methods and the same authorization checks" relationship the
retired-C8 amendment above established -- not a primary/shadow pair --
extended to a fifth capability slice without adding a sixth. The
existing C4/C5/C10 dual-surface amendments above (#1896, #1911, #1924)
are unchanged by this addition.

**NFR3 amendment (issue #2036, part of #2027): C18 gains a second web
propose surface; C18 remains dual-surface.** `web` now also renders a
dedicated video-script authoring page, `GET /channels/{id}/scripts/new`
(`web/schedule.Handlers.HandleNewScript`) submitting to `POST
/channels/{id}/scripts` (`HandleCreateScript`) -- unlike the #1914
amendment's propose form, which is reached from a specific Idea's detail
page and does not accept a Verdict choice, this page is Channel-scoped
rather than Idea-scoped and lets the caller pick any of the Channel's
viable verdicts via a Verdict select. Both web forms, and `save_video_script`,
call the identical `store.VideoScriptStore.Propose` method under the
identical `store.CanWrite` (Founder/Co-Creator/Analyst) check -- the same
"two independent, equally-capable front ends" relationship the retired-C8
amendment above established, now with **two** web entry points onto the
same seam instead of one. This does not add a new UI-only surface (C18
propose was already dual-surface as of #1914) and does not narrow
`save_video_script` -- it reconciles the `web` component-table row (~line
271 before this split) with what shipped, per this issue's own scope note.

**NFR3 amendment (issue #2037, PR #2069, part of #2027): C18 gains a
web-only edit capability -- the fourth UI-only surface, and the first
that is not an OAuth-consent flow.** `POST /channels/{id}/scripts/{scriptID}`
(`web/schedule.Handlers.HandleUpdateScript`) calls a genuinely new store
method neither surface had before, `store.VideoScriptStore.UpdateContent`
(`store/video_script.go`), which overwrites a `video_script`'s title and
script text (FR16/FR17) under the same `store.CanWrite` check propose
uses, frozen once the script leaves `proposed` status or the matched video
has published (`ErrVideoScriptDecided`, checked both at read time to hide
the edit affordance and again inside `UpdateContent` itself so the two
checks can never disagree). No `mcp` tool is added for this: editing is
scoped to the interactive, side-by-side-with-research workflow (#1956)
this batch serves, and an MCP-capable agent already has `save_video_script`
(propose) plus `greenlight_video_script`/`deny_video_script`/
`archive_video_script`. This makes script-content editing a UI-only
surface next to C1/C2/C3's OAuth-consent exceptions -- the first of the
four UI-only surfaces (now six, with C20/C21 below) that is not an OAuth
flow. A follow-up test-coverage-only commit for this store method exists
on branch `pm2-2027/2037-video-script-edit-freeze-tests`, not yet merged
to `main` as of this amendment -- the store method and `web` handler
themselves are already on `main` (PR #2069), so this amendment documents
them as shipped; the pending branch adds no behavior, only tests.

**NFR3 amendment (issues #2038 and #2031, part of #2027): C20 and C21 are
deliberately web-only, no MCP mirror planned -- the fifth and sixth
UI-only surfaces.** Both are new FR23-FR30 capabilities with no capability-map
entry added by this doc (that addition is out of scope for issue #2039 --
see `product/02-capability-map.md`, amended separately):

- **C20 (channel dashboard, issue #2038):** `GET /channels/{id}`
  (`web/main.go`'s `handleChannelDetail`) renders a recent-activity section
  -- 24h/7d counts plus an outcome-classification trend -- computed by
  `store.DashboardStore.ChannelActivity` (`store/dashboard.go`) against a
  single request-time `now`. This is a windowed-aggregate UI convenience
  over data an MCP agent can already reach row-by-row via
  `get_channel_overview`/`get_prediction_vs_outcome`; a windowed-count
  aggregate has no clear agent use case today, so no MCP tool is added.
- **C21 (published-videos browse, issue #2031):** `GET
  /channels/{id}/videos` (`web/videos.Handlers.HandleList`) renders a
  human-filterable (title/date-range/sync-status) browse over
  `store.SyncStore.ListPublishedWithMetrics` (`store/sync.go`), gated by
  `store.CanRead`. A name/date/sync-status filter is a UI convenience for a
  human scanning a list; an MCP agent already reaches the same underlying
  rows via `get_channel_overview`/`get_prediction_vs_outcome`, so no MCP
  tool is added.

Neither surface removes or narrows an existing MCP tool. Both are
deliberate, not deferred: unlike C14's #1880 amendment (MCP-only,
`web` surface explicitly scheduled for a later milestone), no future
milestone is currently scoped to add an MCP tool for either. With this
addition, `web`'s UI-only-surface count grows from three to six: C1, C2,
C3 (OAuth-consent), C18's edit slice (#2037), C20 (#2038), and C21
(#2031) -- see the summary at the top of this section, corrected to match
by this same amendment.

