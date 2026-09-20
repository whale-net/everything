# M3's delivery axis, end to end (issue #2690)

M3 (#2683-#2689) adds a second axis alongside the spec axis migration 002
settled: not just *what* the product is (Features, FRs, LoadBearingDecisions),
but *when and whether* it ships. Five migrations (010-014 above), each
landed independently by its own issue, compose into one coherent shape.
This section is the map; "The milestone authoring schema", "The backlog
bucket vs. the `Later` capability bucket", and "The abandon verb" above
each go deep on their own piece -- read this section first, then follow
the cross-references below for the piece you need.

**One `milestone_ref` table, three `kind`s.** A milestone (`kind =
'milestone'`), a milepebble cut from one (`kind = 'milepebble'`,
migration 011), and the one-per-product backlog bucket scope re-cuts or
abandon sweeps land in (`kind = 'backlog'`, migration 014) are all rows
of the same table, discriminated by this one column -- never three
tables. This is a deliberate, repeated choice, not an accident of
incremental migrations: every consumer that already knew how to read a
`milestone_ref` row (the renderer's `ListRefsByProduct` filter, the
status register, the shipment register, the re-cut/abandon primitives)
keeps working against a widened CHECK constraint rather than needing a
second read path for a milepebble or the backlog. `parent_milestone_id`
is the one structural difference a milepebble adds (NULL for a milestone
or the backlog bucket, always set for a milepebble) -- see migration
011's own comment for the CHECK-enforced pairing, and migration 014's for
why the backlog bucket relaxes that pairing to a three-way rather than a
`<=>` its own row alone would satisfy.

**Delivers and Must not foreclose are still one association table with
a discriminator (LB6), never a column on a spec entity.** Migration 010
adds `entity_milestone.relation` (`'delivers'` | `'must_not_foreclose'`)
to the one table migration 004 already established for this purpose --
this is the same LB6 call M1 made, extended rather than revisited: a
Feature or LoadBearingDecision never grows a `milestone_id` column of its
own, no matter how many new delivery-axis concepts land on top. See "The
milestone authoring schema" above for the unique-index consequence this
had for the importer's own pre-existing `AddAssociation` path.

**Per-table LB3 boundary calls, migrations 010-014.** Every migration in
this range states its own LB3 line explicitly (SCD2 vs. plain mutable vs.
append-only) rather than assuming the table above or below it in the
migration sequence sets a precedent to inherit silently:

- `milestone_ref`'s own authoring columns (010, 011, 014) and
  `milestone_deferral` (010) are **plain mutable**, not SCD2 -- an
  outcome sentence, an FR budget, a milepebble's parent, or a deferral's
  existence are facts set once or revised in place, not a value NFR2
  requires history for. No `valid_from`/`valid_to` pair on any of them.
- `milestone_status_event` (012) and `delivery_shipment` (013) are
  **append-only**, and deliberately neither SCD2 nor plain mutable --
  NFR2's "recorded as an addition to history, never an overwrite" binds
  a status transition and a shipment record specifically, not every
  delivery-axis fact. No `valid_from`/`valid_to` pair either (that shape
  is for a *current value*, and neither table has one to distinguish from
  its history), no UPDATE path, no DELETE path, ever, in this package.
  A milestone's own `status` is derived from this history's latest row,
  never stored as a column on `milestone_ref` itself (`CurrentStatus`'s
  own doc comment).

So the spec axis around a milestone (its Delivers/Must-not-foreclose
Features and Decisions) stays SCD2, the milestone's own authoring shell
is plain mutable, and its status and shipment history are append-only --
three different LB3 answers on three parts of what looks, from the
outside, like one coherent "milestone" concept. This is intentional: LB3
is a per-table call, not a per-feature one.

**The backlog bucket vs. the product's `Later` capability bucket** --
see "The backlog bucket vs. the `Later` capability bucket" above for the
full distinction; in one line, the backlog is delivery-axis (scope that
WAS committed and got un-committed), `Later` is spec-axis (never
committed at all), and nothing ever moves between them.

**The abandon cascade** -- see "The abandon verb" above for the full
design; in one line, abandoning a milestone abandons every live
milepebble it has in the same transaction, each with its own status
transition and its own not-yet-shipped sweep, and there is no un-abandon
verb anywhere in this package.

**M3's migration numbering** -- see "Migration numbering (M3)" above:
010-014, assigned up front on root plan issue #2681 the same way M1's own
table at the top of this section was assigned on issue #2487, so five
tasks landing out of review order never collide on a version number.

