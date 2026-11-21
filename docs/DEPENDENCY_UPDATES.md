# Dependency Updates — Strategy & Rollout Plan

**Status: proposed.** Nothing below is wired up yet. Today the repo has no
update tooling: no Renovate config, no Dependabot config, and no vulnerability
scanning. For *adding* a dependency by hand, see [DEPENDENCIES.md](DEPENDENCIES.md).

## Goal

Keep every pinned external version current without anyone having to remember
to do it:

- Bots find updates and open PRs.
- CI validates those PRs.
- An agent (Claude Code, agy) does triage, fixes breakage, and handles the
  bespoke pins that no bot understands.

## Architecture: three layers

| Layer | Tool | Handles |
|-------|------|---------|
| 1. Detect + PR | **Self-hosted Renovate** on a scheduled GitHub Actions workflow ($0: Actions minutes are free on public repos) | Native managers, plus custom regex managers for the bespoke pins |
| 2. Fix up | Renovate **`postUpgradeTasks`**, which run Bazel on the same runner | The Bazel follow-up steps (tidy, gazelle, lockfile, sha256), committed into the same update PR |
| 3. Triage + repair | **`/deps` agent skill**, run from a harness (optionally on a weekly `/schedule`) | Failing PRs, majors, pins that can't be automated, and a weekly summary |

Vulnerability signal runs alongside all three: Renovate's OSV alerts plus
`osv-scanner` in CI.

### Why Renovate, not Dependabot

Dependabot has no Bazel support. Renovate natively understands:

- `.bazelversion`
- `bazel_dep`, `git_override`, and `oci.pull` (tag+digest) in `MODULE.bazel`
- `go.mod`, `pyproject.toml` + `uv.lock`
- GitHub Actions, including the composite actions in `.github/actions/`
- devcontainer features and Dockerfile `FROM`

Renovate also has:

- `customManagers` (regex) for the bespoke pins
- grouping, to keep coupled pins in one PR
- `minimumReleaseAge`, as a supply-chain cooldown
- a **Dependency Dashboard** issue, which becomes the single "am I on top of
  this?" view

## Dependency surfaces

| Surface | Where | Coverage |
|---------|-------|----------|
| Bazel version | `.bazelversion` (8.3.1) | Renovate native |
| Bazel modules (~17), protobuf override, rules_pycross archive_override | `MODULE.bazel` | Renovate native (`bazel-module`) |
| pigweed `git_override` (bare commit) | `MODULE.bazel` | Renovate native, via git-refs digest; verify in the dry run |
| OCI bases (ubuntu, buildpack-deps), digest-pinned | `MODULE.bazel` `oci.pull` | Renovate native |
| Go module (~67 direct deps) | `go.mod` | Renovate native, **plus fixup** (gazelle, `bazel mod tidy`) |
| Python (39 deps / 157 locked) | `pyproject.toml`, `uv.lock` | Renovate native |
| GitHub Actions (tag-pinned) | `.github/workflows`, `.github/actions` | Renovate native |
| devcontainer base image and features | `.devcontainer/` | Renovate native, after pinning versions |
| Toolchains: `go_sdk` 1.25.0 ↔ `go` directive; Python 3.13 in 4 places | `MODULE.bazel`, `go.mod`, `.bazelrc`, `tools/bazel/container_image.bzl` | **Custom regex**, grouped so they move together |
| `http_archive`/`http_file` GitHub-release pins: helm, xtensa GCC, arduino-esp32 ×2, pubsubclient, python-build-standalone ×2, openapi-generator (Maven) | `MODULE.bazel` | **Custom regex, plus fixup to recompute sha256** |
| Coupled pin `_GCC_VERSION` | `tools/firmware/esp32/BUILD.bazel` | Custom regex, same `depName` as the xtensa archive |
| CDN pins: htmx, alpinejs, tailwind browser, daisyui (copied across ~12 Go/templ/test files) | `libs/go/htmxbase`, `*/ui`, `tools/wireframe` | **Custom regex** (npm datasource), after centralizing |
| Tilt images (`temporal`, `timescaledb:latest-pg16`, `minio:latest`) and floating helm repos | `tools/tilt/*.tilt`, `leaflab/Tiltfile` | **Custom regex**, after pinning |
| Dockerfile ARGs (`BAZELISK_VERSION`, `K3D_VERSION`); `GRPCURL_VERSION` ×2 | `.devcontainer/`, `.github/` | Custom regex |
| `azure/setup-helm` `version: 'latest'` | release workflows | Pin it and add a regex |
| Debian/Ubuntu snapshot `.deb`s (ca-certificates, lib32gcc-s1, libc6-i386) | `MODULE.bazel` | **Agent-only**: there's no meaningful "latest" |
| `steamcmd_linux.tar.gz` (unversioned URL plus sha256) | `MODULE.bazel` | **Agent-only drift check**: upstream silently replaces the file |
| Legacy htmx 1.9.10 | `manman/management-ui/templates` | Ignore (manman is in maintenance mode) |

