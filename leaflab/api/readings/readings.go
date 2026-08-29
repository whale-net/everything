// Package readings is the one place FR25.1's join, enrichment and
// attribution logic for the bounded read path lives -- GetReadingSeries,
// GetCurrentValues, GetPeriodSummary and CompareSeries (leaflab/api/proto/
// api.proto) all delegate here, so no consumer reimplements the join
// (FR25.1: "Join, enrichment and attribution logic is not reimplemented
// by any consumer -- it lives in one place server-side").
//
// Reader composes three packages that already exist, rather than
// reimplementing any of them:
//   - leaflab/api/tiers.Select picks which granularity tier answers a
//     bounded window (FR71, NFR3.2's 48-hour raw cap).
//   - leaflab/api/attribution.Resolver applies FR23's nearest-ancestor
//     plant attribution -- above the aggregate, never through
//     v_sensor_reading_with_plant (FR72's corrected view is deliberately
//     not on this read path; see this task's Implementation section).
//   - leaflab/api/authz.EntityRef/Scope gate every entity this package is
//     asked about -- Reader itself performs no authorization; a caller
//     (the RPC handler, this task's Implementation phase) resolves the
//     entity against the caller's Scope before calling in here, per
//     NFR2's one-resolve-one-check shape.
//
// Entity-kind semantics (Implementation phase, #1362):
//   - sensor / board: sensor_id / board's sensor set, resolved via the
//     `sensor` table (board -> sensor.board_id). Measurement-type
//     filtering (FR25.2) narrows the sensor set by sensor.sensor_type_id.
//   - region: the tier tables' own region_id column -- the *physical*
//     region a reading's sensor was in at write time (denormalized on
//     sensor_reading, migration 001/022), never FR23 attribution. A region
//     entity ref means "readings recorded in this region," not "readings
//     attributed to a plant living in it."
//   - plant: FR23 attribution. A plant's own plant_region_history
//     (migration 017) intervals are walked one at a time; for each, the
//     region's attributed sensor set is resolved via
//     attribution.Resolver.AttributedSensors, and FR20's boundary_partial
//     rows are substituted for any bucket a placement boundary split
//     during that interval (see seriesForPlant/aggregatedPointsForPlantInterval).
//     This evaluates attribution once per plant-owned interval (at that
//     interval's own boundaries), not per bucket or per instant inside
//     it -- a documented limitation: a *different* plant entering or
//     leaving elsewhere on the sensor's ancestor path strictly inside one
//     of this plant's own intervals is not separately detected. FR20's
//     capture mechanism (leaflab/api/capture) is what makes the plant's
//     *own* moves exact even so; it is also not yet wired into
//     leaflab/api/placement's writer (a separate, already-filed scope
//     gap), so boundary_partial rows do not exist in production data yet
//     -- this package's substitution logic is exercised by inserting them
//     directly, the same way leaflab/api/capture's own tests do, until
//     that wiring lands.
package readings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/attribution"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/api/suspect"
	"github.com/whale-net/everything/leaflab/api/tiers"
)

// ErrUnboundedWindow is returned when a Window has a zero Start or End --
// NFR3.2's "no caller can request an unbounded scan," rejected rather than
// served as a scan.
var ErrUnboundedWindow = errors.New("readings: window start and end must both be set")

// ErrInvalidWindow is returned when a Window's End is not after its Start.
var ErrInvalidWindow = errors.New("readings: window end must be after start")

// ErrUnsupportedEntityKind is returned when an RPC is asked about an
// authz.EntityKind it does not support (e.g. EntityReading, which is never
// itself the subject of a readings query -- see api.proto's EntityRef
// comment).
var ErrUnsupportedEntityKind = errors.New("readings: unsupported entity kind for this request")

// ErrTooFewEntities is returned by Compare when fewer than two entities are
// given -- FR25.3: "two or more entities compared over one shared window."
var ErrTooFewEntities = errors.New("readings: compare requires at least two entities")

// ErrMeasurementRequired is returned by Compare when measurementTypeID is
// zero -- FR25.3's "one measurement" is a required filter for a comparison,
// unlike Series/PeriodSummary where zero means unfiltered.
var ErrMeasurementRequired = errors.New("readings: compare requires exactly one measurement type")

// validateWindow enforces NFR3.2's "no unbounded scan" at the one place
// every read-path method calls through before touching the database.
func validateWindow(w Window) error {
	if w.Start.IsZero() || w.End.IsZero() {
		return ErrUnboundedWindow
	}
	if !w.End.After(w.Start) {
		return ErrInvalidWindow
	}
	return nil
}

// Window is an explicitly bounded [Start, End) time range (FR25.1,
// NFR3.2). A zero Start or End is what "unbounded" looks like on this
// type -- the Implementation phase rejects it rather than serving a scan.
type Window struct {
	Start time.Time
	End   time.Time
}

// Point is one bucket in a series: a single raw reading (tiers.TierRaw) or
// one pre-aggregated bucket (tiers.TierFiveMinute/TierHourly). Min/Max/Avg/
// Count are only meaningful for an aggregated tier; a raw point carries
// Min == Max == Avg == Value and Count == 1 (Implementation phase fills in
// this shape -- see leaflab/api/proto/api.proto's ReadingPoint, the wire
// twin this type maps to).
type Point struct {
	RecordedAt time.Time
	Value      float64
	Min        float64
	Max        float64
	Avg        float64
	Count      int64
	// BoundaryPartial is true when this point substitutes an FR20
	// boundary_partial row for a bucket straddled by a plant-attribution
	// boundary, rather than an ordinary tier bucket.
	BoundaryPartial bool

	// SuspectChecks is FR26.3's per-point marker: every named,
	// enumerable suspect.Check that applies to this point. Nil when the
	// point is not suspect (suspect.Marker's own nil-vs-empty
	// convention). Left unpopulated by this task's Scaffold phase --
	// Implementation wires each query path to fill it in alongside the
	// row it is derived from.
	SuspectChecks []suspect.Check

	// readingID is the raw tier's tie-break for FR61's (recorded_at DESC,
	// reading_id) keyset order -- zero and unused for an aggregated-tier
	// point, since (sensor set, bucket) is already unique there after the
	// per-bucket GROUP BY this package applies. Unexported: it exists only
	// to let this package's own pagination logic resume a raw series
	// exactly, never on the wire (server.go builds pb.ReadingPoint field by
	// field and does not carry it).
	readingID int64
}

// Selection carries a tiers.Selection alongside the human-facing
// disclosure every read-path response makes (FR71): which tier actually
// answered, and whether it differs from what was requested.
type Selection = tiers.Selection

// Page is the keyset page window a Series/Compare call is asked for and
// the opaque token it hands back for the next page (FR61) -- mirrors
// leaflab/api/proto's PageRequest/PageResponse without depending on the pb
// package directly, same as every other domain package under leaflab/api.
type Page struct {
	Token string
	Size  int32
}

// SeriesResult is GetReadingSeries's domain-side result.
type SeriesResult struct {
	Points        []Point
	Tier          Selection
	NextPageToken string

	// MarkedCount/ReturnedCount are FR26.3's top-level tally --
	// suspect.CountMarkers over Points' SuspectChecks. Left zero-valued
	// by this task's Scaffold phase; Implementation phase populates
	// them from the same query path that fills in each Point's
	// SuspectChecks.
	MarkedCount   int64
	ReturnedCount int64
}

