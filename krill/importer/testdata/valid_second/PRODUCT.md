# SecondProduct — Product brief

A second, minimal fixture (issue #2548's Testing item 6) distinct from
`testdata/valid`'s `TestProduct`: it exists only to prove that importing a
*different* doc set into the same scope still works after `testdata/valid`
has already been imported there -- the FR12 refusal check
(`refuseIfAlreadyImported`) is keyed on `(scope, source_path)` before
`write()` ever resolves a Product, and `import_completion` itself is keyed
on `(scope_id, product_id)`, not `scope_id` alone, so a second distinct
product must never be blocked by the first product's own completion row.

## Vision

SecondProduct exists only to exercise the importer's FR12 "different doc
set, same scope" case.

## Personas

- **Operator** — runs the fixture through the importer.

## Load-bearing decisions

LB1 — Second fixture's only decision
  Body text for the second fixture's decision.

## Non-goals

- **Being a real product.** This file is a test fixture, not a product brief for anything that ships.
