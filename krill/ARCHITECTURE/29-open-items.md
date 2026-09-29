# Open items

- The HTTP surface over the spec entity model covers create/attach, amend,
  and history reads (issue #2490: `POST /products`, `/feature-sets`,
  `/features`, `/requirements`, `/load-bearing-decisions`; FR 7a3906a3:
  `POST /personas` and `POST /non-goals`, which closed the create half of
  the Persona/NonGoal gap; issue #2493:
  `POST /requirements/{id}/amend`, `POST
  /load-bearing-decisions/{id}/amend`, `GET /requirements/{id}/as-of`,
  `GET /requirements/{id}/versions`, and the `load-bearing-decisions`
  equivalents) — so a create is now reachable for every spec-axis kind an
  actor can also correct, but Persona and NonGoal still have **no HTTP read
  surface at all** (no `GET /personas/{id}`, no list route; `krill/store`'s
  `PersonaStore`/`NonGoalStore` reads are still reached only over MCP or
  the store). The MCP surface gap is closed for create
  (`create_persona`/`create_non_goal`, entity.go) and list
  (`list_personas`/`list_non_goals`, persona_nongoal.go — neither entity is
  part of the FR5-FR9 slice document, so this is a standalone discovery
  pair, not a `Document` field) on the design mount. Amend and void do exist
  for both over HTTP (`POST /personas/{id}/amend` and `POST
  /non-goals/{id}/amend`; the void routes alongside them), and there is no
  amend/history surface for Product, FeatureSet, or Feature (issue #2493
  scopes FR11/FR12 to Requirement and LoadBearingDecision only). FR5-FR9's
  read path exists (issue #2491, see "The scoped-slice query" above); FR21
  wires `/project-manager:design --milestone`'s krill-domain read to it
  (issue #2500, see "The design skill's live milestone read" above) —
  a `Delivers`-only version of "filter that read to just one milestone" now
  exists (`slice.Querier.GetMilestoneDeliversSlice`, issue #2721, see "The
  task payload document" above), reached only through the task payload
  fetch/claim path today; still open: a standalone, MCP-exposed
  `Delivers`+`Must not foreclose` version (M3's C13/C28) for a caller that
  isn't fetching a task.
- `krill/slice`'s four granularities each have an as-of assembly twin now
  (issue #2493, see "As-of slice assembly" above) — but no HTTP route
  exposes them yet (`krill/api/handlers/slice.go` still wires only the
  current-row four); that surface, and any MCP tool built over it, remain
  open for a later task.
- `init` (FR3, #2489) and the write-only gate (`api/handlers/session.go`,
  `api/handlers/gate.go`) cover entity creates and LB attach as of #2490,
  amend as of #2493 (`api/handlers/amend.go`), pointer-issue create as of
  #2496 (`api/handlers/pointer.go`), and M2's open-DesignSession/append-
  RevisionEvent pair as of #2543 (`api/handlers/design_session.go`,
  `revision_event.go`, see "The DesignSession/RevisionEvent HTTP surface"
  above) — every write path *this milestone's HTTP surface* defines is now
  wired, and, as of issue #2547, so is the MCP surface for the same
  open/append/propose capability (`RegisterWrite`, the `/mcp/design` mount
  — see "The design-session MCP surface" above). Import (#2492) is gated
  too, but as a CLI entrypoint checking the session directly against the
  store rather than through this HTTP middleware (see "The markdown
  importer" above).
- `krill/mcp` (issue #2494) now exists and wraps `krill/slice` directly,
  per LB7 (see "The MCP spec surface" above) — its auth (human) front
  door now has both its verification-side migration (`006_mcpauth_credential`)
  and a mint-side `/authorize`/`/token` surface (`krill/ui`, see
  "krill/ui and the auth front door" above); persona resolution
  (`auth.go`) still always resolves `PersonaSwarmOperator`, unconditionally,
  until C12 lands. As of issue #2547, `krill/mcp` also mounts the
  design-session write/read surface at `/mcp/design` (see "The
  design-session MCP surface" above) — its persona resolution is the same
  fixed `auth.go`/`whagent_auth.go` pair, with a per-tool allow-list
  (`RegisterWrite`) restricting `propose_entities` to `PersonaAgent`/
  `PersonaSwarmOperator` (widened from `PersonaAgent`-only by issue #2926,
  which had made the tool unreachable from any mcpauth-authenticated
  caller, including every krill-design/krill-work subagent).
- `api` now has an auth front door (see `16-init-and-write-gate.md`);
  `init` derives identity and scope from the verified credential.
- The renderer (`krill/render`, issue #2495) covers FR13-FR15/NFR3 as of
  this task; see "The doc renderer" above. No hook or schedule triggers it
  automatically yet — `bazel run //krill/render/cmd:render` is a manual,
  Swarm-Operator-run step, same shape as the importer.

## Known gaps: authenticated api

- `krill/Tiltfile` local dev is not configured for the authenticated `api`
  (no Keycloak, roles, or credential wiring); scope note c5ef6e96.
- The machine `client_credentials` token source (`krill/apiclient`) has no
  in-repo caller yet.
- The Keycloak `act.agent_id` claim is dropped; only `sub`/`iss` are kept.
- There is no credential-to-scope mapping: with more than one scope,
  `init` refuses (`ScopeStore.GetSole` fails) rather than choosing one.
