# Migration numbering (M3)

Assigned on root plan issue #2681 -- the first M3 migration:

| Version | Contents | Task |
|---------|----------|------|
| `010` | Milestone authoring: `milestone_ref` authoring columns, `milestone_deferral`, `entity_milestone.relation` (FR1, FR2) | #2683 |
| `011` | Milepebbles: `milestone_ref.kind` widened to include `'milepebble'`, `parent_milestone_id` (FR3, FR4) | #2684 |
| `012` | Milestone/milepebble status: append-only `milestone_status_event` (FR8, FR9, FR12) | #2685 |
| `013` | Per-item shipment: append-only `delivery_shipment` (FR10) | #2686 |
| `014` | Backlog bucket: `milestone_ref.kind` widened to include `'backlog'`, `milestone_ref_backlog_product_idx` | #2687 |

