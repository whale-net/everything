---
name: doc-splitting
description: Split a doc that's outgrown a single-pass read (roughly 800-1000 lines / ~20K tokens) — PLAN.md-style status docs, heavily cross-referenced reference docs like ARCHITECTURE.md, code modules, or persona docs. Use when AGENTS.md's "Size Limits & Splitting" threshold is tripped, or when reviewing whether a doc that's grown during a change now needs splitting.
---

# Doc Splitting

This is the canonical, harness-neutral source for this repo's doc-splitting
mechanics. It is symlinked into `.claude/skills/doc-splitting` for Claude
Code; `AGENTS.md` § Size Limits & Splitting keeps only the short,
always-loaded threshold and the numbered rules other docs cite by number —
read this skill for the full file-type guidance and worked examples.

## How to split, by file type

- **Planning / status docs (`PLAN.md` and similar):** split current-state
  from history. Keep only what's true right now — status, open items,
  forward-looking scope — in the live file; move the as-built record of
  completed phases to a `*-HISTORY.md` sibling, linked from the live file's
  status table and not meant to be read start-to-finish. See
  `tools/app_registry/PRODUCT.md`/`product/01-current-state.md` (current)
  and `tools/app_registry/PLAN-HISTORY.md` (as-built phase archive) for a
  worked example — that domain outgrew a single live `PLAN.md` entirely once
  a `PRODUCT.md` existed, so the "live file" role there is now split further,
  across `PRODUCT.md` (roadmap), `OPERATIONS.md` (known issues, ongoing
  checklists), and `ARCHITECTURE.md` (design caveats), with
  `PLAN-HISTORY.md` remaining the one historical sibling all of them link
  into.
- **Reference docs with heavy internal cross-referencing (`ARCHITECTURE.md`
  and similar):** two variants, same underlying rule — the file must stop
  being where content *accumulates*.
  - **Under threshold, but growing:** an index-at-top (single file, `##`
    sections, a jump table by heading name) is enough — it costs nothing to
    maintain and there's no cross-reference risk yet, since nothing has
    moved.
  - **Over threshold:** move to a directory — one file per `##` section
    under `<DOC>/<NN>-<slug>.md` (recursing into a subdirectory for any one
    section that's still oversized on its own, e.g.
    `<DOC>/<NN>-<slug>/<MM>-<slug>.md`), with `<DOC>.md` itself rewritten
    down to a real index (a jump table linking every file, plus whatever
    handful of principles are short and referenced by number/name from
    everywhere — keep those inline rather than forcing a one-line file).
    Sections keep their exact heading text as each file's `# ` title, so a
    citation like `ARCHITECTURE.md "Reconcile watermark"` in a code comment
    stays discoverable by grepping the directory even before anyone updates
    the comment to the new path — grep-discoverability from unchanged prose
    is what makes rule 5 below tractable at volume (a comment citing a
    section by name is not the same maintenance burden as a `[text](#anchor)`
    link, which is a hard break and must be fixed). Fix anchor-style links
    (rule 5) always; bare prose citations in code/CI comments are lower
    priority at high volume — call out in the change what was left and why.
    See `tools/app_registry/architecture/` (directory) and
    `tools/app_registry/ARCHITECTURE.md` (the index + design principles that
    stayed inline) for a worked example, including the recursive case
    (`architecture/08-release-lifecycle/`).
- **Code modules:** split along the boundary the language already uses for
  imports — one package/module per concern. Watch for circular or stale
  imports introduced by the split (notably in Python, where circular imports
  fail late and confusingly).
- **Persona / role docs:** one file per persona or actor, cross-linked from
  an index, rather than one file describing every persona serially.

## Split mechanics

Priority order when these pull against each other: an algorithmic,
agent-navigable split always wins; only minimize consumer impact within
whatever that split allows — a split that's easy on a human but forces an
agent to grep to find the right file is the wrong split.

1. **Pick an algorithmic boundary, not a line-count cut.** The boundary must
   be something an agent can name and predict *before* opening the doc —
   chronological phase, one-file-per-concern, current-vs-historical,
   one-per-persona. "First half" / "last N sections" / "wherever it got to
   1000 lines" are not valid boundaries: they carry no meaning an agent can
   reason about on the next pass, so the doc will just need re-splitting on
   its own arbitrary terms next time.
2. **Name split files predictably.** Use a fixed suffix/prefix an agent can
   guess without reading an index: `<DOC>-HISTORY.md` for a current/history
   split, `<DOC>/<NN>-<slug>.md` for a directory split ordered by the same
   boundary as rule 1. Never split into ambiguously-named files (`part2.md`,
   `notes.md`, `misc.md`) — the name must say *which* boundary-value lives
   there.
3. **Keep one canonical entry point.** The original filename stays the file
   for "what's true now" / "where a cold read starts" — it must never become
   a content-free redirect stub. If a doc is fully superseded, delete it and
   repoint every inbound reference in the same change; don't leave a
   tombstone for an agent to open and bounce off of.
4. **Make every split file discoverable.** Add or update the entry in the
   domain's `TOC.md` with a one-line description of what's in the file and
   when to read it — a split that isn't indexed just relocates the grep-cold
   problem instead of solving it.
5. **Verify before merging, don't assume.** Grep the repo for the pre-split
   filename and any heading anchors that moved; fix every hit — other docs,
   `TOC.md`, code comments citing a heading or file, CI config — in the same
   change. A split that leaves dangling references is worse than not
   splitting.
6. **Re-check the split's own size.** Each resulting file should
   independently clear the size trigger. Don't stop at two files if a
   natural third boundary already exists — that's deferring the same
   problem, not solving it.
7. **Only then, minimize consumer friction.** Within a boundary that
   satisfies 1–6, prefer the split that costs a normal reader the least —
   e.g. a reader who only wants current state shouldn't need to open the
   history file, and vice versa. This is a tiebreaker, not a reason to
   weaken the boundary itself.

## Reference

- `AGENTS.md` § Size Limits & Splitting — the short, always-loaded version
  of this convention
- `tools/app_registry/PLAN-HISTORY.md` / `product/01-current-state.md` —
  current/history split worked example
- `tools/app_registry/architecture/` — directory-split worked example,
  including the recursive case (`architecture/08-release-lifecycle/`)
