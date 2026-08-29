//go:build integration

// Real-Postgres integration coverage for this task's Testing section
// (#1362, FR25/FR27/FR28/NFR3.2): Reader.Series across every entity kind
// with FR71's tier disclosure and FR25.2's measurement-type filter,
// Reader.Compare's shared-window/shared-measurement alignment (FR25.3),
// Reader.CurrentValues serving from raw ahead of any tier refresh (FR27),
// Reader.PeriodSummary's hourly-exact min/max and server-side
// overnight-low/daytime-high framing (FR28), and NFR3.2's bounding
// (unbounded rejected, a >48h raw request coarsens and discloses it, the
// page-size cap is enforced).
//
// Schema is self-contained hand-written DDL -- a hermetic trim of
// migrations 001 (region/board/sensor_type/sensor/sensor_reading/plant),
// 012 (v_region_path only), 017 (plant_region_history), 019
// (attribute_region_plants) and 033 (boundary_capture/boundary_partial),
// same rationale as this package's sibling integration tests elsewhere in
// leaflab/api (see dbtest_helpers_integration_test.go's doc comment).
// sensor_reading_5m/sensor_reading_1h are plain tables here, populated
// directly, not real TimescaleDB continuous aggregates -- Reader's own SQL
// treats them as plain relations (see readings.go's tierTable), the same
// convention leaflab/api/capture's own integration tests use for the
// five-minute tier. No TimescaleDB extension or hypertable is needed since
// Reader never calls time_bucket() itself.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api/readings:reader_integration_test --test_output=all
package readings

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/api/suspect"
	"github.com/whale-net/everything/leaflab/api/tiers"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const testSchema = `
	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id),
		name             VARCHAR(255) NOT NULL
	);

	CREATE VIEW v_region_path AS
	WITH RECURSIVE path AS (
		SELECT r.region_id, r.parent_region_id, ARRAY[r.region_id]::BIGINT[] AS path_ids
		FROM region r
		WHERE r.parent_region_id IS NULL

		UNION ALL

		SELECT r.region_id, r.parent_region_id, path.path_ids || r.region_id
		FROM region r
		JOIN path ON r.parent_region_id = path.region_id
	)
	SELECT region_id, path_ids FROM path;

	CREATE TABLE board (
		board_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE
	);
	INSERT INTO sensor_type (name) VALUES ('temperature'), ('humidity');

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		region_id      BIGINT REFERENCES region(region_id)
	);
	CREATE INDEX idx_sensor_board_id  ON sensor(board_id);
	CREATE INDEX idx_sensor_region_id ON sensor(region_id);

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id   BIGINT REFERENCES region(region_id),
		value       DOUBLE PRECISION NOT NULL,
		valid       BOOLEAN NOT NULL DEFAULT TRUE,
		recorded_at TIMESTAMPTZ NOT NULL
	);
	CREATE INDEX idx_sensor_reading_sensor_id ON sensor_reading(sensor_id, recorded_at DESC);
	CREATE INDEX idx_sensor_reading_region_id ON sensor_reading(region_id, recorded_at DESC);
	CREATE INDEX idx_sensor_reading_invalid   ON sensor_reading(sensor_id, recorded_at DESC) WHERE valid = FALSE;

	-- FR26.2's stale-attribution comparand: sensor_region_history's own
	-- (sensor_id) placement intervals, independent of sensor_reading's own
	-- denormalized region_id column. Empty in this hermetic fixture unless
	-- a test explicitly seeds it -- Reader's suspect-detection LEFT JOIN
	-- LATERAL against this table tolerates no matching row (an unassigned
	-- sensor, or a sensor placed before any tracked history existed) by
	-- simply not evaluating CheckStaleAttribution for that reading.
	CREATE TABLE sensor_region_history (
		history_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id   BIGINT NOT NULL REFERENCES region(region_id),
		valid_from  TIMESTAMPTZ NOT NULL,
		valid_to    TIMESTAMPTZ
	);
	CREATE INDEX idx_sensor_region_history_sensor_id ON sensor_region_history(sensor_id, valid_from, valid_to);

	CREATE TABLE sensor_reading_5m (
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id     BIGINT,
		bucket        TIMESTAMPTZ NOT NULL,
		reading_count BIGINT NOT NULL,
		value_sum     DOUBLE PRECISION NOT NULL,
		value_min     DOUBLE PRECISION NOT NULL,
		value_max     DOUBLE PRECISION NOT NULL,
		PRIMARY KEY (sensor_id, bucket)
	);

	CREATE TABLE sensor_reading_1h (
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id),
		region_id     BIGINT,
		bucket        TIMESTAMPTZ NOT NULL,
		reading_count BIGINT NOT NULL,
		value_sum     DOUBLE PRECISION NOT NULL,
		value_min     DOUBLE PRECISION NOT NULL,
		value_max     DOUBLE PRECISION NOT NULL,
		PRIMARY KEY (sensor_id, bucket)
	);

	CREATE TABLE boundary_capture (
		capture_id    BIGSERIAL PRIMARY KEY,
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		boundary_at   TIMESTAMPTZ NOT NULL,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		state         TEXT NOT NULL DEFAULT 'pending',
		completed_at  TIMESTAMPTZ,
		CONSTRAINT boundary_capture_tier_check
			CHECK (tier IN ('five_minute', 'hourly')),
		CONSTRAINT boundary_capture_state_check
			CHECK (state IN ('pending', 'completed'))
	);

	CREATE TABLE boundary_partial (
		partial_id    BIGSERIAL PRIMARY KEY,
		capture_id    BIGINT NOT NULL REFERENCES boundary_capture(capture_id) ON DELETE RESTRICT,
		tier          TEXT NOT NULL,
		bucket_start  TIMESTAMPTZ NOT NULL,
		partial_from  TIMESTAMPTZ NOT NULL,
		partial_to    TIMESTAMPTZ NOT NULL,
		reading_count BIGINT NOT NULL,
		value_sum     DOUBLE PRECISION NOT NULL,
		value_min     DOUBLE PRECISION NOT NULL,
		value_max     DOUBLE PRECISION NOT NULL,
		CONSTRAINT boundary_partial_interval_check
			CHECK (partial_from < partial_to)
	);

	CREATE TABLE plant_type (
		plant_type_id BIGSERIAL PRIMARY KEY,
		common_name   VARCHAR(128) NOT NULL
	);
	INSERT INTO plant_type (common_name) VALUES ('Fiddle Leaf Fig');

	CREATE TABLE plant (
		plant_id      BIGSERIAL PRIMARY KEY,
		region_id     BIGINT NOT NULL REFERENCES region(region_id),
		plant_type_id BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
		name          VARCHAR(128) NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		removed_at    TIMESTAMPTZ
	);

	CREATE TABLE plant_region_history (
		plant_region_history_id BIGSERIAL PRIMARY KEY,
		plant_id           BIGINT NOT NULL REFERENCES plant(plant_id),
		region_id          BIGINT NOT NULL REFERENCES region(region_id),
		valid_from         TIMESTAMPTZ NOT NULL,
		valid_to           TIMESTAMPTZ,
		relocation_induced BOOLEAN NOT NULL DEFAULT FALSE
	);
	CREATE INDEX idx_plant_region_history_plant_id_current
		ON plant_region_history(plant_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_plant_region_history_region_id_current
		ON plant_region_history(region_id) WHERE valid_to IS NULL;

	-- Go twin: leaflab/api/attribution.Resolver.AttributedSensors/ResolvePlants
	-- (migration 019's SQL twin, verbatim body).
	CREATE FUNCTION attribute_region_plants(p_region_id BIGINT, p_at TIMESTAMPTZ)
	RETURNS TABLE (attributed_region_id BIGINT, plant_id BIGINT, plant_name TEXT)
	AS $$
	DECLARE
		v_path_ids BIGINT[];
		v_attributed_region_id BIGINT;
		v_candidate BIGINT;
		i INT;
	BEGIN
		SELECT path_ids INTO v_path_ids FROM v_region_path WHERE region_id = p_region_id;

		IF v_path_ids IS NULL THEN
			RETURN;
		END IF;

		FOR i IN REVERSE array_length(v_path_ids, 1)..1 LOOP
			v_candidate := v_path_ids[i];
			IF EXISTS (
				SELECT 1 FROM plant_region_history prh
				WHERE prh.region_id = v_candidate
				  AND prh.valid_from <= p_at
				  AND (prh.valid_to IS NULL OR prh.valid_to > p_at)
			) THEN
				v_attributed_region_id := v_candidate;
				EXIT;
			END IF;
		END LOOP;

		IF v_attributed_region_id IS NULL THEN
			RETURN;
		END IF;

		RETURN QUERY
		SELECT v_attributed_region_id, p.plant_id, p.name::TEXT
		FROM plant_region_history prh
		JOIN plant p ON p.plant_id = prh.plant_id
		WHERE prh.region_id = v_attributed_region_id
		  AND prh.valid_from <= p_at
		  AND (prh.valid_to IS NULL OR prh.valid_to > p_at)
		ORDER BY p.plant_id;
	END;
	$$ LANGUAGE plpgsql;
`

