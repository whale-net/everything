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
| 1. Detect + PR | **Renovate** (Mend-hosted GitHub App; free for this public repo) | Native managers, plus custom regex managers for the bespoke pins |
| 2. Fix up | **`deps-fixup` GitHub workflow** on `renovate/**` branches | The Bazel follow-up steps that Renovate can't run on hosted infrastructure (tidy, gazelle, lockfile, sha256) |
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

## Phase 1: Renovate with native managers, observe only (1 PR plus one human action)

**Human action:** install the Renovate GitHub App on `whale-net/everything`.

Add `renovate.json` (sketch below; finalize it in the PR):

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
- Leave automerge **off** for the first two weeks. Watch which groups go green
  reliably.
- **Verify** that `changed-targets` treats a change to only `go.mod`, `go.sum`,
  or `uv.lock` as affecting all of its consumers. `MODULE.bazel` already
  forces a full run (see [RELEASE.md](RELEASE.md)). If lock-only diffs narrow
  the run, add a CI rule: `renovate/**` branches test `//...`.

## Phase 2: fixup workflow for Bazel follow-ups and sha256 (1 PR plus one human action)

Hosted Renovate won't run arbitrary `postUpgradeTasks`. Add
`.github/workflows/deps-fixup.yml`, triggered on `pull_request` for
`renovate/**` heads. It:

1. Reuses `setup-build-env`.
2. Runs `bazel run //:gazelle`, then `bazel mod tidy`, then
   `bazel mod deps --lockfile_mode=update`.
3. Runs `bazel run //tools/deps:rehash`. This new small Go tool:
   - finds `http_archive`/`http_file` entries whose `url(s)` changed in the PR
     diff
   - downloads each one
   - rewrites its `sha256`/`integrity`
   - fails loudly if a URL 404s (for example, an asset name changed between
     releases)
4. Commits any diff back to the branch as `chore(deps): bazel fixup`.

**Human action:** commits pushed with `GITHUB_TOKEN` don't retrigger CI, so
create a small GitHub App, or a fine-grained PAT scoped to `contents:write`
on this repo. Store it as `DEPS_FIXUP_TOKEN`.

Once the fixup works, turn on automerge (`"automerge": true`, platform
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

Invocation:

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
| 1 Renovate (observe) | low | install the Renovate app |
| 2 Fixup + rehash + automerge | medium | create `DEPS_FIXUP_TOKEN` |
| 3 `/deps` skill (+ schedule) | medium | none |
| 4 OSV/govulncheck | low | none |

Expect a burst of PRs in the first week. The Dashboard rate-limits them
(`prConcurrentLimit`), and the first `/deps` run is mostly catch-up.

## Open decisions

- **Hosted vs. self-hosted Renovate.** Hosted is recommended; Phase 2
  compensates for its lack of `postUpgradeTasks`. Self-hosting via
  `renovatebot/github-action` would allow in-line `bazel mod tidy`, but it
  needs a Bazel-capable runner and costs CI minutes on every Renovate run.
- **Automerge scope** after the observation window.
- **Whether to SHA-pin Actions** (Phase 0, step 5).
