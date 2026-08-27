// Package attribution implements FR23's nearest-ancestor plant attribution
// rule: a reading attributes to the nearest ancestor region (including its
// own) holding an active plant at reading time, and to all active plants in
// that one region -- never to plants further up the tree (A11), and never
// double-counted across a region's sibling plants (A2, FR20.1).
//
// "At reading time" binds to plant_region_history (migration 017, FR19):
// attribution resolves against the placement intervals valid at the
// reading's recorded_at, never against plant.region_id's current-value
// cache.
//
// Scaffold only (this task's Scaffold phase): ResolvePlants returns
// ErrNotImplemented until this task's Implementation phase fills in the
// ancestor walk, reusing v_region_path (migration 012) rather than writing
// a second recursive CTE. A SQL form of the identical rule is scaffolded
// alongside this package as a database function
// (leaflab/migrate/migrations/018_attribution.up.sql,
// attribute_region_plants) -- NFR1.c requires the two implementations to
// agree by construction, which is why they are scaffolded together here
// rather than one now and one later.
package attribution

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotImplemented is returned by ResolvePlants until this task's
// Implementation phase fills in the nearest-ancestor walk (FR23).
var ErrNotImplemented = errors.New("attribution: not implemented (Implementation phase, FR23)")

// PlantRef is one plant attributed to a region at a point in time. Every
// plant-scoped response carries the full attributed set as each other
// plant's sibling disclosure (FR23) -- PlantRef is deliberately the
// smallest shape that supports that: a caller needing more than
// identity/name joins back to `plant` by PlantID rather than this package
// widening PlantRef to carry it.
type PlantRef struct {
	PlantID int64
	Name    string
}

// Resolver resolves plant attribution against plant_region_history and
// region.parent_region_id (via v_region_path). It is the single Go
// implementation of FR23's rule -- see this package's doc comment for the
// SQL twin ResolvePlants must agree with (NFR1.c).
type Resolver struct {
	db *pgxpool.Pool
}

// NewResolver constructs a Resolver over db.
func NewResolver(db *pgxpool.Pool) *Resolver {
	return &Resolver{db: db}
}

// ResolvePlants attributes regionID's reading at at (a reading's
// recorded_at) to the nearest ancestor region -- including regionID itself
// -- holding at least one plant whose plant_region_history interval
// contains at (FR23). It returns every active plant in that one
// attributing region (never plants further up the tree, per A11) alongside
// the attributing region's ID, so a caller can both list siblings (FR23's
// sibling disclosure) and key an aggregate by region rather than by the
// plant fan-out (FR20.1's no-double-counting requirement).
//
// Implementation walks v_region_path (migration 012) from regionID toward
// the root rather than issuing a second recursive CTE -- see this
// package's doc comment.
func (r *Resolver) ResolvePlants(ctx context.Context, regionID int64, at time.Time) ([]PlantRef, int64, error) {
	return nil, 0, ErrNotImplemented
}