// fixture bundles a ready Reader with the raw pool for direct SQL setup
// and assertions.
type fixture struct {
	reader *Reader
	pool   *pgxpool.Pool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: testSchema})
	return &fixture{reader: NewReader(db.Pool), pool: db.Pool}
}

func (f *fixture) insertRegion(t *testing.T, name string, parent int64) int64 {
	t.Helper()
	var id int64
	var err error
	if parent == 0 {
		err = f.pool.QueryRow(context.Background(),
			`INSERT INTO region (name) VALUES ($1) RETURNING region_id`, name).Scan(&id)
	} else {
		err = f.pool.QueryRow(context.Background(),
			`INSERT INTO region (name, parent_region_id) VALUES ($1, $2) RETURNING region_id`, name, parent).Scan(&id)
	}
	if err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

func (f *fixture) insertBoard(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO board DEFAULT VALUES RETURNING board_id`).Scan(&id); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	return id
}

// temperatureTypeID / humidityTypeID are seeded by testSchema's INSERT INTO
// sensor_type above (ids 1 and 2 respectively, since sensor_type_id is a
// fresh BIGSERIAL in every test's own isolated database).
const (
	temperatureTypeID int64 = 1
	humidityTypeID    int64 = 2
)

func (f *fixture) insertSensor(t *testing.T, boardID, regionID, sensorTypeID int64) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO sensor (board_id, region_id, sensor_type_id) VALUES ($1, $2, $3) RETURNING sensor_id
	`, boardID, regionID, sensorTypeID).Scan(&id); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	return id
}

func (f *fixture) insertReading(t *testing.T, sensorID, regionID int64, value float64, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, region_id, value, recorded_at) VALUES ($1, $2, $3, $4)
	`, sensorID, regionID, value, at); err != nil {
		t.Fatalf("insert reading: %v", err)
	}
}

func (f *fixture) insertHourlyBucket(t *testing.T, sensorID, regionID int64, bucket time.Time, count int64, sum, min, max float64) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO sensor_reading_1h (sensor_id, region_id, bucket, reading_count, value_sum, value_min, value_max)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, sensorID, regionID, bucket, count, sum, min, max); err != nil {
		t.Fatalf("insert hourly bucket: %v", err)
	}
}

func (f *fixture) insertPlant(t *testing.T, regionID int64) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO plant (region_id, plant_type_id, name) VALUES ($1, 1, 'test-plant') RETURNING plant_id
	`, regionID).Scan(&id); err != nil {
		t.Fatalf("insert plant: %v", err)
	}
	return id
}

func (f *fixture) insertPlantRegionHistory(t *testing.T, plantID, regionID int64, from time.Time, to *time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from, valid_to) VALUES ($1, $2, $3, $4)
	`, plantID, regionID, from, to); err != nil {
		t.Fatalf("insert plant_region_history: %v", err)
	}
}

// insertReadingValid is insertReading's twin with an explicit valid flag,
// for FR26.1's CheckPersistedInvalidFlag coverage -- insertReading itself
// always writes valid=TRUE (the schema's own default) since none of this
// task's pre-existing tests needed an invalid row.
func (f *fixture) insertReadingValid(t *testing.T, sensorID, regionID int64, value float64, at time.Time, valid bool) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, region_id, value, valid, recorded_at) VALUES ($1, $2, $3, $4, $5)
	`, sensorID, regionID, value, valid, at); err != nil {
		t.Fatalf("insert reading (valid=%v): %v", valid, err)
	}
}

// readingRegionID reads back sensor_reading.region_id directly -- used by
// the stale-attribution test to prove FR26.2's "marked, never rewritten":
// the stored value must be identical to what was written, never corrected
// to sensor_region_history's disagreeing value.
func (f *fixture) readingRegionID(t *testing.T, sensorID int64, at time.Time) int64 {
	t.Helper()
	var regionID int64
	if err := f.pool.QueryRow(context.Background(), `
		SELECT region_id FROM sensor_reading WHERE sensor_id = $1 AND recorded_at = $2
	`, sensorID, at).Scan(&regionID); err != nil {
		t.Fatalf("query stored region_id for sensor %d at %s: %v", sensorID, at, err)
	}
	return regionID
}