Ignore paths: `.pi/**`, `.claude/worktrees/**`, `.pm-worktrees/**`,
`generated/**`, `manman/**`.

## Phase 0: hygiene, so bots can see the pins (1 PR, agent-ready)

1. **Annotate every bespoke pin** with a Renovate comment on the line above it:

   ```starlark
   # renovate: datasource=github-releases depName=helm/helm
   ```

   Use `//` instead of `#` in Go files. One generic regex manager then matches all of them. Adding a new bespoke
   pin becomes "write the comment" rather than "edit renovate.json".
2. **Centralize the CDN versions** into a single Go file, for example
   `libs/go/htmxbase/assets.go`, holding constants such as
   `HTMXVersion = "4.0.0"`. Every `templ_render.go`, `layout.go`, and its test
   then references the constant, so about 12 copies collapse to one
   annotated line.
3. **Pin the floating refs:**
   - `minio:latest` and `timescaledb:latest-pg16` (tag+digest)
   - Tilt helm chart versions
   - devcontainer feature versions
   - `setup-helm` `version`
4. **Deduplicate `GRPCURL_VERSION`** into one place, or give both copies the
   same annotation so they update together.
5. **Optionally SHA-pin GitHub Actions** (Renovate `helpers:pinGitHubActionDigests`).
   This is cheap supply-chain hardening, and Renovate keeps the SHAs current.

## Phase 1: self-hosted Renovate, native managers, observe only (1 PR plus one human action)

**Human action:** create a small GitHub App, `whale-net-renovate`, installed
only on this repo. It needs these permissions:

- read/write: contents, pull requests, issues, workflows
- read: checks, statuses

Store its ID and key as `RENOVATE_APP_ID` / `RENOVATE_APP_PRIVATE_KEY`.
PRs opened with the default `GITHUB_TOKEN` don't trigger CI; PRs opened with
an App token do.

Add two files:

- **`.github/workflows/renovate.yml`:**
  - Runs on `schedule` (Monday 05:00) plus `workflow_dispatch`, so a harness
    can kick a run with `gh workflow run renovate.yml`.
  - Uses `concurrency: renovate` so only one run happens at a time.
  - Mints the App token with `actions/create-github-app-token`.
  - Reuses `setup-build-env` and `setup-bazel-remote-cache-config`, so Bazel
    and the remote cache are on the runner.
  - Runs Renovate directly on the runner with `npx renovate`, pinned to a
    version via a `# renovate:` comment so it updates itself, with
    `RENOVATE_REPOSITORIES=whale-net/everything`. Running it on the runner,
    not in the `renovatebot/github-action` container, means post-upgrade
    commands can see `bazel`.
- **`renovate.json`:** the repo config (sketch below; finalize it in the PR).

