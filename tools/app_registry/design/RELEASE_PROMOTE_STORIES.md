# Release + Promote — User Stories

Source material for AR-5+/AR-8+ design work on the **release/publish** side of
the registry (as opposed to [USER_STORIES.md](USER_STORIES.md), which is
scoped to the admin UI's day-2 promote/rollback/browse jobs). Written from
issue #596: capture the release + promote workflow as stories so gaps between
"what we do today" and "what a domain owner actually needs" are explicit
instead of implicit in `release_helper_go` and CI YAML.

Each story is marked:

- **Shipped** — the behavior exists today; the story documents it so it isn't
  re-litigated.
- **Gap** — the behavior does not exist yet; no phase in [PLAN.md](PLAN.md)
  currently owns it.

## Persona

**Domain owner** — writes `release_app`/`release_helm_chart` manifests, cuts
releases for one or more domains, and is the one who finds out (via a failed
CI run or a confused teammate) when the release system's assumptions about
"one app, one chart, one build" don't hold for their domain.

## Epic A — Image-only and chart-only builds

Today a release run builds and publishes every affected image and chart
together in one pass (`docs/RELEASE.md` "How It Works"). Two situations need
the two halves to move independently:

1. A shared microservice image is unchanged, but a chart that composes it
   needs a new release (e.g. a values/config-only chart bump).
2. An image changed, but nothing needs to re-render or re-push any chart yet
   (e.g. releasing an image to be picked up by a chart in a later, separate
   release).

26. As a domain owner, I want to release a chart without forcing a rebuild of
    every image it pins, so a chart-only change (values, chart metadata, a
    config default) doesn't produce a needless image rebuild-and-republish
    cycle. — **Shipped, with a caveat.** Bazel's affected-target detection
    already limits a release matrix to what changed (`docs/RELEASE.md`
    "Intelligent Change Detection"); a chart-only diff does not touch its
    pinned images' `release_app` targets, so they are not rebuilt. The
    caveat: this is a side effect of dependency-based change detection, not a
    first-class "image-only / chart-only" mode a domain owner can request
    explicitly — see #27 below for the case where that distinction has to be
    forced rather than inferred.

27. As a domain owner, I want to force-release an image on its own — with no
    dependent chart re-composed or re-pushed in the same run — so I can
    publish a new version of a shared microservice ahead of the charts that
    will eventually pin it, without those charts drifting or re-releasing
    early. — **Gap.** `release_helper_go`'s `--apps` filter (docs/RELEASE.md)
    can already scope a manual run to one app, but nothing in the plan step
    distinguishes "release this image, and stop" from "release this image,
    then compose/publish every chart that pins it" — chart releases are
    driven by their own change detection, not explicitly chained off an
    image release. Needs reconciling: does releasing an image in isolation
    leave dependent charts pointing at the old digest until *they* release
    (current behavior, implicit), or should the release system offer an
    explicit "and re-release dependent charts too" step? Undecided — flagging
    for AR-5+/AR-8+ scoping rather than resolving here.

## Epic B — One microservice, many charts

`ARCHITECTURE.md` "Promotability" already models this at the schema level: an
image artifact can be `contains`-referenced by more than one chart artifact,
and each chart records its own lockfile/`contains` independently
(`ARCHITECTURE.md` "Chart → image lockfile"). This epic is about whether the
*release path*, not just the data model, actually supports it end to end —
the manmanv2-host-manager case in `ARCHITECTURE.md`'s "Override" note is the
existing example of one app being deployed outside its chart's normal path.

28. As a domain owner, I want the same microservice image to be included by
    two or more different charts (e.g. a shared host-manager image reused
    across per-environment or per-tenant chart variants), and have each
    chart's promotion state tracked independently, so promoting chart A to
    stage doesn't imply anything about chart B. — **Shipped at the data
    model.** `artifact.contains` is per-chart-artifact, and promotion is
    keyed on `(owner, environment)`, not on the image — two charts pinning
    the same image digest promote and roll back completely independently.
    Not yet exercised by a real domain with two charts sharing one image;
    a future validation pass should pick a case like this to check against
    real CI load, not just schema inspection.

29. As a domain owner, I want to see every chart that currently pins a given
    image, so when I'm about to change or deprecate that image I know the
    full blast radius before I touch it. — **Shipped** — `USER_STORIES.md`
    #19 ("one click back to every app it publishes") already covers the
    admin-UI side of this; the underlying data (`contains`, reverse-lookup by
    digest) exists in the schema this epic depends on. Listed here too
    because it's exactly the question this epic's scenario raises.

30. As a domain owner, I want a chart-composition failure ("this chart pins
    an image digest the registry never recorded published") to name *which*
    chart and *which* pinning is wrong, not just that hermeticity failed, so
    a shared image used by several charts doesn't turn one bad release into
    an undiagnosable multi-chart outage. — **Shipped.** `AR-7f`'s
    `CheckChartHermeticity` (PLAN.md) and the record-time reject in
    `ARCHITECTURE.md` "Chart → image lockfile" both fail per chart artifact,
    scoped to that chart's own `contains` list.

## Epic C — Redundant (no-op) rebuilds

Captures the feature idea from issue #596: a rebuild that reproduces an
existing published digest should not be treated, or logged, as a new
release.

31. As a domain owner, I want a build that reproduces a digest the registry
    has already published for that same `(owner, kind, version)` to be
    recorded as an idempotent success, not a conflict, so routine
    "rebuild-everything-in-the-domain" release batches (`PLAN.md`'s "Current
    status", issue #585) don't fail loudly for the apps that happened not to
    change. — **Shipped.** `ARCHITECTURE.md` "Artifact lifecycle": "`published`
    is terminal. Re-recording the same digest is an idempotent success" —
    fixed for the `(owner, kind, version)`-scoped case by issue #585 (PLAN.md
    "Current status").

32. As a domain owner, I want a build that reproduces the digest of an
    *older* published version of the same app — not the version currently
    being released — to be recognized and logged as a redundant/no-op build
    rather than either silently publishing a duplicate-content new version or
    hard-failing the release, so CI output makes it obvious nothing actually
    changed. — **Shipped, upstream of App Registry.** PR
    [#630](https://github.com/whale-net/everything/pull/630) fixed this in
    `release.yml` itself: tag-creation steps check the just-built digest
    against the most recent existing tag's digest before minting a new
    version tag, and skip tag creation (and the App Registry record call)
    on a match — so a no-op rebuild never reaches App Registry as a "new"
    artifact, and the `artifact_digest_idx` hard-fail this story describes
    no longer happens in practice. See `PLAN.md`'s "Version allocation
    (AR-5)" for the full account.

33. As a domain owner, I want a redundant build to still be visible in the
    release run's output (e.g. `app-registry builds status`), labeled as
    redundant, rather than either indistinguishable from a normal publish or
    silently absent, so I can tell "nothing changed" from "this app was
    skipped" from "this app published normally" at a glance. — **Gap.**
    `PLAN.md`'s `builds status` (`AR-7d`) already models per-artifact state
    (`PUBLISHING`/`PUBLISHED`/`FAILED`/`ALLOCATED`, `USER_STORIES.md` #12);
    it has no `REDUNDANT`/no-op state. Depends on #32 landing a server-side
    decision for what a same-content/different-version request *is* before a
    new displayable state can be added on top of it.

## Open reconciliation questions (issue #596)

Carried forward rather than answered here, since they're design decisions for
whoever picks up AR-5+/AR-8+, not facts to assert:

- Whether "release an image only" (#27) should stay implicit
  (dependency-driven change detection) or become an explicit release-plan
  option.
- Whether a redundant build (#32) should be recorded as a *new* artifact row
  with a `redundant`/`provenance`-style marker, or as a reference from the
  new version to the existing digest's row with no new row at all — affects
  `artifact_digest_idx` and every `(owner, kind, version)` uniqueness
  assumption elsewhere in the schema (`ARCHITECTURE.md` "Data model").