// insertFiveMinuteBucket is insertHourlyBucket's five-minute-tier twin.
func (f *fixture) insertFiveMinuteBucket(t *testing.T, sensorID, regionID int64, bucket time.Time, count int64, sum, min, max float64) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO sensor_reading_5m (sensor_id, region_id, bucket, reading_count, value_sum, value_min, value_max)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, sensorID, regionID, bucket, count, sum, min, max); err != nil {
		t.Fatalf("insert five-minute bucket: %v", err)
	}
}

// insertSensorRegionHistory seeds sensor_region_history -- FR26.2's stale-
// attribution comparand, independent of sensor_reading's own denormalized
// region_id column (this file's testSchema comment on the table).
func (f *fixture) insertSensorRegionHistory(t *testing.T, sensorID, regionID int64, from time.Time, to *time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO sensor_region_history (sensor_id, region_id, valid_from, valid_to) VALUES ($1, $2, $3, $4)
	`, sensorID, regionID, from, to); err != nil {
		t.Fatalf("insert sensor_region_history: %v", err)
	}
}

// ── FR25.1: series over sensor / board / region / plant, with FR71's tier disclosure ──

// TestSeries_EveryEntityKind_ReturnsPointsAndDisclosesTier proves FR25.1's
// "readings are readable for a sensor, board, region or plant over an
// explicitly bounded time range" and FR71's "responses state which
// granularity tier answered them" for every entity kind Series accepts.
func TestSeries_EveryEntityKind_ReturnsPointsAndDisclosesTier(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)
	plantID := f.insertPlant(t, regionID)
	f.insertPlantRegionHistory(t, plantID, regionID, now.Add(-2*time.Hour), nil)

	f.insertReading(t, sensorID, regionID, 21.5, now.Add(-30*time.Minute))
	f.insertReading(t, sensorID, regionID, 22.0, now.Add(-10*time.Minute))

	window := Window{Start: now.Add(-time.Hour), End: now}

	cases := []struct {
		name   string
		entity authz.EntityRef
	}{
		{"sensor", authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}},
		{"board", authz.EntityRef{Kind: authz.EntityBoard, ID: boardID}},
		{"region", authz.EntityRef{Kind: authz.EntityRegion, ID: regionID}},
		{"plant", authz.EntityRef{Kind: authz.EntityPlant, ID: plantID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := f.reader.Series(context.Background(), tc.entity, window, 0, tiers.TierRaw, Page{}, false)
			if err != nil {
				t.Fatalf("Series(%s): %v", tc.name, err)
			}
			if len(result.Points) != 2 {
				t.Errorf("Series(%s): len(Points) = %d, want 2", tc.name, len(result.Points))
			}
			if result.Tier.Tier != tiers.TierRaw {
				t.Errorf("Series(%s): Tier = %q, want %q (response must always name the tier that answered, FR71)", tc.name, result.Tier.Tier, tiers.TierRaw)
			}
		})
	}
}

// ── FR25.2: measurement-type filter narrows the series ──────────────────

// TestSeries_MeasurementTypeFilter_Narrows proves FR25.2: a sensor/board
// entity ref carrying two sensors of different measurement types returns
// only the filtered type's readings when measurementTypeID is set, and
// both when it is 0 (unfiltered).
func TestSeries_MeasurementTypeFilter_Narrows(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	tempSensor := f.insertSensor(t, boardID, regionID, temperatureTypeID)
	humiditySensor := f.insertSensor(t, boardID, regionID, humidityTypeID)

	f.insertReading(t, tempSensor, regionID, 21.5, now.Add(-10*time.Minute))
	f.insertReading(t, humiditySensor, regionID, 55.0, now.Add(-10*time.Minute))

	window := Window{Start: now.Add(-time.Hour), End: now}
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: boardID}

	unfiltered, err := f.reader.Series(context.Background(), boardRef, window, 0, tiers.TierRaw, Page{}, false)
	if err != nil {
		t.Fatalf("Series (unfiltered): %v", err)
	}
	if len(unfiltered.Points) != 2 {
		t.Fatalf("unfiltered Series: len(Points) = %d, want 2", len(unfiltered.Points))
	}

	filtered, err := f.reader.Series(context.Background(), boardRef, window, temperatureTypeID, tiers.TierRaw, Page{}, false)
	if err != nil {
		t.Fatalf("Series (filtered to temperature): %v", err)
	}
	if len(filtered.Points) != 1 {
		t.Fatalf("filtered Series: len(Points) = %d, want 1", len(filtered.Points))
	}
	if filtered.Points[0].Value != 21.5 {
		t.Errorf("filtered Series value = %v, want the temperature reading (21.5), not the humidity one", filtered.Points[0].Value)
	}
}

// ── FR25.3: CompareSeries aligns 2+ entities on one window/measurement ──

// TestCompare_TwoSensors_AlignedOnSharedWindowAndMeasurement proves FR25.3:
// two entities compared over one shared window and one measurement each
// get their own aligned series back.
func TestCompare_TwoSensors_AlignedOnSharedWindowAndMeasurement(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorA := f.insertSensor(t, boardID, regionID, temperatureTypeID)
	sensorB := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	f.insertReading(t, sensorA, regionID, 20.0, now.Add(-20*time.Minute))
	f.insertReading(t, sensorB, regionID, 30.0, now.Add(-20*time.Minute))

	window := Window{Start: now.Add(-time.Hour), End: now}
	entities := []authz.EntityRef{
		{Kind: authz.EntitySensor, ID: sensorA},
		{Kind: authz.EntitySensor, ID: sensorB},
	}

	result, err := f.reader.Compare(context.Background(), entities, window, temperatureTypeID, tiers.TierRaw, Page{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(result.Series) != 2 {
		t.Fatalf("len(Series) = %d, want 2", len(result.Series))
	}
	for i, es := range result.Series {
		if len(es.Points) != 1 {
			t.Errorf("Series[%d] (entity %+v): len(Points) = %d, want 1", i, es.Entity, len(es.Points))
		}
	}
	if result.Series[0].Points[0].Value == result.Series[1].Points[0].Value {
		t.Fatal("test setup: sensor A and B values must differ to prove each series is its own entity's data, not a shared/merged one")
	}
}

// TestCompare_FewerThanTwoEntities_Rejected proves FR25.3's "two or more".
func TestCompare_FewerThanTwoEntities_Rejected(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	window := Window{Start: now.Add(-time.Hour), End: now}
	_, err := f.reader.Compare(context.Background(), []authz.EntityRef{{Kind: authz.EntitySensor, ID: 1}}, window, temperatureTypeID, tiers.TierRaw, Page{})
	if err != ErrTooFewEntities {
		t.Fatalf("Compare with 1 entity: err = %v, want ErrTooFewEntities", err)
	}
}

// ── FR27: current value from raw, before any tier refresh ──────────────

// TestCurrentValues_FromRaw_BeforeAnyTierRefresh proves FR27's core
// promise: a reading just written appears in CurrentValues immediately,
// even though sensor_reading_5m/sensor_reading_1h carry no rows for it at
// all yet -- current values are served from raw, never from a
// pre-aggregated tier that might lag behind a refresh.
func TestCurrentValues_FromRaw_BeforeAnyTierRefresh(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	f.insertReading(t, sensorID, regionID, 19.0, now.Add(-time.Hour))
	f.insertReading(t, sensorID, regionID, 23.5, now) // the latest -- this is the one CurrentValues must return

	// No rows are ever written to sensor_reading_5m/sensor_reading_1h in
	// this test -- if CurrentValues fell back to (or joined against) a
	// tier table, it would find nothing there and this test would fail on
	// an empty result, not merely a stale one.
	result, err := f.reader.CurrentValues(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID})
	if err != nil {
		t.Fatalf("CurrentValues: %v", err)
	}
	if len(result.Values) != 1 {
		t.Fatalf("len(Values) = %d, want 1", len(result.Values))
	}
	if result.Values[0].Value != 23.5 {
		t.Errorf("CurrentValues = %v, want the latest raw reading 23.5", result.Values[0].Value)
	}
	if !result.Values[0].RecordedAt.Equal(now) {
		t.Errorf("CurrentValues RecordedAt = %s, want %s", result.Values[0].RecordedAt, now)
	}
}

// ── FR28: period summary, hourly-exact min/max and server-side framing ──

// TestPeriodSummary_HourlyMinMax_EqualsRawMinMax proves FR28/FR71's
// "exact at the hourly tier for min and max": the hourly-tier summary this
// test computes from directly-populated sensor_reading_1h rows must equal
// a ground truth computed straight from the very same raw readings those
// buckets were derived from -- the test's own independent oracle, not
// Reader's own SQL, so a bug shared between production code and the
// test's expectation cannot cancel out.
func TestPeriodSummary_HourlyMinMax_EqualsRawMinMax(t *testing.T) {
	f := newFixture(t)
	periodStart := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	// Raw ground truth for hour [10:00, 11:00).
	hourBucket := periodStart.Add(10 * time.Hour)
	rawValues := []float64{18.0, 25.5, 12.0, 19.0}
	for i, v := range rawValues {
		f.insertReading(t, sensorID, regionID, v, hourBucket.Add(time.Duration(i)*10*time.Minute))
	}
	var wantSum float64
	wantMin, wantMax := rawValues[0], rawValues[0]
	for _, v := range rawValues {
		wantSum += v
		if v < wantMin {
			wantMin = v
		}
		if v > wantMax {
			wantMax = v
		}
	}
	f.insertHourlyBucket(t, sensorID, regionID, hourBucket, int64(len(rawValues)), wantSum, wantMin, wantMax)

	period := Window{Start: periodStart, End: periodStart.Add(24 * time.Hour)}
	result, err := f.reader.PeriodSummary(context.Background(), regionID, period, temperatureTypeID)
	if err != nil {
		t.Fatalf("PeriodSummary: %v", err)
	}
	if len(result.Summaries) != 1 {
		t.Fatalf("len(Summaries) = %d, want 1", len(result.Summaries))
	}
	got := result.Summaries[0]
	if got.Min != wantMin {
		t.Errorf("Min = %v, want raw-computed %v", got.Min, wantMin)
	}
	if got.Max != wantMax {
		t.Errorf("Max = %v, want raw-computed %v", got.Max, wantMax)
	}
	if result.Tier.Tier != tiers.TierHourly {
		t.Errorf("Tier = %q, want %q (FR28 is always answered from the hourly tier)", result.Tier.Tier, tiers.TierHourly)
	}
}

// TestPeriodSummary_OvernightDaytimeFraming_ComputedServerSide proves
// FR28's overnight-low/daytime-high framing is a server-side windowing of
// the same summary data (fixed UTC hour boundaries, per readings.go's
// periodSummaryTimezone/overnightStartHour/daytimeStartHour constants),
// not a client-side convention: an overnight-hour bucket's low is reported
// distinctly from a daytime-hour bucket's high, even when the daytime
// bucket's own value is numerically lower than the overnight one -- if
// this were computed by simply taking the period's global min/max, the two
// fields would collapse to the same instant regardless of which hours they
// fell in.
func TestPeriodSummary_OvernightDaytimeFraming_ComputedServerSide(t *testing.T) {
	f := newFixture(t)
	periodStart := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	// Overnight bucket (23:00, inside [22:00, 06:00)): the coldest reading
	// of the whole period.
	overnightBucket := periodStart.Add(23 * time.Hour)
	f.insertHourlyBucket(t, sensorID, regionID, overnightBucket, 1, 5.0, 5.0, 5.0)

	// Daytime bucket (14:00, inside [06:00, 22:00)): the hottest reading of
	// the whole period.
	daytimeBucket := periodStart.Add(14 * time.Hour)
	f.insertHourlyBucket(t, sensorID, regionID, daytimeBucket, 1, 30.0, 30.0, 30.0)

	// A second daytime bucket with a lower value than the overnight one, to
	// prove daytime-high picks the daytime *maximum* (30.0), not "whatever
	// the smallest daytime value is" and not the period's global minimum.
	otherDaytimeBucket := periodStart.Add(8 * time.Hour)
	f.insertHourlyBucket(t, sensorID, regionID, otherDaytimeBucket, 1, 10.0, 10.0, 10.0)

	period := Window{Start: periodStart, End: periodStart.Add(24 * time.Hour)}
	result, err := f.reader.PeriodSummary(context.Background(), regionID, period, temperatureTypeID)
	if err != nil {
		t.Fatalf("PeriodSummary: %v", err)
	}
	if result.OvernightLow == nil {
		t.Fatal("OvernightLow is nil, want a populated framing")
	}
	if result.OvernightLow.Min != 5.0 {
		t.Errorf("OvernightLow.Min = %v, want 5.0 (the overnight-hour bucket)", result.OvernightLow.Min)
	}
	if !result.OvernightLow.MinAt.Equal(overnightBucket) {
		t.Errorf("OvernightLow.MinAt = %s, want %s", result.OvernightLow.MinAt, overnightBucket)
	}
	if result.DaytimeHigh == nil {
		t.Fatal("DaytimeHigh is nil, want a populated framing")
	}
	if result.DaytimeHigh.Max != 30.0 {
		t.Errorf("DaytimeHigh.Max = %v, want 30.0 (the daytime-hour bucket with the highest value, not the lowest daytime bucket or the period's global min)", result.DaytimeHigh.Max)
	}
	if !result.DaytimeHigh.MaxAt.Equal(daytimeBucket) {
		t.Errorf("DaytimeHigh.MaxAt = %s, want %s", result.DaytimeHigh.MaxAt, daytimeBucket)
	}
	if result.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want %q (readings.go states which day boundary it used)", result.Timezone, "UTC")
	}
}

// ── NFR3.2: request bounding ─────────────────────────────────────────────

// TestSeries_UnboundedWindow_Rejected proves NFR3.2's "no caller can
// request an unbounded scan": a zero Start or End is rejected outright,
// never served as a scan.
func TestSeries_UnboundedWindow_Rejected(t *testing.T) {
	f := newFixture(t)
	entity := authz.EntityRef{Kind: authz.EntitySensor, ID: 1}

	cases := []struct {
		name   string
		window Window
	}{
		{"zero start", Window{End: time.Now()}},
		{"zero end", Window{Start: time.Now()}},
		{"both zero", Window{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.reader.Series(context.Background(), entity, tc.window, 0, tiers.TierRaw, Page{}, false)
			if err != ErrUnboundedWindow {
				t.Fatalf("Series(%s): err = %v, want ErrUnboundedWindow", tc.name, err)
			}
		})
	}
}

// TestSeries_RawRequestBeyond48Hours_CoarsensAndDiscloses proves NFR3.2's
// "raw-row responses are capped at a 48-hour window; longer windows
// resolve automatically to a coarser tier, and the response says which"
// (FR71): a raw request reaching back 7 days must not be rejected -- it
// must be served, coarsened, with Coarsened=true and a Tier other than raw.
func TestSeries_RawRequestBeyond48Hours_CoarsensAndDiscloses(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	// A five-minute-tier bucket inside the 7-day window, so the coarsened
	// query actually has something to return -- proving the request is
	// served, not merely not-rejected.
	bucket := now.Add(-3 * 24 * time.Hour)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO sensor_reading_5m (sensor_id, region_id, bucket, reading_count, value_sum, value_min, value_max)
		VALUES ($1, $2, $3, 1, 20.0, 20.0, 20.0)
	`, sensorID, regionID, bucket); err != nil {
		t.Fatalf("seed five-minute bucket: %v", err)
	}

	window := Window{Start: now.Add(-7 * 24 * time.Hour), End: now}
	result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, window, 0, tiers.TierRaw, Page{}, false)
	if err != nil {
		t.Fatalf("Series over a 7-day window with requested=raw: %v", err)
	}
	if result.Tier.Tier == tiers.TierRaw {
		t.Errorf("Tier = %q, want a coarser tier -- a 7-day window exceeds NFR3.2's 48-hour raw cap", result.Tier.Tier)
	}
	if !result.Tier.Coarsened {
		t.Error("Coarsened = false, want true -- coarsening away from a raw request must always be disclosed (FR71)")
	}
	if len(result.Points) != 1 {
		t.Errorf("len(Points) = %d, want 1 (the seeded five-minute bucket) -- the request must be served, not merely not-rejected", len(result.Points))
	}
}