// CurrentValue is one sensor's latest raw reading (FR27: "served from the
// latest raw readings, never from a pre-aggregated tier").
type CurrentValue struct {
	SensorID          int64
	MeasurementTypeID int64
	Value             float64
	RecordedAt        time.Time
	// Band is FR58's band descriptor. Left zero-valued until Phase 5 --
	// see this task's Implementation section and api.proto's Band
	// message.
	Band string
	// SuspectChecks is FR26.3's per-value marker -- see Point.SuspectChecks.
	SuspectChecks []suspect.Check
}

// CurrentPlantValue is one plant's current value set: every sensor value
// from the plant's attributing region (FR23), via attribution.Resolver.
type CurrentPlantValue struct {
	PlantID int64
	Values  []CurrentValue
}

// CurrentValuesResult is GetCurrentValues's domain-side result. Exactly
// one of Values (sensor/board/region ref) or PlantValues (plant ref) is
// populated, mirroring the entity kind the request named.
type CurrentValuesResult struct {
	Values      []CurrentValue
	PlantValues []CurrentPlantValue

	// MarkedCount/ReturnedCount are FR26.3's top-level tally -- see
	// SeriesResult's fields of the same name.
	MarkedCount   int64
	ReturnedCount int64
}

// SummaryStat is one measurement type's min/max/average over a period
// (FR28), exact at the hourly tier for min and max (FR71).
type SummaryStat struct {
	MeasurementTypeID int64
	Min               float64
	Max               float64
	Avg               float64
	MinAt             time.Time
	MaxAt             time.Time
	// SuspectChecks is FR26.3's marker for this summary -- every named
	// check that applies to at least one hourly-tier bucket contributing
	// to it. See Point.SuspectChecks.
	SuspectChecks []suspect.Check
}

// PeriodSummaryResult is GetPeriodSummary's domain-side result.
// OvernightLow/DaytimeHigh are the same summary windowed against the
// household's (or the server's -- Implementation phase states which)
// configured day boundary, computed server-side, never a client
// convention.
type PeriodSummaryResult struct {
	Summaries    []SummaryStat
	OvernightLow *SummaryStat
	DaytimeHigh  *SummaryStat
	// Timezone is the IANA name the day boundary was computed against.
	Timezone string
	Tier     Selection

	// MarkedCount/ReturnedCount are FR26.3's top-level tally -- see
	// SeriesResult's fields of the same name.
	MarkedCount   int64
	ReturnedCount int64
}

// EntitySeries pairs one CompareSeries entity with its aligned series.
type EntitySeries struct {
	Entity authz.EntityRef
	Points []Point
}

// CompareResult is CompareSeries's domain-side result: 2+ entities aligned
// on one shared window and one measurement (FR25.3).
type CompareResult struct {
	Series        []EntitySeries
	Tier          Selection
	NextPageToken string

	// MarkedCount/ReturnedCount are FR26.3's top-level tally -- see
	// SeriesResult's fields of the same name.
	MarkedCount   int64
	ReturnedCount int64
}

// Reader is the single implementation of the bounded read path's join,
// enrichment and attribution logic (FR25.1). Every RPC handler
// (Implementation phase) that answers a readings request calls through
// here rather than querying sensor_reading or its tiers directly.
type Reader struct {
	db          *pgxpool.Pool
	attribution *attribution.Resolver
}

// NewReader constructs a Reader over db.
func NewReader(db *pgxpool.Pool) *Reader {
	return &Reader{db: db, attribution: attribution.NewResolver(db)}
}

// periodSummaryTimezone is the IANA name PeriodSummary's overnight-low/
// daytime-high day boundary is computed against. V1 has no household- or
// server-configured timezone anywhere in the schema (checked: no such
// column/setting exists as of this task) -- UTC is the only value that
// does not silently assume a timezone nobody configured. A later phase
// that adds a household timezone setting should thread it through here
// instead of hardcoding this constant.
const periodSummaryTimezone = "UTC"

// Overnight/daytime hour boundaries (in periodSummaryTimezone), a fixed V1
// convention documented here since FR28 does not specify exact hours:
// "night" is 22:00 up to (not including) 06:00, "day" is the complement.
const (
	overnightStartHour = 22
	overnightEndHour   = 6
	daytimeStartHour   = 6
	daytimeEndHour     = 22
)

// seriesIntervalRowCap is a defensive bound on how many rows a single
// plant-placement interval's query may return (seriesForPlant,
// aggregatedPointsForPlantInterval) -- distinct from FR61's page-size cap
// (contract.PageCap), which bounds the *response*, not an intermediate
// per-interval fetch. A plant's own placement intervals are few, but this
// keeps one pathologically long-lived interval from turning into an
// unbounded scan on its own.
const seriesIntervalRowCap = 5000

// seriesCursor is the decoded form of a Page.Token for the tier-backed
// series/compare queries below -- see contract.DecodeReadingCursor.
type seriesCursor struct {
	hasAfter  bool
	afterTime time.Time
	afterID   int64
}

// Series answers GetReadingSeries for entity over window, filtered by
// measurementTypeID (0 = unfiltered) and served from the tiers
// (leaflab/api/tiers), never through v_sensor_reading_with_plant -- see
// this task's Implementation section. requested is a hint (FR71); the
// returned Selection always states which tier actually answered.
func (r *Reader) Series(ctx context.Context, entity authz.EntityRef, window Window, measurementTypeID int64, requested tiers.Tier, page Page, invalidOnly bool) (SeriesResult, error) {
	if err := validateWindow(window); err != nil {
		return SeriesResult{}, err
	}
	selection, err := tiers.Select(requested, window.Start, window.End)
	if err != nil {
		return SeriesResult{}, err
	}
	if invalidOnly {
		// FR26.1's Super Admin invalid-only filter is only meaningful at
		// the raw tier: sensor_reading.valid is a per-reading flag with no
		// aggregated-tier analogue (an aggregated bucket blends valid and
		// invalid readings together), so a request for invalid-only
		// readings is always served from raw, disclosed the same way any
		// other coarsening decision is (Selection.Coarsened).
		selection = tiers.Selection{Tier: tiers.TierRaw, Coarsened: requested != tiers.TierRaw}
	}

	ranges, err := r.measurementRanges(ctx)
	if err != nil {
		return SeriesResult{}, err
	}

	limit := contract.ClampPageSize(page.Size)
	afterTime, afterID, hasAfter, err := contract.DecodeReadingCursor(page.Token)
	if err != nil {
		return SeriesResult{}, fmt.Errorf("readings: decode page token: %w", err)
	}
	cursor := seriesCursor{hasAfter: hasAfter, afterTime: afterTime, afterID: afterID}

	points, err := r.pointsForEntity(ctx, entity, selection.Tier, window, measurementTypeID, limit+1, cursor, ranges, invalidOnly)
	if err != nil {
		return SeriesResult{}, err
	}

	var nextToken string
	if int32(len(points)) > limit {
		last := points[limit-1]
		nextToken = contract.EncodeReadingCursor(last.RecordedAt, last.readingID)
		points = points[:limit]
	}

	counts := suspect.CountMarkers(pointMarkers(points))
	return SeriesResult{
		Points:        points,
		Tier:          selection,
		NextPageToken: nextToken,
		MarkedCount:   counts.Marked,
		ReturnedCount: counts.Returned,
	}, nil
}

