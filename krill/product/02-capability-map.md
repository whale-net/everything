# Capability map

> **A vocabulary collision, on purpose.** This map numbers capabilities `C1..Cn` because that is today's `PRODUCT.md` vocabulary and this brief is a today-conventions artifact. Krill's own entity model **retires `Capability`** — `Feature` takes its slot in the `Product → FeatureSet → Feature → {FR, NFR}` chain (#2423, settled). So a reader will find krill's brief written in a vocabulary krill plans to delete. The alternative — inventing a second numbering scheme before the thing that replaces it exists — was worse.

> **Numbering is by allocation, not by bucket.** C25 and C26 were added in round 2, C27 and C28 in round 3; all four were appended rather than renumbered, so every citation in the load-bearing decisions still resolves. They are bucketed by the milestone that delivers them, not by their number.

## Now

- **C1** — A Requirement Contributor can record a product's spec as structured entities — product, feature set, feature, functional and non-functional requirement — instead of as prose in a file.
- **C2** — A Requirement Contributor can attach a load-bearing decision to the feature set it constrains, so whoever touches that area is shown it and nobody loads the global list.
- **C3** — An Agent can ask krill for exactly the spec slice its work touches — one feature set, one feature, the requirements beneath it — without reading the whole product.
- **C4** — An Agent can do that from whatever harness it is running under — Claude Code today, omnigent or whagent-net next — without a per-harness integration being written for it first.
- **C5** — A Requirement Contributor can see what any requirement or decision said at an earlier point in time, and what superseded it.
- **C6** — A Requirement Contributor can amend a shipped spec by superseding it rather than overwriting it, so shipped history is never rewritten.
- **C7** — Anyone can read a product's current spec as generated, read-only repo docs — the product doc set only — that are split and indexed automatically, with krill remaining the only writable source.
- **C8** — A Swarm Operator can import an existing markdown-specified product into krill and confirm nothing was lost in the move.
- **C9** — A human can cross-link a PR, commit, or conversation to a krill product through a thin GitHub pointer artifact, as they do today.

## Next

- **C10** — A Requirement Contributor and an Agent can hold a design session in krill, with each draft revision kept as a record instead of a comment thread plus a gist.
- **C11** — Anyone can query what is still open in a design session — blocking versus non-blocking questions, answered or not — rather than re-reading the thread to find out.
- **C12** — A Requirement Contributor can submit an idea or user story in plain language and have a producer-role Agent shape it into conforming entities on their behalf, so intake is forgiving while the spec layer stays rigid.
- **C13** — A Requirement Contributor can cut a product's spec into shippable milestones and agent-shippable milepebbles, so *what* and *when* are tracked on separate axes.
- **C14** — An Agent can claim an available task and receive a payload self-contained enough that it never has to supplement it by reading the spec surface.
- **C15** — An Agent can report what happened on a run without knowing where the task goes next.
- **C16** — An ephemeral Agent can be picked up on any host and resume a task from the references krill holds, without re-discovering context the last run already had.
- **C17** — A Swarm Operator can answer "what is claimed, what is stuck, what dead-lettered, and why" from a console.
- **C18** — A Swarm Operator can intervene on a stuck or thrashing task — requeue, cancel, release a lease, escalate to a human — without hand-editing storage.
- **C25** — Any persona can record scope they noticed but are not acting on as a tracked note with its own lifecycle, instead of leaving it as a stray comment somebody has to find.
- **C26** — A Swarm Operator can leave a swarm running unattended.
- **C27** — A Swarm Operator can bring in a product krill did not author and have krill render it back faithfully, so the model is shown to generalize past its own author.
- **C28** — A Requirement Contributor can see, from krill alone, what is planned versus what is merely spec'd, including work that is only partially complete.

> **C13 is authoring, and only authoring — a round-3 narrowing.** C13 is the act of *cutting* a product into milestones and milepebbles; C28 is seeing delivery status across that cut. Both are M3's. Neither is what M1 does: **M1 can hold and render milestones that arrived inside an imported document, because holding is not planning.** See M1's LB6 note.

## Later

- **C19** — Any persona can use krill through a web UI, with that UI's own work planned and tracked in krill as the dogfood case.
- **C20** — An Agent can have krill own the branch and PR lifecycle for a task, not merely hold references to it.
- **C21** — A Swarm Operator can be told when a product's spec in krill has drifted from the code that implements it, without anyone noticing by hand.
- **C22** — A Swarm Operator can run products from a second repository or tenant on the same krill instance.
- **C23** — A Requirement Contributor can record a decision that spans several products, so cross-cutting architecture decisions have a home above a single domain.
- **C24** — A Swarm Operator can attribute an action to a specific agent identity rather than only to the run and the human who minted it.

> **Not in this map, and not a `Later` capability either: rendering anything but the product doc set.** Round 3, Q5 settles the boundary rather than deferring it — *"architecture always lives with the code, as it is the essence of how the code is implemented. Product is the first pass."* `PRODUCT.md` holds **what and why**, which krill can own; `ARCHITECTURE.md` holds **how the code is implemented**, which belongs beside the code. `README.md`, `ENV.md`, and `TOC.md` likewise stay hand-written. See Non-goals.