// TestSeries_ResultCapEnforced proves NFR3.2's "the result cap is
// enforced": requesting a page_size far above contract.PageCap returns at
// most PageCap points, with a next_page_token signalling more remain.
func TestSeries_ResultCapEnforced(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	total := int(contract.PageCap) + 5
	for i := 0; i < total; i++ {
		f.insertReading(t, sensorID, regionID, float64(i), now.Add(-time.Duration(total-i)*time.Second))
	}

	window := Window{Start: now.Add(-time.Hour), End: now.Add(time.Second)}
	result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, window, 0, tiers.TierRaw, Page{Size: contract.PageCap * 1000}, false)
	if err != nil {
		t.Fatalf("Series with an over-cap page size: %v", err)
	}
	if len(result.Points) != int(contract.PageCap) {
		t.Errorf("len(Points) = %d, want the clamped cap %d", len(result.Points), contract.PageCap)
	}
	if result.NextPageToken == "" {
		t.Error("NextPageToken is empty, want a token since more readings remain beyond the clamped page")
	}
}

// ── FR26.1: invalid, missing and zero are three distinguishable outcomes ──

// TestSeries_ZeroValueVsInvalidVsMissing_Distinguishable proves FR26.1: a
// reading of exactly zero is presented as fact (not marked suspect, not
// omitted), an invalid reading is marked suspect rather than presented as
// fact, and a time slot with no reading at all is simply absent from the
// series -- three distinguishable outcomes in one response, not
// collapsible into each other.
func TestSeries_ZeroValueVsInvalidVsMissing_Distinguishable(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	zeroAt := now.Add(-30 * time.Minute)
	invalidAt := now.Add(-20 * time.Minute)
	missingAt := now.Add(-10 * time.Minute) // deliberately never inserted

	f.insertReadingValid(t, sensorID, regionID, 0.0, zeroAt, true)
	f.insertReadingValid(t, sensorID, regionID, 999.0, invalidAt, false)

	window := Window{Start: now.Add(-time.Hour), End: now}
	result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, window, 0, tiers.TierRaw, Page{}, false)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(result.Points) != 2 {
		t.Fatalf("len(Points) = %d, want 2 (the zero reading and the invalid reading; the missing slot must never manifest as a third point)", len(result.Points))
	}

	var zeroPoint, invalidPoint *Point
	for i := range result.Points {
		p := &result.Points[i]
		switch {
		case p.RecordedAt.Equal(zeroAt):
			zeroPoint = p
		case p.RecordedAt.Equal(invalidAt):
			invalidPoint = p
		case p.RecordedAt.Equal(missingAt):
			t.Fatalf("a point exists at the missing time slot %s -- a gap must never manifest as a point", missingAt)
		}
	}
	if zeroPoint == nil {
		t.Fatal("no point found at the zero-value reading's timestamp")
	}
	if zeroPoint.Value != 0.0 {
		t.Errorf("zero-value point Value = %v, want 0.0 (a real, present, non-suspect value)", zeroPoint.Value)
	}
	if len(zeroPoint.SuspectChecks) != 0 {
		t.Errorf("zero-value point SuspectChecks = %v, want none -- zero is a fact, not suspect", zeroPoint.SuspectChecks)
	}
	if invalidPoint == nil {
		t.Fatal("no point found at the invalid reading's timestamp")
	}
	found := false
	for _, c := range invalidPoint.SuspectChecks {
		if c == suspect.CheckPersistedInvalidFlag {
			found = true
		}
	}
	if !found {
		t.Errorf("invalid point SuspectChecks = %v, want to contain %q -- an invalid reading must be presented as suspect, not as fact", invalidPoint.SuspectChecks, suspect.CheckPersistedInvalidFlag)
	}
}