// pointsForEntity dispatches to the entity-kind-specific query strategy
// documented on this package's doc comment. rowCap is the exact number of
// rows the caller wants back (already inflated by one over the page size,
// by convention, so the caller can detect "more pages exist" without a
// second query) -- every branch below honors it as a hard cap, not a hint.
func (r *Reader) pointsForEntity(ctx context.Context, entity authz.EntityRef, tier tiers.Tier, window Window, measurementTypeID int64, rowCap int32, cursor seriesCursor, ranges map[int64]measurementRange, invalidOnly bool) ([]Point, error) {
	switch entity.Kind {
	case authz.EntitySensor, authz.EntityBoard:
		sensorIDs, err := r.resolveSensorIDs(ctx, entity.Kind, entity.ID, measurementTypeID)
		if err != nil {
			return nil, err
		}
		if len(sensorIDs) == 0 {
			return nil, nil
		}
		return r.seriesForSensorSet(ctx, sensorIDs, tier, window, rowCap, cursor, ranges, measurementTypeID, invalidOnly)
	case authz.EntityRegion:
		return r.seriesForRegion(ctx, entity.ID, tier, window, measurementTypeID, rowCap, cursor, ranges, invalidOnly)
	case authz.EntityPlant:
		return r.seriesForPlant(ctx, entity.ID, tier, window, measurementTypeID, rowCap, cursor, ranges, invalidOnly)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEntityKind, entity.Kind)
	}
}

// tierTable returns the physical relation and its time column for tier --
// sensor_reading/recorded_at for raw, sensor_reading_5m/bucket and
// sensor_reading_1h/bucket for migration 022's two continuous aggregates.
func tierTable(tier tiers.Tier) (table, timeCol string) {
	switch tier {
	case tiers.TierFiveMinute:
		return "sensor_reading_5m", "bucket"
	case tiers.TierHourly:
		return "sensor_reading_1h", "bucket"
	default:
		return "sensor_reading", "recorded_at"
	}
}