```json5
{
  "extends": ["config:recommended", ":dependencyDashboard", ":semanticCommits"],
  "timezone": "America/New_York",
  "schedule": ["before 6am on monday"],
  "minimumReleaseAge": "3 days",
  "prConcurrentLimit": 6,
  "osvVulnerabilityAlerts": true,
  "vulnerabilityAlerts": { "schedule": ["at any time"], "minimumReleaseAge": null },
  "ignorePaths": [".pi/**", ".claude/**", ".pm-worktrees/**", "generated/**", "manman/**"],
  "postUpdateOptions": ["gomodTidy"],
  "packageRules": [
    { "matchManagers": ["gomod"], "matchUpdateTypes": ["minor", "patch"], "groupName": "go deps (non-major)" },
    { "matchManagers": ["pep621"], "matchUpdateTypes": ["minor", "patch"], "groupName": "python deps (non-major)" },
    { "matchManagers": ["bazel-module", "bazelisk"], "groupName": "bazel rules & toolchain" },
    { "matchManagers": ["github-actions"], "groupName": "github actions" },
    { "matchDepNames": ["go", "golang/go"], "groupName": "go toolchain" },
    { "matchUpdateTypes": ["major"], "dependencyDashboardApproval": true }
  ],
  "customManagers": [
    {
      "customType": "regex",
      "managerFilePatterns": ["/MODULE\\.bazel$/", "/\\.bzl$/", "/BUILD\\.bazel$/", "/\\.tilt$/", "/Tiltfile$/",
                              "/Dockerfile[^/]*$/", "/\\.github/.+\\.ya?ml$/", "/\\.bazelrc$/", "/\\.go$/"],
      "matchStrings": [
        "(?:#|//)\\s*renovate:\\s*datasource=(?<datasource>\\S+)\\s+depName=(?<depName>\\S+)(\\s+versioning=(?<versioning>\\S+))?\\s*\\n[^\\n]*?[\"'=]v?(?<currentValue>\\d[\\w.+-]*)"
      ]
    }
  ]
}
```

Notes:

- Majors wait behind dashboard approval, so they never spam PRs.
- Security fixes skip the schedule and the cooldown.
- Leave `postUpgradeTasks` out of Phase 1 so you see what raw Renovate
  produces. Expect Go PRs to fail CI until Phase 2 lands.
- Leave automerge **off** for the first two weeks. Watch which groups go green
  reliably.
- **Verify** that `changed-targets` treats a change to only `go.mod`, `go.sum`,
  or `uv.lock` as affecting all of its consumers. `MODULE.bazel` already
  forces a full run (see [RELEASE.md](RELEASE.md)). If lock-only diffs narrow
  the run, add a CI rule: `renovate/**` branches test `//...`.

## Phase 2: Bazel post-upgrade tasks and sha256 (1 PR)

Self-hosting lets Renovate run commands after each update and commit the
result into the same PR. Make these changes:

**Global config** (env in `renovate.yml`):

```
RENOVATE_ALLOWED_COMMANDS: '["^bazel ", "^bazel$", "^tools/deps/"]'
```

**`renovate.json`:** add the tasks below.

```json5
"postUpgradeTasks": {
  "commands": [
    "bazel run //:gazelle",
    "bazel mod tidy",
    "bazel run //tools/deps:rehash",
    "bazel mod deps --lockfile_mode=update"
  ],
  "fileFilters": ["**/*"],
  "executionMode": "branch"
}
```

**New tool `//tools/deps:rehash`.** A small Go tool that:

- finds the `http_archive`/`http_file` entries whose `url(s)` differ from
  `origin/main`
- downloads each one and rewrites its `sha256`/`integrity`
- fails loudly if a URL 404s (for example, an asset name changed between
  releases)
- has a `--check` mode for the agent's steamcmd drift check

**Cost guard:** `executionMode: branch` runs the commands once per branch,
not once per dependency. Grouping keeps the number of branches small, and the
remote cache keeps each run warm.

