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
// This is the Go twin of leaflab/migrate/migrations/019_attribution.up.sql's
// attribute_region_plants database function -- NFR1.c requires the two to
// agree by construction. Both walk the same path (v_region_path, migration
// 012, leaf toward root) and apply the identical stopping rule: the first
// region -- including the reading's own -- with at least one
// plant_region_history row whose interval contains the reading's recorded
// instant. Keep any change to the rule mirrored in both places.
package attribution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrRegionNotFound is returned by ResolvePlants when regionID does not
// exist in v_region_path (and therefore not in region either) -- there is
// no path to walk.
var ErrRegionNotFound = errors.New("attribution: region not found")

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
// package's doc comment. This is the Go twin of
// attribute_region_plants (migration 019); keep the two in agreement.
func (r *Resolver) ResolvePlants(ctx context.Context, regionID int64, at time.Time) ([]PlantRef, int64, error) {
	var pathIDs []int64
	err := r.db.QueryRow(ctx, `
		SELECT path_ids FROM v_region_path WHERE region_id = $1
	`, regionID).Scan(&pathIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, fmt.Errorf("%w: region %d", ErrRegionNotFound, regionID)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("attribution: load region path for region %d: %w", regionID, err)
	}

	// pathIDs is root-to-leaf (v_region_path's convention, migration 012);
	// walk it leaf-to-root -- i.e. from regionID itself outward to the
	// root -- stopping at the first region with at least one plant whose
	// plant_region_history interval contains at (A11: nearest ancestor
	// only, never every ancestor level).
	var attributedRegionID int64
	found := false
	for i := len(pathIDs) - 1; i >= 0; i-- {
		candidate := pathIDs[i]
		var hasPlant bool
		if err := r.db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM plant_region_history
				WHERE region_id = $1
				  AND valid_from <= $2
				  AND (valid_to IS NULL OR valid_to > $2)
			)
		`, candidate, at).Scan(&hasPlant); err != nil {
			return nil, 0, fmt.Errorf("attribution: check region %d for an attributing plant: %w", candidate, err)
		}
		if hasPlant {
			attributedRegionID = candidate
			found = true
			break
		}
	}
	if !found {
		// No region on the path -- including regionID itself -- has an
		// active plant at at. Not an error: a reading in an unplanted
		// area attributes to nothing (FR23 is silent on this case beyond
		// "the first region ... with at least one plant"; there is none).
		return nil, 0, nil
	}

	rows, err := r.db.Query(ctx, `
		SELECT p.plant_id, p.name
		FROM plant_region_history prh
		JOIN plant p ON p.plant_id = prh.plant_id
		WHERE prh.region_id = $1
		  AND prh.valid_from <= $2
		  AND (prh.valid_to IS NULL OR prh.valid_to > $2)
		ORDER BY p.plant_id
	`, attributedRegionID, at)
	if err != nil {
		return nil, 0, fmt.Errorf("attribution: load attributed plants for region %d: %w", attributedRegionID, err)
	}
	defer rows.Close()

	var plants []PlantRef
	for rows.Next() {
		var ref PlantRef
		if err := rows.Scan(&ref.PlantID, &ref.Name); err != nil {
			return nil, 0, fmt.Errorf("attribution: scan attributed plant for region %d: %w", attributedRegionID, err)
		}
		plants = append(plants, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("attribution: iterate attributed plants for region %d: %w", attributedRegionID, err)
	}

	return plants, attributedRegionID, nil
}

// SensorRef is one sensor attributed to a region at a point in time --
// AttributedSensors' result shape. Carries just enough for the read path
// (leaflab/api/readings) to key a query by sensor_id and disambiguate
// measurement type via sensor_type_id, mirroring PlantRef's "smallest
// shape" rationale above.
type SensorRef struct {
	SensorID     int64
	SensorTypeID int64
}

// AttributedSensors returns every sensor whose reading, at at, attributes
// to regionID under FR23's nearest-ancestor rule (attribute_region_plants,
// migration 019) -- the reverse direction of ResolvePlants: given the
// attributing region, which sensors' readings land there. A candidate
// sensor is any sensor in regionID's subtree (v_region_path.path_ids
// contains regionID); it counts only if attribute_region_plants(the
// sensor's own region, at) resolves back to regionID -- i.e. no region
// strictly between the sensor and regionID holds an active plant of its
// own at at, per A11's nearest-ancestor-only rule.
//
// Bounded by the household's sensor count, not by anything time-range
// related -- callers needing this over a series
// (leaflab/api/readings.Reader.Series) call it once per
// plant_region_history interval rather than per bucket (documented
// limitation: this evaluates attribution at one instant, at, for the whole
// interval it is asked about, so a *different* plant entering or leaving
// elsewhere on the path strictly inside that interval is not separately
// detected).
func (r *Resolver) AttributedSensors(ctx context.Context, regionID int64, at time.Time) ([]SensorRef, error) {
	rows, err := r.db.Query(ctx, `
		SELECT s.sensor_id, s.sensor_type_id
		FROM sensor s
		JOIN v_region_path rp ON rp.region_id = s.region_id
		WHERE $1 = ANY(rp.path_ids)
		  AND EXISTS (
		      SELECT 1 FROM attribute_region_plants(s.region_id, $2) arp
		      WHERE arp.attributed_region_id = $1
		  )
		ORDER BY s.sensor_id
	`, regionID, at)
	if err != nil {
		return nil, fmt.Errorf("attribution: load sensors attributed to region %d at %s: %w", regionID, at, err)
	}
	defer rows.Close()

	var sensors []SensorRef
	for rows.Next() {
		var ref SensorRef
		if err := rows.Scan(&ref.SensorID, &ref.SensorTypeID); err != nil {
			return nil, fmt.Errorf("attribution: scan attributed sensor for region %d: %w", regionID, err)
		}
		sensors = append(sensors, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attribution: iterate attributed sensors for region %d: %w", regionID, err)
	}
	return sensors, nil
}

// AttributedRegions is AttributedSensors' region-space twin: it returns
// every region -- including regionID itself -- in regionID's subtree
// (v_region_path.path_ids contains regionID) whose reading, at at,
// attributes to regionID under FR23's nearest-ancestor rule
// (attribute_region_plants, migration 019/021).
//
// AttributedSensors' candidate set is "any sensor whose *currently cached*
// region (sensor.region_id) is in the subtree" -- correct for
// GetCurrentValues, which only cares where a sensor is now. A bounded
// historical series, by contrast, must key off each reading's own
// denormalized region_id column (sensor_reading/its tier tables, stamped at
// write time -- see leaflab/api/readings' package doc comment's "region"
// entity-kind semantics) so that a sensor's later move never retroactively
// changes which of its past readings a plant's series includes. This
// function's candidate set is therefore "any region in the subtree," not
// "any sensor" -- leaflab/api/readings.Reader.seriesForPlant queries
// sensor_reading/the tier tables directly by this result's region_id set,
// the same way it already does for a plain region entity ref, rather than
// joining through sensor at all.
func (r *Resolver) AttributedRegions(ctx context.Context, regionID int64, at time.Time) ([]int64, error) {
	rows, err := r.db.Query(ctx, `
		SELECT rp.region_id
		FROM v_region_path rp
		WHERE $1 = ANY(rp.path_ids)
		  AND EXISTS (
		      SELECT 1 FROM attribute_region_plants(rp.region_id, $2) arp
		      WHERE arp.attributed_region_id = $1
		  )
		ORDER BY rp.region_id
	`, regionID, at)
	if err != nil {
		return nil, fmt.Errorf("attribution: load regions attributed to region %d at %s: %w", regionID, at, err)
	}
	defer rows.Close()

	var regionIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("attribution: scan attributed region for region %d: %w", regionID, err)
		}
		regionIDs = append(regionIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("attribution: iterate attributed regions for region %d: %w", regionID, err)
	}
	return regionIDs, nil
}
