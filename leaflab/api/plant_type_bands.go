package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
)

// This file is #1382's Implementation of FR58 (band criterion, discharged
// against FR27) -- migration 035's plant_type_band table and api.proto's
// SetPlantTypeBands/GetPlantTypeBands RPCs.
//
// V1 stores and renders bands only -- no evaluation, no notification (the
// issue text, restated on api.proto's section doc comment). Nothing in
// this file, or in leaflab/api/readings' band-resolution logic (the other
// half of this task, wired onto GetCurrentValues), is permitted to write
// an alert/notification row, schedule anything, or fire anything: every
// function below either stores a band set as given, or reads one back.
// There is no evaluator and no scheduler anywhere on this branch -- that
// is deliberate, not an oversight (the alerts subsystem is a plan of its
// own, out of scope for V1 entirely).
//
// Bands hang off the plant type (FR55), so this file's authorization split
// mirrors plant_types.go exactly: member-or-grantee (standing in with
// member-only, per that file's doc comment) for the caller's own
// household-owned type, FR10 elevation (requireGlobalWriteElevation) for a
// global type. FR8 audit recording applies to SetPlantTypeBands the same
// way it applies to every plant_type write (stampPlantTypeAudit).
//
// No server.go handler wiring yet, matching every other PlantType RPC in
// this package (plant_types.go's doc comment) -- both functions below are
// exercised directly by the Testing phase.

// PlantTypeBandRow is one plant_type_band row.
type PlantTypeBandRow struct {
	PlantTypeBandID int64
	PlantTypeID     int64
	SensorTypeID    int64
	BandLabel       string
	MinValue        *float64
	MaxValue        *float64
	SortOrder       int32
}

// PlantTypeBandSpec is one caller-supplied band within a SetPlantTypeBands
// call -- the domain-side twin of api.proto's PlantTypeBandSpec message.
type PlantTypeBandSpec struct {
	BandLabel string
	MinValue  *float64
	MaxValue  *float64
	SortOrder int32
}

// GetPlantTypeBands reads back plantTypeID's configured bands, narrowed to
// sensorTypeID when it is non-zero, unfiltered (every measurement type
// this plant type has bands for) when it is zero. Ordered by
// (sensor_type_id, sort_order) so an unfiltered read groups naturally by
// measurement type in caller order. Returns an empty slice, never an
// error, when the plant type has no bands configured for the requested
// scope (this task's Testing criterion: "A plant type with no bands
// returns values with no band field, not an error" -- the same "empty,
// not an error" shape applies to this read).
//
// Deliberately does not authorize plantTypeID against scope: reading a
// plant type's bands carries the same visibility as reading the plant
// type itself (global rows are readable by anyone per FR55; an owned row
// a caller cannot see is already unreachable because they cannot resolve
// its plant_type_id in the first place) -- mirrors GetPlantTypeByID's own
// "not itself authorizing" doc comment.
func (r *Repository) GetPlantTypeBands(ctx context.Context, plantTypeID int64, sensorTypeID int64) ([]PlantTypeBandRow, error) {
	var sqlQuery string
	args := []any{plantTypeID}
	if sensorTypeID != 0 {
		sqlQuery = `
			SELECT plant_type_band_id, plant_type_id, sensor_type_id, band_label, min_value, max_value, sort_order
			FROM plant_type_band
			WHERE plant_type_id = $1 AND sensor_type_id = $2
			ORDER BY sensor_type_id, sort_order
		`
		args = append(args, sensorTypeID)
	} else {
		sqlQuery = `
			SELECT plant_type_band_id, plant_type_id, sensor_type_id, band_label, min_value, max_value, sort_order
			FROM plant_type_band
			WHERE plant_type_id = $1
			ORDER BY sensor_type_id, sort_order
		`
	}

	rows, err := r.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("get plant type bands for plant type %d: %w", plantTypeID, err)
	}
	defer rows.Close()

	var bands []PlantTypeBandRow
	for rows.Next() {
		var row PlantTypeBandRow
		if err := rows.Scan(&row.PlantTypeBandID, &row.PlantTypeID, &row.SensorTypeID, &row.BandLabel, &row.MinValue, &row.MaxValue, &row.SortOrder); err != nil {
			return nil, fmt.Errorf("scan plant type band: %w", err)
		}
		bands = append(bands, row)
	}
	return bands, rows.Err()
}