// ── FR26.1: a bucket with no readings is absent, never zero or interpolated ──

// TestSeries_Gaps_AbsentAtAllThreeTiers proves "a bucket containing no
// readings is absent from the response, never present with a zero or an
// interpolated value" at all three tiers Series can answer from: raw
// (missing raw readings never manifest as points), five-minute and hourly
// (a tier bucket this task's aggregation job never wrote a row for must
// never be synthesized as a zero-filled or interpolated point).
func TestSeries_Gaps_AbsentAtAllThreeTiers(t *testing.T) {
	f := newFixture(t)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	t.Run("raw", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		presentA := now.Add(-25 * time.Minute)
		presentB := now.Add(-5 * time.Minute)
		gap := now.Add(-15 * time.Minute) // never inserted

		f.insertReadingValid(t, sensorID, regionID, 10.0, presentA, true)
		f.insertReadingValid(t, sensorID, regionID, 12.0, presentB, true)

		result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, Window{Start: now.Add(-time.Hour), End: now}, 0, tiers.TierRaw, Page{}, false)
		if err != nil {
			t.Fatalf("Series: %v", err)
		}
		if len(result.Points) != 2 {
			t.Fatalf("len(Points) = %d, want 2", len(result.Points))
		}
		for _, p := range result.Points {
			if p.RecordedAt.Equal(gap) {
				t.Fatalf("a point exists at the raw gap's timestamp %s", gap)
			}
		}
	})

	t.Run("five_minute", func(t *testing.T) {
		now := time.Now().UTC()
		base := now.Truncate(5 * time.Minute).Add(-30 * time.Minute)
		bucketA := base
		bucketGap := base.Add(5 * time.Minute) // never inserted
		bucketB := base.Add(10 * time.Minute)

		f.insertFiveMinuteBucket(t, sensorID, regionID, bucketA, 3, 30.0, 9.0, 11.0)
		f.insertFiveMinuteBucket(t, sensorID, regionID, bucketB, 3, 36.0, 11.0, 13.0)

		result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, Window{Start: base, End: base.Add(15 * time.Minute)}, 0, tiers.TierFiveMinute, Page{}, false)
		if err != nil {
			t.Fatalf("Series: %v", err)
		}
		if result.Tier.Tier != tiers.TierFiveMinute {
			t.Fatalf("Tier = %q, want %q (test setup requires an uncoarsened five-minute request)", result.Tier.Tier, tiers.TierFiveMinute)
		}
		if len(result.Points) != 2 {
			t.Fatalf("len(Points) = %d, want 2 (the gap bucket must be absent, not zero-filled)", len(result.Points))
		}
		for _, p := range result.Points {
			if p.RecordedAt.Equal(bucketGap) {
				t.Fatalf("a point exists at the missing five-minute bucket %s", bucketGap)
			}
			if p.Count == 0 || p.Value == 0 {
				t.Errorf("point at %s has Count=%d Value=%v -- looks synthesized/interpolated rather than a real seeded bucket", p.RecordedAt, p.Count, p.Value)
			}
		}
	})

	t.Run("hourly", func(t *testing.T) {
		now := time.Now().UTC()
		base := now.Truncate(time.Hour).Add(-6 * time.Hour)
		bucketA := base
		bucketGap := base.Add(time.Hour) // never inserted
		bucketB := base.Add(2 * time.Hour)

		f.insertHourlyBucket(t, sensorID, regionID, bucketA, 4, 80.0, 18.0, 22.0)
		f.insertHourlyBucket(t, sensorID, regionID, bucketB, 4, 84.0, 19.0, 23.0)

		result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, Window{Start: base, End: base.Add(3 * time.Hour)}, 0, tiers.TierHourly, Page{}, false)
		if err != nil {
			t.Fatalf("Series: %v", err)
		}
		if len(result.Points) != 2 {
			t.Fatalf("len(Points) = %d, want 2 (the gap bucket must be absent, not zero-filled)", len(result.Points))
		}
		for _, p := range result.Points {
			if p.RecordedAt.Equal(bucketGap) {
				t.Fatalf("a point exists at the missing hourly bucket %s", bucketGap)
			}
		}
	})
}