// resolveSensorIDs resolves the sensor set a sensor/board entity ref names,
// narrowed by measurementTypeID (0 = unfiltered).
func (r *Reader) resolveSensorIDs(ctx context.Context, kind authz.EntityKind, entityID, measurementTypeID int64) ([]int64, error) {
	var query string
	switch kind {
	case authz.EntitySensor:
		query = `SELECT sensor_id FROM sensor WHERE sensor_id = $1`
	case authz.EntityBoard:
		query = `SELECT sensor_id FROM sensor WHERE board_id = $1`
	default:
		return nil, fmt.Errorf("readings: resolveSensorIDs called with unsupported kind %q", kind)
	}
	args := []any{entityID}
	if measurementTypeID != 0 {
		query += " AND sensor_type_id = $2"
		args = append(args, measurementTypeID)
	}
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("readings: resolve sensor set for %s %d: %w", kind, entityID, err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("readings: scan sensor id for %s %d: %w", kind, entityID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate sensor set for %s %d: %w", kind, entityID, err)
	}
	return ids, nil
}

// seriesForSensorSet queries tier for every sensorID in sensorIDs, within
// [window.Start, window.End), returning at most rowCap points ordered most
// recent first -- one point per bucket for an aggregated tier (merged
// across every sensor in the set with SUM/MIN/MAX, since a sensor/board
// series is one shared line, not a per-sensor breakdown -- CompareSeries,
// not this, is where per-entity breakdown lives) or one point per raw
// reading for the raw tier (never merged: raw is exact, and merging would
// silently turn an exact reading into an implicit average).
func (r *Reader) seriesForSensorSet(ctx context.Context, sensorIDs []int64, tier tiers.Tier, window Window, rowCap int32, cursor seriesCursor, ranges map[int64]measurementRange, measurementTypeID int64, invalidOnly bool) ([]Point, error) {
	if tier == tiers.TierRaw {
		where := "sr.sensor_id = ANY($1)"
		if invalidOnly {
			where += " AND sr.valid = FALSE"
		}
		return r.rawPoints(ctx, where, []any{sensorIDs}, window, rowCap, cursor, ranges)
	}

	table, timeCol := tierTable(tier)
	where := fmt.Sprintf("sensor_id = ANY($1) AND %s >= $2 AND %s < $3", timeCol, timeCol)
	args := []any{sensorIDs, window.Start, window.End}
	next := 4
	if cursor.hasAfter {
		where += fmt.Sprintf(" AND %s < $%d", timeCol, next)
		args = append(args, cursor.afterTime)
		next++
	}
	query := fmt.Sprintf(`
		SELECT %s,
		       coalesce(sum(reading_count), 0)::bigint,
		       coalesce(sum(value_sum), 0),
		       min(value_min),
		       max(value_max)
		FROM %s
		WHERE %s
		GROUP BY %s
		ORDER BY %s DESC
		LIMIT $%d
	`, timeCol, table, where, timeCol, timeCol, next)
	args = append(args, rowCap)

	points, err := r.scanAggregatedPoints(ctx, query, args)
	if err != nil {
		return nil, err
	}
	// fixedRegionID is 0 here: a sensor/board series is merged across every
	// sensor in the set (this function's own doc comment) -- possibly
	// spanning more than one region -- so CheckStaleAttribution and
	// CheckMigrationSnapWindow, which both require one fixed region to
	// compare against, are not evaluated for this entity kind's aggregated
	// tiers (documented limitation; both checks are fully evaluated at the
	// raw tier above, and for region/plant entity kinds at every tier).
	if err := r.markAggregatedPoints(ctx, points, tier, "sensor_id = ANY($1)", []any{sensorIDs}, window, ranges, measurementTypeID, 0); err != nil {
		return nil, err
	}
	return points, nil
}

// seriesForRegion serves a region entity ref directly from the tier
// tables' own region_id column -- the physical region a reading's sensor
// was in at write time (see this package's doc comment) -- narrowed by
// measurementTypeID (0 = unfiltered) via a join to sensor for its
// sensor_type_id.
func (r *Reader) seriesForRegion(ctx context.Context, regionID int64, tier tiers.Tier, window Window, measurementTypeID int64, rowCap int32, cursor seriesCursor, ranges map[int64]measurementRange, invalidOnly bool) ([]Point, error) {
	// invalidWhere/invalidArgs is this region's plain (unaliased) filter
	// against sensor_reading itself -- reused below both for the raw-tier
	// query and, at an aggregated tier, for markAggregatedPoints'
	// idx_sensor_reading_invalid lookup, so the two never drift apart on
	// which sensors this region/type filter actually includes.
	invalidWhere := "region_id = $1"
	invalidArgs := []any{regionID}
	if measurementTypeID != 0 {
		invalidWhere += " AND sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $2)"
		invalidArgs = append(invalidArgs, measurementTypeID)
	}

	if tier == tiers.TierRaw {
		rawWhere := "sr.region_id = $1"
		rawArgs := []any{regionID}
		if measurementTypeID != 0 {
			rawWhere += " AND sr.sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $2)"
			rawArgs = append(rawArgs, measurementTypeID)
		}
		if invalidOnly {
			rawWhere += " AND sr.valid = FALSE"
		}
		return r.rawPoints(ctx, rawWhere, rawArgs, window, rowCap, cursor, ranges)
	}

	table, timeCol := tierTable(tier)
	where := fmt.Sprintf("region_id = $1 AND %s >= $2 AND %s < $3", timeCol, timeCol)
	args := []any{regionID, window.Start, window.End}
	next := 4
	if measurementTypeID != 0 {
		where += fmt.Sprintf(" AND sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $%d)", next)
		args = append(args, measurementTypeID)
		next++
	}
	if cursor.hasAfter {
		where += fmt.Sprintf(" AND %s < $%d", timeCol, next)
		args = append(args, cursor.afterTime)
		next++
	}
	query := fmt.Sprintf(`
		SELECT %s,
		       coalesce(sum(reading_count), 0)::bigint,
		       coalesce(sum(value_sum), 0),
		       min(value_min),
		       max(value_max)
		FROM %s
		WHERE %s
		GROUP BY %s
		ORDER BY %s DESC
		LIMIT $%d
	`, timeCol, table, where, timeCol, timeCol, next)
	args = append(args, rowCap)

	points, err := r.scanAggregatedPoints(ctx, query, args)
	if err != nil {
		return nil, err
	}
	// regionID is fixed for this entity kind, so CheckStaleAttribution and
	// CheckMigrationSnapWindow are fully evaluated here too (unlike
	// seriesForSensorSet's aggregated branch above).
	if err := r.markAggregatedPoints(ctx, points, tier, invalidWhere, invalidArgs, window, ranges, measurementTypeID, regionID); err != nil {
		return nil, err
	}
	return points, nil
}

// rawPoints lists individual raw readings matching whereBase (already
// containing its own positional placeholders starting at $1, qualified
// against the sr alias -- e.g. "sr.sensor_id = ANY($1)") within
// [window.Start, window.End), applying cursor's keyset predicate on
// (recorded_at DESC, reading_id DESC) per FR61, and returning at most
// rowCap rows. Every point carries its FR26.3 SuspectChecks, computed
// against the row it came from -- ranges is FR26.1's out-of-range table
// (Reader.measurementRanges, loaded once per top-level call and threaded
// down), and the region a reading was actually attributed to at the time
// (sensor_region_history, joined here per row via a LATERAL, never a
// second round trip) feeds CheckStaleAttribution. CheckMigrationSnapWindow
// is resolved after the main query returns, once the set of stamped
// region_ids actually present is known (regionSnapWindows) -- see this
// package's suspect_detect.go.
func (r *Reader) rawPoints(ctx context.Context, whereBase string, baseArgs []any, window Window, rowCap int32, cursor seriesCursor, ranges map[int64]measurementRange) ([]Point, error) {
	next := len(baseArgs) + 1
	args := append([]any{}, baseArgs...)
	where := fmt.Sprintf("%s AND sr.recorded_at >= $%d AND sr.recorded_at < $%d", whereBase, next, next+1)
	args = append(args, window.Start, window.End)
	next += 2
	if cursor.hasAfter {
		where += fmt.Sprintf(" AND (sr.recorded_at, sr.reading_id) < ($%d, $%d)", next, next+1)
		args = append(args, cursor.afterTime, cursor.afterID)
		next += 2
	}
	query := fmt.Sprintf(`
		SELECT sr.recorded_at, sr.reading_id, sr.value, sr.sensor_id, sr.region_id, sr.valid,
		       s.sensor_type_id, srh.region_id AS assigned_region_id
		FROM sensor_reading sr
		JOIN sensor s ON s.sensor_id = sr.sensor_id
		LEFT JOIN LATERAL (
			SELECT region_id
			FROM sensor_region_history
			WHERE sensor_id = sr.sensor_id
			  AND valid_from <= sr.recorded_at
			  AND (valid_to IS NULL OR valid_to > sr.recorded_at)
			LIMIT 1
		) srh ON true
		WHERE %s
		ORDER BY sr.recorded_at DESC, sr.reading_id DESC
		LIMIT $%d
	`, where, next)
	args = append(args, rowCap)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("readings: query raw series: %w", err)
	}

	type rawRow struct {
		recordedAt       time.Time
		readingID        int64
		value            float64
		sensorID         int64
		regionID         *int64
		valid            bool
		sensorTypeID     int64
		assignedRegionID *int64
	}
	var rawRows []rawRow
	for rows.Next() {
		var rr rawRow
		if err := rows.Scan(&rr.recordedAt, &rr.readingID, &rr.value, &rr.sensorID, &rr.regionID, &rr.valid, &rr.sensorTypeID, &rr.assignedRegionID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("readings: scan raw series row: %w", err)
		}
		rawRows = append(rawRows, rr)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return nil, fmt.Errorf("readings: iterate raw series: %w", rowsErr)
	}

	regionSet := make(map[int64]bool)
	for _, rr := range rawRows {
		if rr.regionID != nil {
			regionSet[*rr.regionID] = true
		}
	}
	regionIDs := make([]int64, 0, len(regionSet))
	for id := range regionSet {
		regionIDs = append(regionIDs, id)
	}
	snapWindows, err := r.regionSnapWindows(ctx, regionIDs)
	if err != nil {
		return nil, err
	}

	points := make([]Point, 0, len(rawRows))
	for _, rr := range rawRows {
		var checks []suspect.Check
		if outOfRange(ranges, rr.sensorTypeID, rr.value) {
			checks = append(checks, suspect.CheckOutOfRange)
		}
		if !rr.valid {
			checks = append(checks, suspect.CheckPersistedInvalidFlag)
		}
		if rr.regionID != nil && rr.assignedRegionID != nil && *rr.regionID != *rr.assignedRegionID {
			checks = append(checks, suspect.CheckStaleAttribution)
		}
		if rr.regionID != nil && inSnapWindow(snapWindows, *rr.regionID, rr.recordedAt) {
			checks = append(checks, suspect.CheckMigrationSnapWindow)
		}
		points = append(points, Point{
			RecordedAt:    rr.recordedAt,
			Value:         rr.value,
			Min:           rr.value,
			Max:           rr.value,
			Avg:           rr.value,
			Count:         1,
			readingID:     rr.readingID,
			SuspectChecks: checks,
		})
	}
	return points, nil
}

// scanAggregatedPoints executes query/args -- expected to return
// (bucket, reading_count, value_sum, value_min, value_max) rows, migration
// 022's four composable aggregates plus their bucket -- and converts each
// row to a Point, deriving Avg (and the headline Value) as Sum/Count, never
// stored or averaged directly (mirrors leaflab/api/capture's own
// aggregateResult convention).
func (r *Reader) scanAggregatedPoints(ctx context.Context, query string, args []any) ([]Point, error) {
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("readings: query aggregated series: %w", err)
	}
	defer rows.Close()

	var points []Point
	for rows.Next() {
		var bucket time.Time
		var count int64
		var sum float64
		var min, max sql.NullFloat64
		if err := rows.Scan(&bucket, &count, &sum, &min, &max); err != nil {
			return nil, fmt.Errorf("readings: scan aggregated series row: %w", err)
		}
		var avg float64
		if count > 0 {
			avg = sum / float64(count)
		}
		p := Point{RecordedAt: bucket, Value: avg, Avg: avg, Count: count}
		if min.Valid {
			p.Min = min.Float64
		}
		if max.Valid {
			p.Max = max.Float64
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate aggregated series: %w", err)
	}
	return points, nil
}

// regionInterval is one sub-range of a plant's placement history
// (plant_region_history, migration 017), clipped to the requested window.
type regionInterval struct {
	regionID int64
	from, to time.Time
}