// validateBandSpecs returns a persona-appropriate reason (FR59.2) if bands
// is invalid independent of overlap: empty, a blank label, a duplicate
// label (migration 035's UNIQUE(plant_type_id, sensor_type_id, band_label)
// would otherwise surface as an opaque constraint-violation error), or a
// minimum that is not below its own maximum.
func validateBandSpecs(bands []PlantTypeBandSpec) string {
	if len(bands) == 0 {
		return "At least one band is required."
	}
	seen := make(map[string]bool, len(bands))
	for _, b := range bands {
		label := strings.TrimSpace(b.BandLabel)
		if label == "" {
			return "Every band needs a label."
		}
		if seen[label] {
			return fmt.Sprintf("Band label %q is used more than once.", label)
		}
		seen[label] = true
		if b.MinValue != nil && b.MaxValue != nil && *b.MinValue >= *b.MaxValue {
			return fmt.Sprintf("Band %q has a minimum that is not below its maximum.", label)
		}
	}
	return ""
}

// bandLowerBound/bandUpperBound stand in for a band's MinValue/MaxValue
// with +/-infinity substitutes for the unbounded (nil) case, so
// findOverlappingBands' comparisons need no separate nil-handling branch.
func bandLowerBound(b PlantTypeBandSpec) float64 {
	if b.MinValue == nil {
		return math.Inf(-1)
	}
	return *b.MinValue
}

func bandUpperBound(b PlantTypeBandSpec) float64 {
	if b.MaxValue == nil {
		return math.Inf(1)
	}
	return *b.MaxValue
}

// findOverlappingBands returns the labels of every band in bands that
// overlaps another, or nil if none do. A band's range is
// [MinValue, MaxValue) -- MinValue unset means unbounded below, MaxValue
// unset means unbounded above (migration 035's NULL columns) -- so two
// bands overlap when their half-open ranges intersect. Gaps between bands
// are permitted and produce no overlap (this task's stated rule: "bands
// must not overlap; gaps are permitted and produce no band").
//
// Sorts a copy by lower bound (unbounded-below first) before the adjacent
// comparison: for a valid (non-overlapping) set this is the same order
// sort_order should already describe, but this function checks the
// caller-declared numeric ranges themselves, never sort_order, since
// sort_order is a display/walk-order hint (api.proto's PlantTypeBandSpec
// doc comment), not a claim about numeric order. Adjacent-pair comparison
// after sorting by lower bound is sufficient to catch every overlapping
// pair, not just consecutive ones: if band A's range strictly contains
// band C's range with band B's range between them by lower bound alone,
// A already overlaps B (A's upper bound exceeds B's lower bound, since B
// sits inside A), so the adjacent check still flags A as offending; the
// non-adjacent A/C overlap is redundant to report once A/B and (if C
// nests further) B/C are already flagged.
func findOverlappingBands(bands []PlantTypeBandSpec) []string {
	ordered := make([]PlantTypeBandSpec, len(bands))
	copy(ordered, bands)
	sort.SliceStable(ordered, func(i, j int) bool {
		return bandLowerBound(ordered[i]) < bandLowerBound(ordered[j])
	})

	offending := map[string]bool{}
	for i := 1; i < len(ordered); i++ {
		prev, cur := ordered[i-1], ordered[i]
		// prev overlaps cur when prev's upper bound is strictly after cur's
		// lower bound -- the standard half-open-interval overlap test.
		if bandUpperBound(prev) > bandLowerBound(cur) {
			offending[prev.BandLabel] = true
			offending[cur.BandLabel] = true
		}
	}
	if len(offending) == 0 {
		return nil
	}
	names := make([]string, 0, len(offending))
	for _, b := range bands {
		if offending[b.BandLabel] {
			names = append(names, b.BandLabel)
		}
	}
	return names
}

