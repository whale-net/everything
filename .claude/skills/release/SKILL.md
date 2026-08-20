---
name: release
description: Guide for triggering an app/helm chart release in the Everything monorepo
---

# Release Builder

**This skill's mechanism changed with the App Registry v2 release cutover
(issue #891, plan #886 FR15).** `gh workflow run release.yml ...` and the
GitHub Actions "Run workflow" UI form are no longer supported human entry
points -- `release.yml`'s `authorize-trigger` job now rejects any dispatch
that isn't authenticated as the App Registry's own GitHub App (the identity
Temporal's `ReleaseWorkflow` dispatches under). There is no CLI/`gh`
equivalent for triggering a release anymore.

## What to do instead

Releases are triggered from the **App Registry UI**'s Trigger Release page
(`/releases/trigger`), by a user holding the same permission already
required to promote. This agent cannot submit that authenticated form on
the user's behalf, so this skill's role is now to prepare the request, not
execute it:

1. Determine what the user wants to release (apps, helm charts, or both)
   and the batch scope -- same accepted values as before: a domain name
   (e.g. `manmanv2`), a comma-separated list, or `all` (excludes `demo`
   unless explicitly included).
2. Determine the version strategy: auto-increment patch/minor, or an exact
   version.
3. Summarize the release plan for the user (what will be released, version
   strategy, dry-run or not) exactly as this skill used to before
   triggering.
4. Direct the user to the App Registry UI's `/releases/trigger` page to
   submit it themselves -- do not attempt `gh workflow run release.yml` or
   any other automated trigger; it will be rejected.
5. If the user wants to check on an in-flight or past release, point them
   at `/releases/<id>` (the App Registry UI's release status page), not
   the GitHub Actions run list.

## Quick Reference: Available Domains

- **manmanv2** - ManManV2 apps and control services chart
- **manman** - ManMan v1 host services
- **friendly_computing_machine** - FCM bot services
- **demo** - Demo/example apps and charts

## Parameter formats (for summarizing to the user)

### Apps
Accepts one of:
- **Domain/namespace name**: `"manmanv2"`, `"demo"`, `"friendly_computing_machine"`
- **Comma-separated app names**: `"hello-python,hello-go"`
- **"all"**: Release all apps (excludes demo unless include_demo is set)

### Helm Charts
**CRITICAL**: Chart names should be WITHOUT the "helm-" prefix!
- ✅ CORRECT: `"manmanv2-control-services"`
- ❌ WRONG: `"helm-manmanv2-control-services"`

Accepts one of:
- **Chart name without helm- prefix**: `"manmanv2-control-services"`, `"demo-hello-fastapi"`
- **Domain name**: `"demo"`, `"manmanv2"`
- **Comma-separated chart names**: `"manmanv2-control-services,demo-hello-fastapi"`
- **"all"**: Release all charts (excludes demo unless include_demo is set)
