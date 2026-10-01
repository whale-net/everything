package store

// entityMilestoneActive is the view (migration 029) exposing only
// entity_milestone rows with withdrawn_at IS NULL. Every reader of delivery
// edges selects FROM this, never the raw table; writers and withdrawal itself
// use entity_milestone directly.
const entityMilestoneActive = "entity_milestone_active"
