# App Registry Admin UI — User Stories

Source material for `design/wireframes/`. One persona, two jobs — see
[PRINCIPLES.md](PRINCIPLES.md) for the design principles these stories imply.

## Persona

**Registry Admin** — operates releases day to day. Most sessions are short
and routine ("is prod caught up? promote this."). Some sessions are stressful
and open-ended ("a release job went red, what actually happened, and is
anything actually broken?"). Same person, same login — the UI has to serve
calm-day and bad-day equally well, not pick one.

Every story below is scoped to what the registry actually knows — it records
and answers questions; it does not deploy anything (README.md). No story
implies a "deploy" or "sync" button.

## Epic A — At a glance: what's deployed

1. As the admin, I want a single screen listing every environment with what's
   currently promoted, so I can answer "what's live?" without running a CLI
   command per environment.
   - Acceptance: one matrix — a row per promotable entity, a column per
     environment (dev/stage/prod/…) — shows version and promotability at a
     glance for everything, everywhere, without switching tabs or re-running
     a per-environment lookup. Maps to `app-registry status <env>` run once
     per environment and rendered together.

1a. As the admin, I want a chart's composed apps available without them
    cluttering the top-level list, so the matrix stays scannable on a calm
    day but still answers "what exactly is in this chart" on a bad one.
    - Acceptance: only `PROMOTABLE` entities (charts, and standalone
      `image`-deploy-unit apps) are top-level rows; a chart's `VIA_CHART`
      apps are reachable by expanding that row, never listed as their own
      top-level row.

2. As the admin, I want environments that have drifted (an overridden image
   no longer matching its chart's pin) to be visually loud on the overview,
   not something I have to go looking for.
   - Acceptance: a drift count/badge is visible on the dashboard and on the
     affected entity's row before I click into anything. Maps to `status`'s
     `DriftEntry` banner.

3. As the admin, I want to see what was deployed at a past point in time, not
   just right now, so I can answer "was this version live during the
   incident window?"
   - Acceptance: the deployments matrix accepts an "as of" instant and shows
     the SCD2 snapshot for that time, across every environment column at
     once. Maps to `app-registry status <env> --at <time>`.

3a. As the admin, I want every promote/rollback action to be scoped to the
    exact environment I clicked, with no environment-selection step in
    between, so I can't promote the right app to the wrong environment by
    picking the wrong option in a dropdown or losing track of which tab I'm
    on.
    - Acceptance: each environment column's cell carries its own
      promote/rollback controls; the promote/rollback layers open already
      scoped to (entity, environment) and never ask the admin to choose an
      environment.

## Epic B — Promotion

4. As the admin, I want to promote an app to an environment from the same
   screen where I see its current state, so promoting doesn't require
   context-switching to a terminal.
   - Acceptance: a "Promote" action opens inline from the environment status
     row; the form is pre-scoped to that app/environment. Maps to
     `PromotionRegistry.Promote`.

5. As the admin, I want the promote form to only offer versions/apps that are
   legal to promote, and to clearly warn me when something is not, so I don't
   discover a rejection after I've already filled out a reason.
   - Acceptance: `NOT_PROMOTABLE` artifacts are not selectable; `VIA_CHART`
     artifacts require an explicit "override" acknowledgment before the
     submit button is enabled. Maps to promotability + `--allow-override`.

6. As the admin, I want a reason to be required for any promotion, and
   required (not just present) above `dev`, so the history is
   self-explanatory later without me having to remember context.
   - Acceptance: reason field cannot be submitted empty for stage/prod; the
     UI states why the field exists rather than treating it as boilerplate.

7. As the admin, I want to dry-run a promotion above `dev` before committing
   it, so I can see the resulting state without writing anything.
   - Acceptance: dry-run is the default toggle state for stage/prod promotes;
     the result of a dry run is visually distinct from a real promotion
     (never looks like a completed write). Maps to `Promote(..., dry_run)`.

## Epic C — Rollback

8. As the admin, I want to roll an environment back to what it ran
   immediately before the current promotion, in one action, without having
   to look up a version number myself.
   - Acceptance: rollback shows the exact target (the superseded promotion)
     before I confirm — app, version, digest, when it was last live. Maps to
     `app-registry rollback` (no version argument; SCD2-history-derived).

9. As the admin, I want rollback to clearly refuse when there's nothing to
   roll back to, rather than silently doing nothing.
   - Acceptance: an environment/app with no promotion history shows rollback
     as disabled with the reason stated inline, not just a greyed-out button.

## Epic D — History & cross-environment comparison

10. As the admin, I want the full promotion history for one app — across
    environments and over time — so I can trace "when did prod last change,
    and what was it before that."
    - Acceptance: a per-app history view lists every promotion event with
      actor, reason, timestamp, and environment. Maps to
      `app-registry history <domain-name> --env <env>`.

11. As the admin, I want to compare two environments side by side, so I can
    answer "is stage ahead of prod, and by what" without mentally diffing two
    separate screens.
    - Acceptance: a diff view lists every app present in either environment
      with its version in each side, highlighting mismatches. Maps to
      `app-registry diff <env-a> <env-b>`.

## Epic E — Troubleshooting a failed or incomplete release

12. As the admin, I want to look up a specific CI run by its workflow run id
    and see every artifact it touched with a per-artifact state, so I can
    tell exactly what did and didn't finish.
    - Acceptance: a build-detail view lists each artifact's state
      (`PUBLISHING` / `PUBLISHED` / `FAILED` / `ALLOCATED`) with a filter for
      "incomplete only." Maps to `app-registry builds status <run-id>
      [--incomplete] [--attempt N]`.

13. As the admin, I want a run that is stuck or partially failed to show
    *which* artifacts are the problem, not just that the run had a problem,
    so I know exactly what to re-run instead of re-running everything.
    - Acceptance: every non-`PUBLISHED` row is visually flagged with its
      state and, for `FAILED`, its `fail_reason` (e.g. `"stale"`).

14. As the admin, I want to see, for a given release run, whether the "App
    Registry recording health" step passed or failed, distinct from whether
    the release itself succeeded, so a red health step doesn't make me think
    the release failed.
    - Acceptance: recording health is its own labeled status, never merged
      visually with overall release/run status.

15. As the admin, when I need to fix a stuck or pre-registry artifact, I want
    a clearly separate, admin-only "adopt" action that forces me to state a
    reason and confirms exactly what I'm asserting, so this rare and
    consequential action can't be mistaken for a routine one.
    - Acceptance: adopt is reached only from a troubleshooting context (never
      from the routine promote flow), requires a reason, and its result is
      visually tagged as `provenance: adopted` everywhere it later appears.
      Maps to `ArtifactRegistry.AdoptArtifact` (admin-role only).

16. As the admin, I want to see every adopted artifact in one place, so I can
    audit how much of what's "published" was actually observed by CI versus
    asserted by a human.
    - Acceptance: a filterable list of `provenance = adopted` artifacts,
      scoped optionally to one app. Maps to `app-registry artifacts list
      --provenance adopted`.

## Epic F — Catalog

17. As the admin, I want to browse every registered app/chart with its
    declared deploy unit (`chart` / `image` / `none`), so I understand what
    "promotable" even means for a given app before I try to promote it.
    - Acceptance: a catalog view lists every app/chart with domain, deploy
      unit, and current promotability per environment. Maps to
      `app-registry apps list` / `apps get`.

18. As the admin, I want to see which images a chart pins (and their
    digests), so when a chart-level promotion looks wrong I can inspect what
    it's actually made of without leaving the UI.
    - Acceptance: an artifact detail view shows `contains` for chart
      artifacts, each linking to that image artifact's own detail.

19. As the admin, from an app's page I want one click to the chart that
    publishes it (when it's `VIA_CHART`), and from that chart's page one
    click back to every app it publishes, so I never have to hold the
    app↔chart relationship in my head or go find it myself.
    - Acceptance: an app whose deploy unit involves a chart shows a "part of
      chart" link; the chart's own page lists every app it currently
      publishes, each linking back to that app's page.

20. As the admin, I want every artifact CI has ever recorded for an app in
    one place, separate from the promotion timeline, so I can answer "what
    versions exist" without it being tangled up with "what's been promoted
    where."
    - Acceptance: a version-history view lists every recorded artifact for
      one app (version, digest, provenance, build, current promotion state
      if any), reachable with one click from that app's page.

## Epic G — Troubleshooting the identity pipeline (reconcile)

21. As the admin, I want to see the history of `ReconcileApps` sweeps
    (applied vs. rejected as stale) separately from release/build
    troubleshooting, so a "why didn't my app/chart identity update" question
    doesn't get mixed up with "why didn't my release publish."
    - Acceptance: a reconcile-runs view lists each sweep's commit, outcome,
      and which app/chart manifests actually changed, linking to the
      affected app/chart. A rejected sweep states plainly that this is
      expected behavior (stale watermark), not a failure. Maps to AR-8's
      `reconcile_run` + manifest history tables (#592).

## Epic H — Environment administration

Environments are a row, not an enum (README.md "Core concepts") — a new
region or an ephemeral environment is meant to be an insert, not a release.
Rare, but real, and admin-only (`app-registry-admin` role, per
ARCHITECTURE.md "Authorization").

22. As the admin, I want to see every environment the registry knows about —
    including archived ones — in one place, separate from what's currently
    deployed to them, so "what environments exist" and "what's live" don't
    get tangled into one screen.
    - Acceptance: an environments view lists key, display name, rank,
      requires-approval, allowed principals, and gitops path, with an
      explicit toggle to include archived rows. Maps to `app-registry env
      list [--include-archived]`.

23. As the admin, on the rare occasion I need to add a new environment (a
    new region/canary/ephemeral env), I want one form that captures everything
    it needs — key, rank, gitops path, allowed principals — rather than
    hunting for the right CLI flags.
    - Acceptance: an add/edit form mirrors `env upsert`'s fields exactly,
      including ones that don't do anything yet (see #24). Maps to
      `app-registry env upsert <key>`.

24. As the admin, I want the UI to tell me plainly when a field I can set
    doesn't actually do anything yet, so I don't waste time debugging an
    approval gate that was never wired up.
    - Acceptance: `requires_approval` is shown and settable (it's a real
      column), but labeled inert until the approval workflow ships — see
      ARCHITECTURE.md "Future: approval gate."

25. As the admin, I want archiving an environment to require a reason and be
    clearly separated from routine edits, so it's treated with the same
    care as any other destructive-adjacent action in this UI.
    - Acceptance: archive lives in a danger-zone section of the same
      edit form, requires a reason, and states that promotion history is
      retained (nothing is hard-deleted). Maps to `app-registry env archive
      <key> --reason`.

## Epic I — Unauthenticated CI & developer discovery (#853)

Public / unauthenticated read paths allow CI workflows, local developer CLI
tools, and deployment engines to discover catalog metadata, artifact digests,
and environment states without Keycloak credentials.

26. As a CI runner in a pull request or external fork, I want to query active
    promoted artifact versions for an environment (`GetEnvironmentState`,
    `GetApp`) without Keycloak OIDC client credentials, so release tools and
    dependency resolvers work out of the box without requiring repo secrets.
    - Acceptance: `GetApp`, `ListApps`, `GetEnvironmentState`, and
      `ListPromotions` return valid responses when called with no `authorization`
      header or bearer token.

27. As a developer using the CLI locally, I want to browse the application
    catalog (`ListApps`, `ListCharts`) and inspect artifact versions
    (`GetArtifact`, `ListArtifacts`, `ResolveArtifact`) without setting up local
    OIDC tokens, so read discovery is as frictionless as querying a public
    container registry.
    - Acceptance: all read RPCs succeed anonymously; mutation and promotion
      endpoints return `codes.Unauthenticated` if no token is provided.