// ── FR26.1: Super Admin invalid-only filter ─────────────────────────────

// TestSeries_InvalidOnlyFilter_ReturnsExactSet proves invalidOnly=true
// returns exactly the invalid readings, not a superset or subset, and
// always answers from the raw tier (sensor_reading.valid has no
// aggregated-tier analogue).
func TestSeries_InvalidOnlyFilter_ReturnsExactSet(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	f.insertReadingValid(t, sensorID, regionID, 20.0, now.Add(-30*time.Minute), true)
	f.insertReadingValid(t, sensorID, regionID, 999.0, now.Add(-20*time.Minute), false)
	f.insertReadingValid(t, sensorID, regionID, 21.0, now.Add(-10*time.Minute), true)

	window := Window{Start: now.Add(-time.Hour), End: now}
	result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, window, 0, tiers.TierHourly, Page{}, true)
	if err != nil {
		t.Fatalf("Series (invalidOnly): %v", err)
	}
	if result.Tier.Tier != tiers.TierRaw {
		t.Errorf("Tier = %q, want %q -- invalid-only is only meaningful at the raw tier", result.Tier.Tier, tiers.TierRaw)
	}
	if len(result.Points) != 1 {
		t.Fatalf("len(Points) = %d, want 1 (exactly the invalid reading)", len(result.Points))
	}
	if result.Points[0].Value != 999.0 {
		t.Errorf("Points[0].Value = %v, want 999.0 (the invalid reading)", result.Points[0].Value)
	}
}