Once post-upgrade tasks are green, turn on automerge (`"automerge": true`, platform
automerge with required checks) for:

- patch/minor GitHub Actions
- Go and Python non-major groups
- digest-only OCI bumps

Leave toolchain bumps, Bazel rules, firmware, and CDN libraries
(htmx/daisyui render changes) as human- or agent-reviewed.

## Phase 3: the `/deps` agent skill (harness-driven; the part you run)

Write it harness-neutral at `docs/skills/deps/SKILL.md`, symlinked into
`.claude/skills/deps`, the same pattern as the other shared skills, so agy and
Claude Code read the same instructions. When run, it:

1. **Snapshots state.**
   - Reads the Dependency Dashboard issue
     (`gh issue list --label renovate` / `--search "Dependency Dashboard"`).
   - Reads open `renovate/**` PRs with check status
     (`gh pr list --json ...,statusCheckRollup`).
   - Reads OSV/`osv-scanner` findings.
2. **Triages each item into one bucket:**
   - green and in the automerge class → nothing to do
   - green but needs review → summarize the changelog and risk, recommend
     merge or hold
   - red → dispatch **one subagent per failing PR**. Each one checks out the
     Renovate branch in a worktree, reads the failure via the `gh-actions-logs`
     agent, fixes code for API changes, runs
     `bazel test` on the affected targets, and pushes to the same branch.
   - major awaiting approval → read the upstream migration notes, estimate the
     blast radius with `bazel query 'rdeps(//..., @dep//...)'`, then approve it
     on the dashboard or file an `agent:ready` issue labeled `high-effort`
3. **Handles the agent-only pins:**
   - **Snapshot `.deb`s:** monthly, bump the snapshot date to the latest
     snapshot, run rehash, build the consuming images.
   - **steamcmd:** run `//tools/deps:rehash --check` (download and compare the
     sha). On drift, update the sha and smoke-test the steamcmd image.
4. **Reports.** Posts one comment on the Dashboard issue with a dated summary
   (merged / fixed / held / needs-human) and runs vendor-sync locally if
   anything merged.

Invocation (the skill can start with `gh workflow run renovate.yml` to force a fresh Renovate pass):

- **On demand:** `/deps` in Claude Code, or "follow docs/skills/deps/SKILL.md"
  in agy.
- **Hands-off:** `/schedule` a weekly routine (Monday mid-morning, after
  Renovate's 6am window) that runs `/deps` and pings you only for the
  needs-human bucket.

## Phase 4: vulnerability scanning in CI (1 PR)

- Add a weekly `osv-scanner` job in `.github/workflows/deps-audit.yml`,
  covering `go.mod`, `uv.lock`, and the OCI base images. Also run it on PRs
  that touch lockfiles.
- Add `govulncheck ./...`, which is reachability-aware and so has less noise
  for Go.
- Findings appear as GitHub code-scanning alerts (SARIF). The `/deps` skill
  reads them in step 1.

## Rollout order and effort

| Phase | Effort | Blocking human action |
|-------|--------|-----------------------|
| 0 Hygiene | low, `agent:ready` | none |
| 1 Self-hosted Renovate (observe) | low | create the `whale-net-renovate` GitHub App and its secrets |
| 2 Post-upgrade tasks + rehash + automerge | medium | none |
| 3 `/deps` skill (+ schedule) | medium | none |
| 4 OSV/govulncheck | low | none |

Expect a burst of PRs in the first week. The Dashboard rate-limits them
(`prConcurrentLimit`), and the first `/deps` run is mostly catch-up.

## Open decisions

- **Cost.** Everything here is free: Renovate is open source (AGPL), Actions
  minutes are free on public repos, and osv-scanner/govulncheck are free.
  Mend's hosted app is not used, because it can't run Bazel post-upgrade
  commands.
- **Automerge scope** after the observation window.
- **Whether to SHA-pin Actions** (Phase 0, step 5).
