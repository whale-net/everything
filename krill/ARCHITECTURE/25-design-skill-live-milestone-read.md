# The design skill's live milestone read (FR21, root plan issue #2485)

FR21 is M1's one concrete self-hosting *consumer*: `/project-manager:design
--milestone`'s milestone-read step (`tools/project-manager/skills/design/
SKILL.md` step 2, and `tools/project-manager/agents/producer.md`'s
"Milestone-scoped intake") reads `<domain>/product/03-roadmap.md` for every
domain except krill's own — for krill, that step instead calls krill's own
`get_product_slice` MCP tool (FR8, whole-product granularity — the same
tool `krill/mcp/tools/slice.go` registers for FR5-FR9, issue #2494) over
the MCP spec surface, ungated by `init` (a read, same as every FR5-FR9/FR11
call — see "`init` and the write gate" above). See
`tools/project-manager/CONVENTIONS.md` "krill's own milestone read is live,
every other domain's is a file" for why this is a narrow, deliberate
exception rather than the start of migrating every domain's read off the
file.

**Resolving krill's own Product id.** FR5-FR9's tools take a surrogate id,
not a name — there is no "find a Product by name" tool in M1. The design
skill resolves krill's own live Product id through its own FR20/C9 pointer
artifact (see "The GitHub pointer artifact" above): the one GitHub issue
titled `Product: krill` whose body carries `krill id \`<uuid>\``. This is
exactly what FR20 exists for — keeping GitHub-side cross-referencing
working once a Product's spec lives in krill's entity model rather than a
file — used here for the read direction instead of the cross-linking
direction FR20's own doc comment (`krill/forge/github.go`) describes. Until
krill's own brief (FR16-FR19, issue #2497) has actually been imported into
a reachable krill instance and a pointer artifact minted for it, this
lookup has nothing to resolve; standing up and importing into that instance
is an operational step, not something this task's code does (`AGENTS.md`:
"Do not patch production environments").

**Updated gap, as of M3 (#2683-#2689): a milestone's Delivers association
set is now queryable, but not through this read path.** `get_product_
slice` (FR8, the call this section describes) still returns every current
`FeatureSet`/`Feature`/`Requirement`/`LoadBearingDecision` under krill's
Product with no per-milestone filter — that has not changed. What has
changed is that a milestone's own `Delivers`/`Must not foreclose`
association set is no longer reachable only through `krill/render`'s
direct `Source` interface: `MilestoneAuthoringStore.GetMilestone`
(#2683) exposes it over both HTTP (`GET /milestones/{id}`) and MCP
(`get_milestone`, `krill/mcp/tools/milestone.go`), and
`slice.Querier.ListProductDelivery` (#2689, FR11) exposes the same
association sets, plus status, for every milestone and milepebble under a
Product at once (`list_product_delivery` MCP tool, `GET
/products/{id}/delivery`). Both are real, callable surfaces today — this
is the concrete capability M3's C13/C28 promised.
**What remains unwired is the design skill's own call site**: `tools/
project-manager/skills/design/SKILL.md` step 2 and `producer.md`'s
milestone-scoped intake still call only `get_product_slice`, never
`get_milestone` or `list_product_delivery` — wiring the design skill's
krill-domain branch onto either of M3's new reads is explicitly out of
scope for the plan that shipped them (root plan issue #2681 § Out of
scope) and is a separate, later task. Until that wiring lands, the design
skill's live call still surfaces this milestone's spec *context* only (a
superset — every capability/decision in the product, not a pre-filtered
`Delivers:`/`Must not foreclose:` list), and still consults `krill/
product/03-roadmap.md`'s headings (structure only, not content) to
confirm which `M<n>` exists. Architect's own Load-bearing check
(architect.md § Process) still reads the committed file directly for the
authoritative `Must not foreclose` list on every milestone draft, krill
included, so this gap does not leave that check unguarded.

**Failure mode.** An unreachable krill MCP server, or no pointer artifact
to resolve a Product id from, is a **loud, named stop** — "krill's spec MCP
surface is unreachable; cannot read krill's own milestone roadmap live" —
never a silent fallback to reading `krill/product/03-roadmap.md`. A quiet
fallback would leave M1's self-hosting loop unexercised, which is the
failure FR21 exists to prevent (root plan issue #2485).

**Tested at the `slice.Querier` level (LB7).** `krill/conformance/
design_milestone_query_integration_test.go` proves the *data* half of this
read path against a real Postgres holding krill's own imported brief
(#2497's fixture): `GetProductSlice` for krill's own Product surfaces the
same capability descriptions a given milestone's committed `Delivers:`
line names, and a nonexistent/unreachable Product id surfaces a clear,
non-nil, named error rather than an empty or silently-wrong result. Per
LB7 ("M1's MCP tool is a thin wrapper over it, not the thing itself" —
`krill/mcp/tools/slice.go`), testing `slice.Querier` directly exercises the
same code the MCP tool wraps; the MCP wire protocol itself (auth, byte-
identical JSON shape) is already covered by issue #2494's own tests. The
domain-branch decision in `SKILL.md`/`producer.md` itself (krill →
live call, every other domain → file) is verified by diff review, per root
plan issue #2485's own acceptance criteria, not by an automated test —
`tools/project-manager` ships no Bazel targets to run one against.