// ── FR26.2: stale attribution, per-reading, never re-stamped ────────────

// TestSeries_StaleAttribution_Marked_RegionIDUnchanged proves FR26.2: a
// reading whose stamped region_id disagrees with sensor_region_history at
// its own recorded_at is marked CheckStaleAttribution, and its stored
// region_id is never rewritten to the historically-correct value --
// FR26.2's compensating control is the marker, not a silent correction.
func TestSeries_StaleAttribution_Marked_RegionIDUnchanged(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	staleRegion := f.insertRegion(t, "stale-region", 0)
	correctRegion := f.insertRegion(t, "correct-region", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, staleRegion, temperatureTypeID)

	readingAt := now.Add(-30 * time.Minute)
	// sensor_region_history says the sensor was actually in correctRegion
	// at readingAt -- the reading's own stamped region_id (staleRegion,
	// below) disagrees.
	f.insertSensorRegionHistory(t, sensorID, correctRegion, now.Add(-time.Hour), nil)
	f.insertReadingValid(t, sensorID, staleRegion, 20.0, readingAt, true)

	window := Window{Start: now.Add(-time.Hour), End: now}
	result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, window, 0, tiers.TierRaw, Page{}, false)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("len(Points) = %d, want 1", len(result.Points))
	}
	var marked bool
	for _, c := range result.Points[0].SuspectChecks {
		if c == suspect.CheckStaleAttribution {
			marked = true
		}
	}
	if !marked {
		t.Errorf("SuspectChecks = %v, want to contain %q", result.Points[0].SuspectChecks, suspect.CheckStaleAttribution)
	}

	if gotRegion := f.readingRegionID(t, sensorID, readingAt); gotRegion != staleRegion {
		t.Errorf("stored sensor_reading.region_id = %d, want unchanged %d (staleRegion) -- FR26.2 forbids retroactive re-stamping", gotRegion, staleRegion)
	}
}

// ── FR21/FR26.2: migration-snap window ───────────────────────────────────

// TestSeries_MigrationSnapWindow_Marked proves a reading recorded inside
// the hour bucket that two distinct plants' plant_region_history intervals
// overlap in the same region (FR21's disclosed migration-snap cost) is
// marked CheckMigrationSnapWindow.
func TestSeries_MigrationSnapWindow_Marked(t *testing.T) {
	f := newFixture(t)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	hourStart := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	removedAt := hourStart.Add(20 * time.Minute)

	plantA := f.insertPlant(t, regionID)
	plantB := f.insertPlant(t, regionID)
	// plantA is removed mid-hour (removedAt); the migration backfill snaps
	// plantB's own valid_from back to the hour boundary, so both plants'
	// intervals are concurrently open across [hourStart, removedAt) --
	// FR21's disclosed shared-hour cost.
	f.insertPlantRegionHistory(t, plantA, regionID, hourStart.Add(-time.Hour), &removedAt)
	f.insertPlantRegionHistory(t, plantB, regionID, hourStart, nil)

	readingAt := hourStart.Add(10 * time.Minute) // inside the shared hour
	f.insertReadingValid(t, sensorID, regionID, 20.0, readingAt, true)

	window := Window{Start: hourStart.Add(-time.Hour), End: hourStart.Add(time.Hour)}
	result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, window, 0, tiers.TierRaw, Page{}, false)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("len(Points) = %d, want 1", len(result.Points))
	}
	var marked bool
	for _, c := range result.Points[0].SuspectChecks {
		if c == suspect.CheckMigrationSnapWindow {
			marked = true
		}
	}
	if !marked {
		t.Errorf("SuspectChecks = %v, want to contain %q", result.Points[0].SuspectChecks, suspect.CheckMigrationSnapWindow)
	}
}

// ── FR26.3: every marker names its own check, enumerable and stable ─────

// suspectCheckIsEnumerable reports whether c is a member of suspect.All()
// -- FR26.3's "every suspect marker is attributable to a named, enumerable
// check."
func suspectCheckIsEnumerable(c suspect.Check) bool {
	for _, known := range suspect.All() {
		if c == known {
			return true
		}
	}
	return false
}

// TestSeries_MultipleChecksOnOnePoint_EachNamedDistinctly proves FR26.3: a
// single reading that trips every one of the four checks simultaneously
// carries all four named identifiers (each drawn from suspect.All(), never
// a free-form string), each exactly once -- and marked_count/returned_count
// tally readings, not checks, so one reading with four checks contributes
// 1 to marked_count, not 4.
func TestSeries_MultipleChecksOnOnePoint_EachNamedDistinctly(t *testing.T) {
	f := newFixture(t)
	staleRegion := f.insertRegion(t, "stale-region", 0)
	correctRegion := f.insertRegion(t, "correct-region", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, staleRegion, temperatureTypeID)

	hourStart := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	removedAt := hourStart.Add(20 * time.Minute)
	plantA := f.insertPlant(t, staleRegion)
	plantB := f.insertPlant(t, staleRegion)
	f.insertPlantRegionHistory(t, plantA, staleRegion, hourStart.Add(-time.Hour), &removedAt)
	f.insertPlantRegionHistory(t, plantB, staleRegion, hourStart, nil)

	readingAt := hourStart.Add(10 * time.Minute)
	// out-of-range (999 exceeds temperature's [-40,60]), persisted-invalid
	// (valid=false), stale-attribution (stamped staleRegion vs. history's
	// correctRegion) and migration-snap (inside the shared hour above) --
	// all four checks at once.
	f.insertSensorRegionHistory(t, sensorID, correctRegion, hourStart.Add(-time.Hour), nil)
	f.insertReadingValid(t, sensorID, staleRegion, 999.0, readingAt, false)

	window := Window{Start: hourStart.Add(-time.Hour), End: hourStart.Add(time.Hour)}
	result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, window, 0, tiers.TierRaw, Page{}, false)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("len(Points) = %d, want 1", len(result.Points))
	}
	got := result.Points[0].SuspectChecks
	want := map[suspect.Check]bool{
		suspect.CheckOutOfRange:           true,
		suspect.CheckPersistedInvalidFlag: true,
		suspect.CheckStaleAttribution:     true,
		suspect.CheckMigrationSnapWindow:  true,
	}
	seen := make(map[suspect.Check]int)
	for _, c := range got {
		seen[c]++
		if !suspectCheckIsEnumerable(c) {
			t.Errorf("check %q is not a member of suspect.All()", c)
		}
	}
	for c := range want {
		if seen[c] != 1 {
			t.Errorf("check %q appears %d times in SuspectChecks %v, want exactly once", c, seen[c], got)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("SuspectChecks = %v, want exactly the four checks, no extras", got)
	}
	if result.MarkedCount != 1 {
		t.Errorf("MarkedCount = %d, want 1 (one marked reading, regardless of how many checks it trips)", result.MarkedCount)
	}
	if result.ReturnedCount != 1 {
		t.Errorf("ReturnedCount = %d, want 1", result.ReturnedCount)
	}
}