// SetPlantTypeBands replaces the full band set for one
// (plantTypeID, sensorTypeID) pair -- not a delta/patch (api.proto's
// SetPlantTypeBandsRequest doc comment). Authorization mirrors
// CreatePlantType/RenamePlantType/RetirePlantType exactly:
// authorizePlantTypeWrite resolves and scopes plantTypeID, then a global
// row additionally requires requireGlobalWriteElevation (FR10) -- a
// household-owned row needs no elevation, just membership in the owning
// household (FR7, standing in with member-only per plant_types.go's doc
// comment).
//
// Refuses (FR59.3's refused_with_alternative shape, via contract.Refuse)
// naming the offending bands when bands overlap (findOverlappingBands) --
// nothing is written in that case, matching FR59.3's "refused before
// anything is written" contract. Refuses (FR59.2 invalid_argument) on an
// empty set, a blank or duplicate label, or an inverted min/max
// (validateBandSpecs) before ever touching overlap or the database.
func (r *Repository) SetPlantTypeBands(ctx context.Context, plantTypeID, sensorTypeID int64, bands []PlantTypeBandSpec, adminSubject string, scope authz.Scope, entry audit.Entry) ([]PlantTypeBandRow, error) {
	if reason := validateBandSpecs(bands); reason != "" {
		return nil, contract.InvalidArgument("plant_type_band", "bands", reason)
	}
	if offending := findOverlappingBands(bands); len(offending) > 0 {
		return nil, contract.Refuse(
			"plant_type_band", "bands",
			fmt.Sprintf("These bands overlap: %s.", strings.Join(offending, ", ")),
			"Adjust the overlapping bands so their ranges don't intersect, then try again.",
		)
	}

	typeRow, err := r.authorizePlantTypeWrite(ctx, plantTypeID, scope)
	if err != nil {
		return nil, err
	}
	if typeRow.HouseholdID == nil {
		if err := r.requireGlobalWriteElevation(ctx, adminSubject); err != nil {
			return nil, err
		}
	}

	var stored []PlantTypeBandRow
	writeErr := r.auditedWrite(ctx, func(tx pgx.Tx) (audit.Entry, error) {
		if _, err := tx.Exec(ctx, `
			DELETE FROM plant_type_band
			WHERE plant_type_id = $1 AND sensor_type_id = $2
		`, plantTypeID, sensorTypeID); err != nil {
			return audit.Entry{}, fmt.Errorf("clear existing plant type bands for plant type %d, sensor type %d: %w", plantTypeID, sensorTypeID, err)
		}

		for _, b := range bands {
			var row PlantTypeBandRow
			if err := tx.QueryRow(ctx, `
				INSERT INTO plant_type_band (plant_type_id, sensor_type_id, band_label, min_value, max_value, sort_order)
				VALUES ($1, $2, $3, $4, $5, $6)
				RETURNING plant_type_band_id, plant_type_id, sensor_type_id, band_label, min_value, max_value, sort_order
			`, plantTypeID, sensorTypeID, strings.TrimSpace(b.BandLabel), b.MinValue, b.MaxValue, b.SortOrder).Scan(
				&row.PlantTypeBandID, &row.PlantTypeID, &row.SensorTypeID, &row.BandLabel, &row.MinValue, &row.MaxValue, &row.SortOrder,
			); err != nil {
				return audit.Entry{}, fmt.Errorf("insert plant type band %q for plant type %d, sensor type %d: %w", b.BandLabel, plantTypeID, sensorTypeID, err)
			}
			stored = append(stored, row)
		}

		stampPlantTypeAudit(&entry, plantTypeID, typeRow.HouseholdID)
		return entry, nil
	})
	if writeErr != nil {
		return nil, writeErr
	}
	return stored, nil
}
