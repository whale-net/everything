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
// Scaffold only (this task's Scaffold phase, #1362): every method returns
// ErrNotImplemented until this task's Implementation phase fills in the
// tier-backed series query, the raw-only current-value query, and the
// hourly-tier summary/framing query.
package readings

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/attribution"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/tiers"
)

// ErrNotImplemented is returned by every Reader method until this task's
// Implementation phase fills in the read path (FR25, FR27, FR28).
var ErrNotImplemented = errors.New("readings: not implemented (Implementation phase, FR25/FR27/FR28)")

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

// Series answers GetReadingSeries for entity over window, filtered by
// measurementTypeID (0 = unfiltered) and served from the tiers
// (leaflab/api/tiers), never through v_sensor_reading_with_plant -- see
// this task's Implementation section. requested is a hint (FR71); the
// returned Selection always states which tier actually answered.
func (r *Reader) Series(ctx context.Context, entity authz.EntityRef, window Window, measurementTypeID int64, requested tiers.Tier, page Page) (SeriesResult, error) {
	return SeriesResult{}, ErrNotImplemented
}

// CurrentValues answers GetCurrentValues for entity, always from the
// latest raw readings (FR27) -- for a plant ref, via attribution's
// nearest-ancestor walk (FR23) over the sensors beneath the attributing
// region.
func (r *Reader) CurrentValues(ctx context.Context, entity authz.EntityRef) (CurrentValuesResult, error) {
	return CurrentValuesResult{}, ErrNotImplemented
}

// PeriodSummary answers GetPeriodSummary for regionID over period,
// filtered by measurementTypeID (0 = unfiltered), from the hourly tier
// where the period allows (FR28).
func (r *Reader) PeriodSummary(ctx context.Context, regionID int64, period Window, measurementTypeID int64) (PeriodSummaryResult, error) {
	return PeriodSummaryResult{}, ErrNotImplemented
}

// Compare answers CompareSeries for entities (2+) aligned on one shared
// window and one measurement (FR25.3).
func (r *Reader) Compare(ctx context.Context, entities []authz.EntityRef, window Window, measurementTypeID int64, requested tiers.Tier, page Page) (CompareResult, error) {
	return CompareResult{}, ErrNotImplemented
}
