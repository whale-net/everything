# The GitHub pointer artifact (FR20, C9, issue #2496)

`krill/forge` and `krill/store/pointer.go` implement FR20: `POST
/pointer-artifacts` (`krill/api/handlers/pointer.go`) mints krill's one
thin GitHub issue for a Product, so this repo's own "Part of #\<n\>"
cross-linking convention keeps working once a Product's spec lives in
krill's entity model instead of a markdown file.

**One artifact per Product, not one per PR/commit/conversation.** Despite
`pointer_artifact`'s columns reading like a per-cross-link record at first
glance, this task settles the opposite design: krill creates exactly one
GitHub issue per Product (`pointer_artifact_product_idx`, migration 005,
is a UNIQUE index on `product_id`) and then gets out of the way — the
issue's own number is what a PR body, a commit message, or a conversation
references afterward, through GitHub's ordinary mechanics, with **no
further krill involvement and no per-reference row**. This matches C20
("krill does not own branch or PR lifecycle... stores references only"):
there is no branch name, PR number, commit SHA, or conversation URL column
anywhere in migration 005 — see that migration's own comment for the full
reasoning. `pointer_artifact.kind` discriminates the artifact's own shape
(today, always `"github_issue"`), the same one-column-not-two-tables
precedent as `requirement.kind`/`non_goal.kind`, not what has since
referenced the issue.

**`scope.pointer_issue_number` is the system of record; `pointer_artifact`
is the audit trail.** LB1 already put the forge coordinates
(`repo_full_name`, `default_branch`, `pointer_issue_number`) on `scope`,
not on any entity row (migrations/001_scope.up.sql). `PointerArtifactStore
.Create` (`krill/store/pointer.go`) writes both in one transaction: the
`pointer_artifact` row (who created it — both LB4 subjects, always
recorded, unlike every other M1 create endpoint — and when) and
`scope.pointer_issue_number` (what the current coordinate actually is).
The two can never observably diverge for the reason above: at most one
pointer issue is ever minted per Product in M1's one-Product-per-scope
shape.

**Order of operations avoids minting a spurious issue on a caller
error.** `CreatePointerArtifactHandler` reads back the target Product and
rejects an unknown or cross-scope `product_id` with 400 *before* ever
calling `krill/forge.Client.CreateIssue` — unlike every entity create
handler, this one's store call is not the first fallible step, because its
side effect (a real, human-visible GitHub issue) is not one a rejected
request should still cause.

**`krill/forge` is intentionally the smallest possible client.**
`forge.Client` has exactly one method, `CreateIssue`; `GitHubClient`
authenticates with a plain bearer token (`KRILL_GITHUB_TOKEN`, see
`ENV.md`), not a full GitHub App installation-token flow like
`tools/app_registry/worker/release`'s `GitHubDispatcher` — that
machinery exists to dispatch and poll CI workflow runs repeatedly; FR20
needs exactly one write, ever, per Product.

**Retrievable from the whole-product slice (FR8).** `krill/slice`'s
`GetProductSlice` is the only one of the four granularities that populates
`Document.PointerArtifacts` (`krill/slice/query.go`) — a pointer
artifact's single parent is the Product itself, never a FeatureSet or
Feature, so it is unreachable from `GetFeatureSetSlice`/`GetFeatureSlice`/
`GetRequirementSlice` the way a FeatureSet-scoped LoadBearingDecision is.
`PointerArtifactEntity` (`krill/slice/document.go`) embeds only `ID`, not
the `EntityRef`/`RevisionID` pair every other slice entity carries —
`pointer_artifact` is not SCD2 (LB3), so there is no revision to expose.