// plantRegionIntervals returns plantID's plant_region_history intervals
// intersecting window, each clipped to window's own bounds -- the plant's
// own placement eras a series query walks one at a time (seriesForPlant).
func (r *Reader) plantRegionIntervals(ctx context.Context, plantID int64, window Window) ([]regionInterval, error) {
	rows, err := r.db.Query(ctx, `
		SELECT region_id, valid_from, valid_to
		FROM plant_region_history
		WHERE plant_id = $1
		  AND valid_from < $3
		  AND (valid_to IS NULL OR valid_to > $2)
		ORDER BY valid_from
	`, plantID, window.Start, window.End)
	if err != nil {
		return nil, fmt.Errorf("readings: load placement intervals for plant %d: %w", plantID, err)
	}
	defer rows.Close()

	var intervals []regionInterval
	for rows.Next() {
		var regionID int64
		var validFrom time.Time
		var validTo sql.NullTime
		if err := rows.Scan(&regionID, &validFrom, &validTo); err != nil {
			return nil, fmt.Errorf("readings: scan placement interval for plant %d: %w", plantID, err)
		}
		from := validFrom
		if from.Before(window.Start) {
			from = window.Start
		}
		to := window.End
		if validTo.Valid && validTo.Time.Before(window.End) {
			to = validTo.Time
		}
		if !from.Before(to) {
			continue // clipped to an empty range -- nothing to walk
		}
		intervals = append(intervals, regionInterval{regionID: regionID, from: from, to: to})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate placement intervals for plant %d: %w", plantID, err)
	}
	return intervals, nil
}

// seriesForPlant walks plantID's placement intervals (plantRegionIntervals)
// and, for each, queries that era's attributing region -- raw readings
// directly, or the aggregated tier with FR20 boundary_partial substitution
// (aggregatedPointsForPlantInterval) -- then merges every interval's points
// into one DESC-ordered series and applies cursor/rowCap across the
// combined result. A plant's placement intervals are few (a handful of
// moves at most in practice), so merging in Go keeps the interval-walk and
// partial-substitution logic in one place instead of a much larger dynamic
// UNION ALL query.
func (r *Reader) seriesForPlant(ctx context.Context, plantID int64, tier tiers.Tier, window Window, measurementTypeID int64, rowCap int32, cursor seriesCursor, ranges map[int64]measurementRange, invalidOnly bool) ([]Point, error) {
	intervals, err := r.plantRegionIntervals(ctx, plantID, window)
	if err != nil {
		return nil, err
	}

	var all []Point
	for _, iv := range intervals {
		var pts []Point
		var err error
		if tier == tiers.TierRaw {
			where := "sr.region_id = $1"
			args := []any{iv.regionID}
			if measurementTypeID != 0 {
				where += " AND sr.sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $2)"
				args = append(args, measurementTypeID)
			}
			if invalidOnly {
				where += " AND sr.valid = FALSE"
			}
			pts, err = r.rawPoints(ctx, where, args, Window{Start: iv.from, End: iv.to}, seriesIntervalRowCap, seriesCursor{}, ranges)
		} else {
			pts, err = r.aggregatedPointsForPlantInterval(ctx, tier, iv.regionID, iv.from, iv.to, measurementTypeID, ranges)
		}
		if err != nil {
			return nil, err
		}
		all = append(all, pts...)
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].RecordedAt.Equal(all[j].RecordedAt) {
			return all[i].readingID > all[j].readingID
		}
		return all[i].RecordedAt.After(all[j].RecordedAt)
	})

	if cursor.hasAfter {
		filtered := all[:0]
		for _, p := range all {
			if p.RecordedAt.Before(cursor.afterTime) ||
				(p.RecordedAt.Equal(cursor.afterTime) && p.readingID < cursor.afterID) {
				filtered = append(filtered, p)
			}
		}
		all = filtered
	}

	if int32(len(all)) > rowCap {
		all = all[:rowCap]
	}
	return all, nil
}