// ── FR26.3: marked_count/returned_count present in every case ───────────

// TestSeries_MarkedReturnedCounts_NoneMarkedAndAllMarked proves
// marked_count/returned_count are present and correct both when nothing is
// marked (a marker that covers nothing must be visible as such) and when
// everything is marked (a marker that covers everything must be visible as
// such, not silently universal).
func TestSeries_MarkedReturnedCounts_NoneMarkedAndAllMarked(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)

	t.Run("none marked", func(t *testing.T) {
		sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)
		f.insertReadingValid(t, sensorID, regionID, 20.0, now.Add(-20*time.Minute), true)
		f.insertReadingValid(t, sensorID, regionID, 21.0, now.Add(-10*time.Minute), true)

		result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, Window{Start: now.Add(-time.Hour), End: now}, 0, tiers.TierRaw, Page{}, false)
		if err != nil {
			t.Fatalf("Series: %v", err)
		}
		if result.ReturnedCount != 2 {
			t.Errorf("ReturnedCount = %d, want 2", result.ReturnedCount)
		}
		if result.MarkedCount != 0 {
			t.Errorf("MarkedCount = %d, want 0", result.MarkedCount)
		}
	})

	t.Run("all marked", func(t *testing.T) {
		sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)
		f.insertReadingValid(t, sensorID, regionID, 20.0, now.Add(-20*time.Minute), false)
		f.insertReadingValid(t, sensorID, regionID, 21.0, now.Add(-10*time.Minute), false)

		result, err := f.reader.Series(context.Background(), authz.EntityRef{Kind: authz.EntitySensor, ID: sensorID}, Window{Start: now.Add(-time.Hour), End: now}, 0, tiers.TierRaw, Page{}, false)
		if err != nil {
			t.Fatalf("Series: %v", err)
		}
		if result.ReturnedCount != 2 {
			t.Errorf("ReturnedCount = %d, want 2", result.ReturnedCount)
		}
		if result.MarkedCount != 2 {
			t.Errorf("MarkedCount = %d, want 2 (every returned reading is marked -- must be visibly universal, not silently so)", result.MarkedCount)
		}
	})
}

// TestCurrentValues_MarkedReturnedCounts proves GetCurrentValues also
// carries FR26.3's marked_count/returned_count, correctly reflecting an
// invalid reading among otherwise-valid ones.
func TestCurrentValues_MarkedReturnedCounts(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorA := f.insertSensor(t, boardID, regionID, temperatureTypeID)
	sensorB := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	f.insertReadingValid(t, sensorA, regionID, 20.0, now, true)
	f.insertReadingValid(t, sensorB, regionID, 999.0, now, false)

	result, err := f.reader.CurrentValues(context.Background(), authz.EntityRef{Kind: authz.EntityRegion, ID: regionID})
	if err != nil {
		t.Fatalf("CurrentValues: %v", err)
	}
	if result.ReturnedCount != 2 {
		t.Errorf("ReturnedCount = %d, want 2", result.ReturnedCount)
	}
	if result.MarkedCount != 1 {
		t.Errorf("MarkedCount = %d, want 1 (exactly the invalid+out-of-range reading)", result.MarkedCount)
	}
}

// TestPeriodSummary_MarkedReturnedCounts proves GetPeriodSummary also
// carries FR26.3's marked_count/returned_count, reflecting a suspect
// hourly bucket contributing to the summary.
func TestPeriodSummary_MarkedReturnedCounts(t *testing.T) {
	f := newFixture(t)
	periodStart := time.Now().UTC().Truncate(time.Hour).Add(-24 * time.Hour)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorID := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	hourBucket := periodStart.Add(2 * time.Hour)
	f.insertHourlyBucket(t, sensorID, regionID, hourBucket, 2, 40.0, 19.0, 21.0)
	// An invalid raw reading inside the same hour, surfacing the bucket as
	// suspect even though the hourly aggregate itself carries no invalid flag.
	f.insertReadingValid(t, sensorID, regionID, 20.0, hourBucket.Add(10*time.Minute), false)

	period := Window{Start: periodStart, End: periodStart.Add(24 * time.Hour)}
	result, err := f.reader.PeriodSummary(context.Background(), regionID, period, temperatureTypeID)
	if err != nil {
		t.Fatalf("PeriodSummary: %v", err)
	}
	if result.ReturnedCount == 0 {
		t.Fatal("ReturnedCount = 0, want at least the one summary computed")
	}
	if result.MarkedCount == 0 {
		t.Error("MarkedCount = 0, want at least 1 -- the summary's contributing hour has an invalid raw reading")
	}
}

// TestCompare_MarkedReturnedCounts_AggregateAcrossEntities proves
// CompareSeries's marked_count/returned_count tally across every entity's
// series, not just the first.
func TestCompare_MarkedReturnedCounts_AggregateAcrossEntities(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	regionID := f.insertRegion(t, "greenhouse", 0)
	boardID := f.insertBoard(t)
	sensorA := f.insertSensor(t, boardID, regionID, temperatureTypeID)
	sensorB := f.insertSensor(t, boardID, regionID, temperatureTypeID)

	f.insertReadingValid(t, sensorA, regionID, 20.0, now.Add(-10*time.Minute), true)
	f.insertReadingValid(t, sensorB, regionID, 999.0, now.Add(-10*time.Minute), false)

	entities := []authz.EntityRef{
		{Kind: authz.EntitySensor, ID: sensorA},
		{Kind: authz.EntitySensor, ID: sensorB},
	}
	result, err := f.reader.Compare(context.Background(), entities, Window{Start: now.Add(-time.Hour), End: now}, temperatureTypeID, tiers.TierRaw, Page{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if result.ReturnedCount != 2 {
		t.Errorf("ReturnedCount = %d, want 2 (one point per entity)", result.ReturnedCount)
	}
	if result.MarkedCount != 1 {
		t.Errorf("MarkedCount = %d, want 1 (sensorB's invalid+out-of-range reading)", result.MarkedCount)
	}
}