// aggregatedPointsForPlantInterval serves one plant-placement interval
// [from, to) in regionID from an aggregated tier: whole tier buckets that
// no FR20 boundary capture has split (mirroring leaflab/api/capture's own
// full-bucket exclusion, aggregate.go's fiveMinuteFullBucketsAggregate,
// generalized from one sensor to this region's whole sensor set) plus
// every boundary_partial row wholly inside [from, to) for a sensor
// currently in regionID, substituted for the whole bucket it split
// (FR20's read-path requirement).
func (r *Reader) aggregatedPointsForPlantInterval(ctx context.Context, tier tiers.Tier, regionID int64, from, to time.Time, measurementTypeID int64, ranges map[int64]measurementRange) ([]Point, error) {
	table, timeCol := tierTable(tier)

	whereWhole := fmt.Sprintf(`region_id = $1 AND %s >= $2 AND %s < $3
		AND NOT EXISTS (
			SELECT 1 FROM boundary_partial bp
			JOIN boundary_capture bc ON bc.capture_id = bp.capture_id
			JOIN sensor s2 ON s2.sensor_id = bc.sensor_id
			WHERE s2.region_id = %s.region_id
			  AND bp.tier = $4
			  AND bp.bucket_start = %s.%s
		)`, timeCol, timeCol, table, table, timeCol)
	argsWhole := []any{regionID, from, to, string(tier)}
	next := 5
	invalidWhere := "region_id = $1"
	invalidArgs := []any{regionID}
	if measurementTypeID != 0 {
		whereWhole += fmt.Sprintf(" AND sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $%d)", next)
		argsWhole = append(argsWhole, measurementTypeID)
		next++
		invalidWhere += " AND sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $2)"
		invalidArgs = append(invalidArgs, measurementTypeID)
	}
	queryWhole := fmt.Sprintf(`
		SELECT %s,
		       coalesce(sum(reading_count), 0)::bigint,
		       coalesce(sum(value_sum), 0),
		       min(value_min),
		       max(value_max)
		FROM %s
		WHERE %s
		GROUP BY %s
		ORDER BY %s DESC
		LIMIT $%d
	`, timeCol, table, whereWhole, timeCol, timeCol, next)
	argsWhole = append(argsWhole, seriesIntervalRowCap)

	wholePts, err := r.scanAggregatedPoints(ctx, queryWhole, argsWhole)
	if err != nil {
		return nil, err
	}
	// regionID is fixed for this plant-placement interval, so
	// CheckStaleAttribution and CheckMigrationSnapWindow are fully
	// evaluated here, same as seriesForRegion's aggregated branch. Marked
	// on wholePts only -- boundary_partial rows (partialPts, below) carry
	// their own distinct FR20 semantics and do not exist in production
	// data yet (see this package's doc comment); marking them is left as a
	// documented gap alongside that pre-existing one, not a new omission.
	if err := r.markAggregatedPoints(ctx, wholePts, tier, invalidWhere, invalidArgs, Window{Start: from, End: to}, ranges, measurementTypeID, regionID); err != nil {
		return nil, err
	}

	wherePartial := "s2.region_id = $1 AND bp.tier = $2 AND bp.partial_from >= $3 AND bp.partial_to <= $4"
	argsPartial := []any{regionID, string(tier), from, to}
	if measurementTypeID != 0 {
		wherePartial += " AND bc.sensor_id IN (SELECT sensor_id FROM sensor WHERE sensor_type_id = $5)"
		argsPartial = append(argsPartial, measurementTypeID)
	}
	queryPartial := fmt.Sprintf(`
		SELECT bp.partial_from, bp.reading_count, bp.value_sum, bp.value_min, bp.value_max
		FROM boundary_partial bp
		JOIN boundary_capture bc ON bc.capture_id = bp.capture_id
		JOIN sensor s2 ON s2.sensor_id = bc.sensor_id
		WHERE %s
	`, wherePartial)

	rows, err := r.db.Query(ctx, queryPartial, argsPartial...)
	if err != nil {
		return nil, fmt.Errorf("readings: query boundary partials for region %d: %w", regionID, err)
	}
	defer rows.Close()

	var partialPts []Point
	for rows.Next() {
		var partialFrom time.Time
		var count int64
		var sum, min, max float64
		if err := rows.Scan(&partialFrom, &count, &sum, &min, &max); err != nil {
			return nil, fmt.Errorf("readings: scan boundary partial for region %d: %w", regionID, err)
		}
		var avg float64
		if count > 0 {
			avg = sum / float64(count)
		}
		partialPts = append(partialPts, Point{
			RecordedAt:      partialFrom,
			Value:           avg,
			Min:             min,
			Max:             max,
			Avg:             avg,
			Count:           count,
			BoundaryPartial: true,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate boundary partials for region %d: %w", regionID, err)
	}

	return append(wholePts, partialPts...), nil
}

// CurrentValues answers GetCurrentValues for entity, always from the
// latest raw readings (FR27) -- for a plant ref, via attribution's
// nearest-ancestor walk (FR23) over the sensors beneath the attributing
// region.
func (r *Reader) CurrentValues(ctx context.Context, entity authz.EntityRef) (CurrentValuesResult, error) {
	ranges, err := r.measurementRanges(ctx)
	if err != nil {
		return CurrentValuesResult{}, err
	}

	switch entity.Kind {
	case authz.EntitySensor, authz.EntityBoard, authz.EntityRegion:
		sensorIDs, err := r.currentValueSensorIDs(ctx, entity)
		if err != nil {
			return CurrentValuesResult{}, err
		}
		values, err := r.latestRawValues(ctx, sensorIDs, ranges)
		if err != nil {
			return CurrentValuesResult{}, err
		}
		counts := suspect.CountMarkers(currentValueMarkers(values))
		return CurrentValuesResult{Values: values, MarkedCount: counts.Marked, ReturnedCount: counts.Returned}, nil
	case authz.EntityPlant:
		return r.currentPlantValues(ctx, entity.ID, ranges)
	default:
		return CurrentValuesResult{}, fmt.Errorf("%w: %q", ErrUnsupportedEntityKind, entity.Kind)
	}
}

// currentValueSensorIDs resolves the sensor set a sensor/board/region
// entity ref names for GetCurrentValues -- unlike Series, there is no
// measurement-type filter on this RPC (api.proto's GetCurrentValuesRequest
// carries only entity), so every sensor under the ref is returned.
func (r *Reader) currentValueSensorIDs(ctx context.Context, entity authz.EntityRef) ([]int64, error) {
	var query string
	switch entity.Kind {
	case authz.EntitySensor:
		query = `SELECT sensor_id FROM sensor WHERE sensor_id = $1`
	case authz.EntityBoard:
		query = `SELECT sensor_id FROM sensor WHERE board_id = $1`
	case authz.EntityRegion:
		query = `SELECT sensor_id FROM sensor WHERE region_id = $1`
	default:
		return nil, fmt.Errorf("readings: currentValueSensorIDs called with unsupported kind %q", entity.Kind)
	}
	rows, err := r.db.Query(ctx, query, entity.ID)
	if err != nil {
		return nil, fmt.Errorf("readings: resolve current-value sensor set for %s %d: %w", entity.Kind, entity.ID, err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("readings: scan current-value sensor id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate current-value sensor set: %w", err)
	}
	return ids, nil
}

// latestRawValues returns each sensorID's single latest raw reading (FR27),
// via one DISTINCT ON query against idx_sensor_reading_sensor_id
// (sensor_id, recorded_at DESC) -- bounded by len(sensorIDs), never a scan
// of sensor_reading itself.
func (r *Reader) latestRawValues(ctx context.Context, sensorIDs []int64, ranges map[int64]measurementRange) ([]CurrentValue, error) {
	if len(sensorIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT ON (sr.sensor_id) sr.sensor_id, s.sensor_type_id, sr.value, sr.recorded_at,
		       sr.region_id, sr.valid, srh.region_id AS assigned_region_id
		FROM sensor_reading sr
		JOIN sensor s ON s.sensor_id = sr.sensor_id
		LEFT JOIN LATERAL (
			SELECT region_id
			FROM sensor_region_history
			WHERE sensor_id = sr.sensor_id
			  AND valid_from <= sr.recorded_at
			  AND (valid_to IS NULL OR valid_to > sr.recorded_at)
			LIMIT 1
		) srh ON true
		WHERE sr.sensor_id = ANY($1)
		ORDER BY sr.sensor_id, sr.recorded_at DESC
	`, sensorIDs)
	if err != nil {
		return nil, fmt.Errorf("readings: query latest raw values: %w", err)
	}

	type rawValue struct {
		v                CurrentValue
		regionID         *int64
		valid            bool
		assignedRegionID *int64
	}
	var rawValues []rawValue
	for rows.Next() {
		var rv rawValue
		if err := rows.Scan(&rv.v.SensorID, &rv.v.MeasurementTypeID, &rv.v.Value, &rv.v.RecordedAt, &rv.regionID, &rv.valid, &rv.assignedRegionID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("readings: scan latest raw value: %w", err)
		}
		rawValues = append(rawValues, rv)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return nil, fmt.Errorf("readings: iterate latest raw values: %w", rowsErr)
	}

	regionSet := make(map[int64]bool)
	for _, rv := range rawValues {
		if rv.regionID != nil {
			regionSet[*rv.regionID] = true
		}
	}
	regionIDs := make([]int64, 0, len(regionSet))
	for id := range regionSet {
		regionIDs = append(regionIDs, id)
	}
	snapWindows, err := r.regionSnapWindows(ctx, regionIDs)
	if err != nil {
		return nil, err
	}

	values := make([]CurrentValue, 0, len(rawValues))
	for _, rv := range rawValues {
		v := rv.v
		if outOfRange(ranges, v.MeasurementTypeID, v.Value) {
			v.SuspectChecks = append(v.SuspectChecks, suspect.CheckOutOfRange)
		}
		if !rv.valid {
			v.SuspectChecks = append(v.SuspectChecks, suspect.CheckPersistedInvalidFlag)
		}
		if rv.regionID != nil && rv.assignedRegionID != nil && *rv.regionID != *rv.assignedRegionID {
			v.SuspectChecks = append(v.SuspectChecks, suspect.CheckStaleAttribution)
		}
		if rv.regionID != nil && inSnapWindow(snapWindows, *rv.regionID, v.RecordedAt) {
			v.SuspectChecks = append(v.SuspectChecks, suspect.CheckMigrationSnapWindow)
		}
		values = append(values, v)
	}
	return values, nil
}

// currentValueMarkers projects values' SuspectChecks into suspect.Marker
// values for suspect.CountMarkers, mirroring pointMarkers for
// readings.Point.
func currentValueMarkers(values []CurrentValue) []suspect.Marker {
	out := make([]suspect.Marker, len(values))
	for i, v := range values {
		out[i] = suspect.Marker{Checks: v.SuspectChecks}
	}
	return out
}

// currentPlantValues answers GetCurrentValues for a plant ref: it resolves
// the plant's current region (plant_region_history's open interval),
// attribution.Resolver.ResolvePlants to find every sibling plant sharing
// that attributing region (FR23's sibling disclosure), and
// attribution.Resolver.AttributedSensors to find the sensors whose readings
// land there -- then every sibling plant gets the identical current-value
// set, since they are, by construction, attributed the same readings.
func (r *Reader) currentPlantValues(ctx context.Context, plantID int64, ranges map[int64]measurementRange) (CurrentValuesResult, error) {
	now := time.Now()

	var regionID int64
	err := r.db.QueryRow(ctx, `
		SELECT region_id FROM plant_region_history
		WHERE plant_id = $1 AND valid_from <= $2 AND (valid_to IS NULL OR valid_to > $2)
	`, plantID, now).Scan(&regionID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The plant has no currently-open placement interval -- resolves to
		// nothing, same as attribution.Resolver.ResolvePlants' "no
		// attributing region" case.
		return CurrentValuesResult{}, nil
	}
	if err != nil {
		return CurrentValuesResult{}, fmt.Errorf("readings: resolve current region for plant %d: %w", plantID, err)
	}

	siblings, attributedRegionID, err := r.attribution.ResolvePlants(ctx, regionID, now)
	if err != nil {
		return CurrentValuesResult{}, fmt.Errorf("readings: resolve attributed plants for plant %d: %w", plantID, err)
	}
	if len(siblings) == 0 {
		return CurrentValuesResult{}, nil
	}

	sensors, err := r.attribution.AttributedSensors(ctx, attributedRegionID, now)
	if err != nil {
		return CurrentValuesResult{}, fmt.Errorf("readings: resolve attributed sensors for plant %d: %w", plantID, err)
	}
	sensorIDs := make([]int64, len(sensors))
	for i, s := range sensors {
		sensorIDs[i] = s.SensorID
	}

	values, err := r.latestRawValues(ctx, sensorIDs, ranges)
	if err != nil {
		return CurrentValuesResult{}, err
	}

	plantValues := make([]CurrentPlantValue, len(siblings))
	for i, sib := range siblings {
		plantValues[i] = CurrentPlantValue{PlantID: sib.PlantID, Values: values}
	}

	// Every sibling plant carries an identical copy of values on the wire
	// (this function's own doc comment): marked_count/returned_count count
	// across every CurrentValue in every plant_values entry (api.proto's
	// GetCurrentValuesResponse.marked_count comment), so each sibling's
	// copy contributes its own tally, not just one shared count.
	perSibling := suspect.CountMarkers(currentValueMarkers(values))
	return CurrentValuesResult{
		PlantValues:   plantValues,
		MarkedCount:   perSibling.Marked * int64(len(siblings)),
		ReturnedCount: perSibling.Returned * int64(len(siblings)),
	}, nil
}

// PeriodSummary answers GetPeriodSummary for regionID over period,
// filtered by measurementTypeID (0 = unfiltered), from the hourly tier
// (FR28) -- always hourly, never coarsened further (it is the coarsest
// tier and has indefinite retention, migration 022) and never a finer
// tier (FR28: "exact at the hourly tier for min and max" is the property
// this RPC relies on; there is no requested-granularity hint on
// GetPeriodSummaryRequest to coarsen away from).
func (r *Reader) PeriodSummary(ctx context.Context, regionID int64, period Window, measurementTypeID int64) (PeriodSummaryResult, error) {
	if err := validateWindow(period); err != nil {
		return PeriodSummaryResult{}, err
	}

	ranges, err := r.measurementRanges(ctx)
	if err != nil {
		return PeriodSummaryResult{}, err
	}

	summaries, err := r.hourlySummaries(ctx, regionID, period, measurementTypeID, ranges)
	if err != nil {
		return PeriodSummaryResult{}, err
	}

	// Overnight-low/daytime-high framing (FR28) is a temperature framing by
	// convention -- when the caller didn't filter to one measurement type,
	// frame against 'temperature' specifically (the classic
	// greenhouse/household reading these two fields describe) rather than
	// mixing units across measurement types into one min/max. If the
	// household has no temperature sensor type at all (should not happen
	// post-migration-001 seed, but defensively handled), both fields are
	// left nil rather than guessing at a type.
	framingMeasurementTypeID := measurementTypeID
	if framingMeasurementTypeID == 0 {
		id, ok, err := r.temperatureSensorTypeID(ctx)
		if err != nil {
			return PeriodSummaryResult{}, err
		}
		if ok {
			framingMeasurementTypeID = id
		}
	}

	var overnight, daytime *SummaryStat
	if framingMeasurementTypeID != 0 {
		overnight, err = r.framedSummary(ctx, regionID, period, framingMeasurementTypeID, overnightStartHour, overnightEndHour, false, ranges)
		if err != nil {
			return PeriodSummaryResult{}, err
		}
		daytime, err = r.framedSummary(ctx, regionID, period, framingMeasurementTypeID, daytimeStartHour, daytimeEndHour, true, ranges)
		if err != nil {
			return PeriodSummaryResult{}, err
		}
	}

	var markers []suspect.Marker
	for _, s := range summaries {
		markers = append(markers, suspect.Marker{Checks: s.SuspectChecks})
	}
	if overnight != nil {
		markers = append(markers, suspect.Marker{Checks: overnight.SuspectChecks})
	}
	if daytime != nil {
		markers = append(markers, suspect.Marker{Checks: daytime.SuspectChecks})
	}
	counts := suspect.CountMarkers(markers)

	return PeriodSummaryResult{
		Summaries:     summaries,
		OvernightLow:  overnight,
		DaytimeHigh:   daytime,
		Timezone:      periodSummaryTimezone,
		Tier:          tiers.Selection{Tier: tiers.TierHourly, Coarsened: false},
		MarkedCount:   counts.Marked,
		ReturnedCount: counts.Returned,
	}, nil
}

// hourlySummaries computes, per measurement type present in region's
// hourly-tier data over period, the exact min/max (FR71: hourly composes
// min/max exactly from finer tiers), the weighted average (sum/count,
// never averaged), and the bucket instant (min_at/max_at) each extreme
// occurred at, via a ROW_NUMBER() ranking rather than a second aggregate
// pass per extreme.
func (r *Reader) hourlySummaries(ctx context.Context, regionID int64, period Window, measurementTypeID int64, ranges map[int64]measurementRange) ([]SummaryStat, error) {
	where := "h.region_id = $1 AND h.bucket >= $2 AND h.bucket < $3"
	args := []any{regionID, period.Start, period.End}
	if measurementTypeID != 0 {
		where += " AND s.sensor_type_id = $4"
		args = append(args, measurementTypeID)
	}

	query := fmt.Sprintf(`
		WITH agg AS (
			SELECT h.bucket, s.sensor_type_id, h.reading_count, h.value_sum, h.value_min, h.value_max
			FROM sensor_reading_1h h
			JOIN sensor s ON s.sensor_id = h.sensor_id
			WHERE %s
		),
		ranked AS (
			SELECT sensor_type_id, bucket, reading_count, value_sum, value_min, value_max,
			       ROW_NUMBER() OVER (PARTITION BY sensor_type_id ORDER BY value_min ASC, bucket ASC) AS min_rank,
			       ROW_NUMBER() OVER (PARTITION BY sensor_type_id ORDER BY value_max DESC, bucket ASC) AS max_rank
			FROM agg
		)
		SELECT sensor_type_id,
		       coalesce(sum(reading_count), 0)::bigint,
		       coalesce(sum(value_sum), 0),
		       min(value_min),
		       max(value_max),
		       max(CASE WHEN min_rank = 1 THEN bucket END),
		       max(CASE WHEN max_rank = 1 THEN bucket END)
		FROM ranked
		GROUP BY sensor_type_id
		ORDER BY sensor_type_id
	`, where)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("readings: query period summary for region %d: %w", regionID, err)
	}
	defer rows.Close()

	var summaries []SummaryStat
	for rows.Next() {
		var mt int64
		var count int64
		var sum float64
		var min, max sql.NullFloat64
		var minAt, maxAt sql.NullTime
		if err := rows.Scan(&mt, &count, &sum, &min, &max, &minAt, &maxAt); err != nil {
			return nil, fmt.Errorf("readings: scan period summary row for region %d: %w", regionID, err)
		}
		var avg float64
		if count > 0 {
			avg = sum / float64(count)
		}
		s := SummaryStat{MeasurementTypeID: mt, Avg: avg}
		if min.Valid {
			s.Min = min.Float64
		}
		if max.Valid {
			s.Max = max.Float64
		}
		if minAt.Valid {
			s.MinAt = minAt.Time
		}
		if maxAt.Valid {
			s.MaxAt = maxAt.Time
		}
		summaries = append(summaries, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate period summary for region %d: %w", regionID, err)
	}

	for i := range summaries {
		marker, err := r.regionTypeSuspectMarker(ctx, regionID, period, summaries[i].MeasurementTypeID, summaries[i].Min, summaries[i].Max, ranges)
		if err != nil {
			return nil, err
		}
		summaries[i].SuspectChecks = marker.Checks
	}
	return summaries, nil
}

// temperatureSensorTypeID looks up sensor_type's 'temperature' row (seeded
// by migration 001) -- see PeriodSummary's doc comment on why an unfiltered
// overnight/daytime framing defaults to this measurement type.
func (r *Reader) temperatureSensorTypeID(ctx context.Context) (int64, bool, error) {
	var id int64
	err := r.db.QueryRow(ctx, `SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("readings: look up temperature sensor_type: %w", err)
	}
	return id, true, nil
}

// framedSummary finds the single extreme (min if wantMax is false, max if
// true) among regionID's hourly-tier buckets for measurementTypeID within
// period, restricted to hours in [startHour, endHour) of
// periodSummaryTimezone -- startHour > endHour wraps past midnight (the
// overnight framing). Returns (nil, nil) when no bucket falls in that
// framing window (e.g. a period shorter than one full night/day), never an
// error -- that is a legitimately empty framing, not a failure.
func (r *Reader) framedSummary(ctx context.Context, regionID int64, period Window, measurementTypeID int64, startHour, endHour int, wantMax bool, ranges map[int64]measurementRange) (*SummaryStat, error) {
	var hourClause string
	if startHour < endHour {
		hourClause = fmt.Sprintf(
			"EXTRACT(HOUR FROM h.bucket AT TIME ZONE '%s') >= %d AND EXTRACT(HOUR FROM h.bucket AT TIME ZONE '%s') < %d",
			periodSummaryTimezone, startHour, periodSummaryTimezone, endHour)
	} else {
		hourClause = fmt.Sprintf(
			"(EXTRACT(HOUR FROM h.bucket AT TIME ZONE '%s') >= %d OR EXTRACT(HOUR FROM h.bucket AT TIME ZONE '%s') < %d)",
			periodSummaryTimezone, startHour, periodSummaryTimezone, endHour)
	}

	orderCol := "h.value_min ASC"
	if wantMax {
		orderCol = "h.value_max DESC"
	}
	query := fmt.Sprintf(`
		SELECT h.bucket, h.sensor_id, h.value_min, h.value_max
		FROM sensor_reading_1h h
		JOIN sensor s ON s.sensor_id = h.sensor_id
		WHERE h.region_id = $1 AND h.bucket >= $2 AND h.bucket < $3 AND s.sensor_type_id = $4
		  AND %s
		ORDER BY %s, h.bucket ASC
		LIMIT 1
	`, hourClause, orderCol)

	var bucket time.Time
	var sensorID int64
	var min, max float64
	err := r.db.QueryRow(ctx, query, regionID, period.Start, period.End, measurementTypeID).Scan(&bucket, &sensorID, &min, &max)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("readings: query framed summary for region %d: %w", regionID, err)
	}

	value := min
	if wantMax {
		value = max
	}
	marker, err := r.bucketSuspectMarker(ctx, regionID, sensorID, measurementTypeID, bucket, value, ranges)
	if err != nil {
		return nil, err
	}

	if wantMax {
		return &SummaryStat{MeasurementTypeID: measurementTypeID, Max: max, MaxAt: bucket, SuspectChecks: marker.Checks}, nil
	}
	return &SummaryStat{MeasurementTypeID: measurementTypeID, Min: min, MinAt: bucket, SuspectChecks: marker.Checks}, nil
}

// Compare answers CompareSeries for entities (2+) aligned on one shared
// window and one measurement (FR25.3). Every entity is queried with the
// same tier, window and cursor; the shared next_page_token is the maximum
// (most recent) "last returned point" among entities that still have more
// -- guaranteeing no entity's data is skipped across a page boundary, at
// the cost of a few points possibly repeating for an entity whose own data
// is sparser than the one driving the shared cursor. This is a deliberate,
// documented trade-off (duplicate rather than lose data); CompareSeries's
// own testing criterion is alignment on one window/measurement, not exact
// no-overlap pagination across ragged series.
func (r *Reader) Compare(ctx context.Context, entities []authz.EntityRef, window Window, measurementTypeID int64, requested tiers.Tier, page Page) (CompareResult, error) {
	if len(entities) < 2 {
		return CompareResult{}, ErrTooFewEntities
	}
	if measurementTypeID == 0 {
		return CompareResult{}, ErrMeasurementRequired
	}
	if err := validateWindow(window); err != nil {
		return CompareResult{}, err
	}
	selection, err := tiers.Select(requested, window.Start, window.End)
	if err != nil {
		return CompareResult{}, err
	}

	ranges, err := r.measurementRanges(ctx)
	if err != nil {
		return CompareResult{}, err
	}

	limit := contract.ClampPageSize(page.Size)
	afterTime, afterID, hasAfter, err := contract.DecodeReadingCursor(page.Token)
	if err != nil {
		return CompareResult{}, fmt.Errorf("readings: decode page token: %w", err)
	}
	cursor := seriesCursor{hasAfter: hasAfter, afterTime: afterTime, afterID: afterID}

	series := make([]EntitySeries, len(entities))
	var cutoff time.Time
	var hasMoreAny bool
	var allMarkers []suspect.Marker
	for i, e := range entities {
		pts, err := r.pointsForEntity(ctx, e, selection.Tier, window, measurementTypeID, limit+1, cursor, ranges, false)
		if err != nil {
			return CompareResult{}, err
		}
		if int32(len(pts)) > limit {
			hasMoreAny = true
			last := pts[limit-1]
			if cutoff.IsZero() || last.RecordedAt.After(cutoff) {
				cutoff = last.RecordedAt
			}
			pts = pts[:limit]
		}
		series[i] = EntitySeries{Entity: e, Points: pts}
		allMarkers = append(allMarkers, pointMarkers(pts)...)
	}

	var nextToken string
	if hasMoreAny {
		nextToken = contract.EncodeReadingCursor(cutoff, 0)
	}

	counts := suspect.CountMarkers(allMarkers)
	return CompareResult{
		Series:        series,
		Tier:          selection,
		NextPageToken: nextToken,
		MarkedCount:   counts.Marked,
		ReturnedCount: counts.Returned,
	}, nil
}
