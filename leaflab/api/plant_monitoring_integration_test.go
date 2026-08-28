//go:build integration

// Real-Postgres integration coverage for #1381's FR56 (GR-4; SB-1.11) plant
// monitoring status -- GetPlantMonitoringStatus/ListPlantMonitoringStatus
// (leaflab/api/plant_monitoring.go). Exercised the same way
// plants_lifecycle_integration_test.go exercises #1377's plant lifecycle:
// hermetic, hand-written schema, not shared with any other integration test
// file in this package (see that file's doc comment for the rationale).
//
// Plain postgres (not TimescaleDB): computeMonitoringStatus never calls
// time_bucket(), unlike CreatePlant/MovePlant's capture.Recorder.Record --
// so fixtures here write plant/plant_region_history rows directly via SQL
// (pmInsertPlant/pmMovePlant below), bypassing placement.MoveTx and
// capture.Recorder entirely, rather than going through Repository.CreatePlant/
// MovePlant (which would additionally require a TimescaleDB image and a
// registered sensor subtree per FR20's boundary-capture side effect, neither
// of which this file's tests need).
//
// v_region_path and v_sensor_reading_with_plant (both migration 012) are
// recreated here; the latter is trimmed to the columns
// lastAttributableReadingAt actually reads (plant_id, recorded_at) but
// keeps its exact join predicate (region_id equality plus the
// created_at/removed_at attribution window) verbatim, per this package's
// "hermetic, trimmed to what X reads" precedent (see
// plants_lifecycle_integration_test.go's schema doc comment, and its own
// hand-written postmortem query using this same join predicate directly).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:plant_monitoring_integration_test --test_output=all
package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const pmTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE household_membership (
		household_membership_id BIGSERIAL PRIMARY KEY,
		household_id             BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject        TEXT NOT NULL,
		valid_from                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                  TIMESTAMPTZ
	);
	CREATE INDEX idx_pm_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name              VARCHAR(255) NOT NULL,
		household_id      BIGINT REFERENCES household(household_id)
	);

	CREATE TABLE plant_type (
		plant_type_id BIGSERIAL PRIMARY KEY,
		common_name   VARCHAR(128) NOT NULL
	);

	CREATE TABLE plant (
		plant_id      BIGSERIAL PRIMARY KEY,
		region_id     BIGINT NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
		plant_type_id BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
		household_id  BIGINT NOT NULL REFERENCES household(household_id),
		name          VARCHAR(128) NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		removed_at    TIMESTAMPTZ
	);

	CREATE TABLE plant_region_history (
		plant_region_history_id BIGSERIAL PRIMARY KEY,
		plant_id            BIGINT NOT NULL REFERENCES plant(plant_id),
		region_id           BIGINT NOT NULL REFERENCES region(region_id),
		valid_from           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to             TIMESTAMPTZ,
		relocation_induced   BOOLEAN NOT NULL DEFAULT FALSE
	);
	CREATE INDEX idx_pm_prh_plant_id_current
		ON plant_region_history(plant_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_pm_prh_region_id_current
		ON plant_region_history(region_id) WHERE valid_to IS NULL;

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY,
		region_id BIGINT REFERENCES region(region_id)
	);

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		region_id   BIGINT REFERENCES region(region_id),
		value       DOUBLE PRECISION NOT NULL,
		recorded_at TIMESTAMPTZ NOT NULL
	);

	-- v_region_path: migration 012_views, verbatim shape.
	CREATE VIEW v_region_path AS
	WITH RECURSIVE path AS (
		SELECT
			r.region_id,
			r.name,
			r.parent_region_id,
			ARRAY[r.region_id]::BIGINT[] AS path_ids,
			ARRAY[r.name]::TEXT[]        AS path_names,
			r.name::TEXT                 AS path_name,
			0                            AS depth
		FROM region r
		WHERE r.parent_region_id IS NULL

		UNION ALL

		SELECT
			r.region_id,
			r.name,
			r.parent_region_id,
			p.path_ids   || r.region_id,
			p.path_names || r.name,
			p.path_name  || ' / ' || r.name,
			p.depth + 1
		FROM region r
		JOIN path p ON p.region_id = r.parent_region_id
	)
	SELECT region_id, name, parent_region_id, path_ids, path_names, path_name, depth FROM path;

	-- v_sensor_reading_with_plant (migration 012), trimmed to the columns
	-- lastAttributableReadingAt reads (plant_id, recorded_at) -- see this
	-- file's doc comment. Join predicate is verbatim: region_id equality
	-- plus the created_at/removed_at attribution window.
	CREATE VIEW v_sensor_reading_with_plant AS
	SELECT
		sr.reading_id,
		sr.recorded_at,
		sr.region_id,
		p.plant_id,
		p.name AS plant_name
	FROM sensor_reading sr
	LEFT JOIN plant p
	       ON p.region_id  = sr.region_id
	      AND p.created_at <= sr.recorded_at
	      AND (p.removed_at IS NULL OR p.removed_at > sr.recorded_at);
`

// newPMTestRepository starts a real (plain) Postgres container with
// pmTestSchema applied and returns a *Repository plus a real
// authz.PGResolver (GetPlantMonitoringStatus authorizes via
// authorizePlantWrite, which needs real household/household_membership SQL
// to resolve a caller's scope) and the raw pool for fixture setup.
func newPMTestRepository(t *testing.T) (*Repository, *authz.PGResolver, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: pmTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return repo, resolver, db.Pool
}

func pmInsertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func pmInsertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

// pmScopeFor resolves subject's real authz.Scope via ScopeForPrincipal,
// mirroring plantsScopeFor/scopeFor in the other lifecycle integration test
// files.
func pmScopeFor(t *testing.T, resolver *authz.PGResolver, subject string) authz.Scope {
	t.Helper()
	scope, err := resolver.ScopeForPrincipal(context.Background(), subject)
	if err != nil {
		t.Fatalf("ScopeForPrincipal(%q): %v", subject, err)
	}
	return scope
}

// pmInsertRegion inserts a region row directly via SQL. parentRegionID is
// nil for a root region.
func pmInsertRegion(t *testing.T, pool *pgxpool.Pool, name string, parentRegionID *int64, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (parent_region_id, name, household_id) VALUES ($1, $2, $3) RETURNING region_id`,
		parentRegionID, name, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

func pmInsertPlantType(t *testing.T, pool *pgxpool.Pool, commonName string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id`,
		commonName).Scan(&id); err != nil {
		t.Fatalf("insert plant_type %s: %v", commonName, err)
	}
	return id
}

// pmInsertPlant inserts a plant row and opens its first plant_region_history
// interval directly via SQL -- bypassing Repository.CreatePlant and
// placement.MoveTx entirely (see this file's doc comment) -- mirroring the
// two rows CreatePlant would have written. Returns the plant_id and the
// interval's actual valid_from (via RETURNING, for exact-equality
// assertions against status.Since later, since a Go time.Time compared
// against a value that has round-tripped through a TIMESTAMPTZ column loses
// sub-microsecond precision).
func pmInsertPlant(t *testing.T, pool *pgxpool.Pool, regionID, plantTypeID, householdID int64, name string) (plantID int64, placedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO plant (region_id, plant_type_id, household_id, name) VALUES ($1, $2, $3, $4) RETURNING plant_id`,
		regionID, plantTypeID, householdID, name).Scan(&plantID); err != nil {
		t.Fatalf("insert plant %s: %v", name, err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO plant_region_history (plant_id, region_id) VALUES ($1, $2) RETURNING valid_from`,
		plantID, regionID).Scan(&placedAt); err != nil {
		t.Fatalf("insert plant_region_history for plant %d: %v", plantID, err)
	}
	return plantID, placedAt
}

func pmInsertSensor(t *testing.T, pool *pgxpool.Pool, regionID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor (region_id) VALUES ($1) RETURNING sensor_id`,
		regionID).Scan(&id); err != nil {
		t.Fatalf("insert sensor in region %d: %v", regionID, err)
	}
	return id
}

// pmDeleteSensor removes a sensor row entirely -- simulating "this region's
// sensor coverage was removed" for the since-when fallback tests. Readings
// already recorded remain (sensor_reading has no FK to sensor in this
// schema, mirroring production: sensor_reading.region_id is a snapshot
// taken at insert time, not a live join to sensor).
func pmDeleteSensor(t *testing.T, pool *pgxpool.Pool, sensorID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DELETE FROM sensor WHERE sensor_id = $1`, sensorID); err != nil {
		t.Fatalf("delete sensor %d: %v", sensorID, err)
	}
}

// pmInsertReading inserts a sensor_reading row and returns the actual
// recorded_at Postgres stored (RETURNING), for the same exact-equality
// reason pmInsertPlant's placedAt is returned via RETURNING.
func pmInsertReading(t *testing.T, pool *pgxpool.Pool, regionID int64, value float64, recordedAt time.Time) (actual time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor_reading (region_id, value, recorded_at) VALUES ($1, $2, $3) RETURNING recorded_at`,
		regionID, value, recordedAt).Scan(&actual); err != nil {
		t.Fatalf("insert sensor_reading in region %d: %v", regionID, err)
	}
	return actual
}

// pmBackdatePlantCreatedAt sets plant.created_at directly -- CreatePlant
// always stamps the real NOW(), so a fixture wanting historical readings
// attributed to a plant (v_sensor_reading_with_plant's join requires
// p.created_at <= sr.recorded_at) must backdate created_at first, same
// precedent as
// TestRetirePlant_ReadingsRemainReachable_PostmortemOverFinalWeeks
// (plants_lifecycle_integration_test.go).
func pmBackdatePlantCreatedAt(t *testing.T, pool *pgxpool.Pool, plantID int64, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE plant SET created_at = $1 WHERE plant_id = $2`, createdAt, plantID); err != nil {
		t.Fatalf("backdate plant %d created_at: %v", plantID, err)
	}
}

// -- FR56's "iff", both directions -------------------------------------------

// TestGetPlantMonitoringStatus_DirectSensorInOwnRegion_Monitored proves one
// direction of FR56's "monitored iff attributable": a plant with a sensor
// directly in its own region (no interception possible) is monitored.
func TestGetPlantMonitoringStatus_DirectSensorInOwnRegion_Monitored(t *testing.T) {
	repo, resolver, pool := newPMTestRepository(t)
	ctx := context.Background()

	householdID := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdID, "alice")
	scope := pmScopeFor(t, resolver, "alice")

	regionID := pmInsertRegion(t, pool, "Grow Room", nil, householdID)
	plantTypeID := pmInsertPlantType(t, pool, "Fern")
	plantID, _ := pmInsertPlant(t, pool, regionID, plantTypeID, householdID, "Fred")
	pmInsertSensor(t, pool, regionID)

	status, found, err := repo.GetPlantMonitoringStatus(ctx, plantID, scope)
	if err != nil {
		t.Fatalf("GetPlantMonitoringStatus: %v", err)
	}
	if !found {
		t.Fatal("plant not found")
	}
	if !status.Monitored {
		t.Error("Monitored = false, want true: a sensor directly in the plant's own region attributes to it (FR23)")
	}
	if status.Reason != "" {
		t.Errorf("Reason = %q on a monitored plant, want empty", status.Reason)
	}
	if status.Intercepting != nil {
		t.Errorf("Intercepting = %+v on a monitored plant, want nil", status.Intercepting)
	}
	if status.Since != nil {
		t.Errorf("Since = %v on a monitored plant, want nil", status.Since)
	}
}

// TestGetPlantMonitoringStatus_DescendantActivePlantIntercepts_Unmonitored
// proves the other direction, and the case that distinguishes FR56 from "a
// sensor exists somewhere beneath it" (SB-1.11's whole point, per the issue
// text): a plant in a parent region, with its only reachable sensor sitting
// in a descendant region that itself holds an active plant, is unmonitored
// -- that descendant's own plant intercepts the reading first (FR23). It
// also asserts the unmonitored reason names the intercepting region and
// plant, and sanity-checks the intercepting (child) plant is itself
// monitored by that same sensor.
func TestGetPlantMonitoringStatus_DescendantActivePlantIntercepts_Unmonitored(t *testing.T) {
	repo, resolver, pool := newPMTestRepository(t)
	ctx := context.Background()

	householdID := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdID, "alice")
	scope := pmScopeFor(t, resolver, "alice")

	parentID := pmInsertRegion(t, pool, "Parent Room", nil, householdID)
	childID := pmInsertRegion(t, pool, "Child Room", &parentID, householdID)
	plantTypeID := pmInsertPlantType(t, pool, "Fern")

	parentPlantID, _ := pmInsertPlant(t, pool, parentID, plantTypeID, householdID, "Parent Plant")
	childPlantID, _ := pmInsertPlant(t, pool, childID, plantTypeID, householdID, "Child Plant")
	// The sensor sits in the descendant region -- a naive "a sensor exists
	// somewhere in the subtree" reading would call the parent plant
	// monitored. FR23 says otherwise: the reading attributes to childID
	// (nearest ancestor, including itself, holding an active plant), never
	// reaching parentID.
	pmInsertSensor(t, pool, childID)

	status, found, err := repo.GetPlantMonitoringStatus(ctx, parentPlantID, scope)
	if err != nil {
		t.Fatalf("GetPlantMonitoringStatus: %v", err)
	}
	if !found {
		t.Fatal("plant not found")
	}
	if status.Monitored {
		t.Fatal("Monitored = true, want false: the descendant region's own active plant intercepts every reading before it reaches the parent (FR23, FR56)")
	}
	if status.Reason != UnmonitoredReasonIntercepted {
		t.Errorf("Reason = %q, want %q", status.Reason, UnmonitoredReasonIntercepted)
	}
	if status.Intercepting == nil {
		t.Fatal("Intercepting = nil on an intercepted plant")
	}
	if status.Intercepting.RegionID != childID {
		t.Errorf("Intercepting.RegionID = %d, want %d (the descendant region)", status.Intercepting.RegionID, childID)
	}
	if status.Intercepting.RegionName != "Child Room" {
		t.Errorf("Intercepting.RegionName = %q, want %q", status.Intercepting.RegionName, "Child Room")
	}
	if status.Intercepting.PlantID != childPlantID {
		t.Errorf("Intercepting.PlantID = %d, want %d (the plant now taking those readings)", status.Intercepting.PlantID, childPlantID)
	}
	if status.Intercepting.PlantName != "Child Plant" {
		t.Errorf("Intercepting.PlantName = %q, want %q", status.Intercepting.PlantName, "Child Plant")
	}

	childStatus, found, err := repo.GetPlantMonitoringStatus(ctx, childPlantID, scope)
	if err != nil || !found {
		t.Fatalf("GetPlantMonitoringStatus(child): found=%v err=%v", found, err)
	}
	if !childStatus.Monitored {
		t.Error("child plant Monitored = false, want true -- the sensor in its own region attributes to it")
	}
}

// -- FR56's since-when --------------------------------------------------------

// TestGetPlantMonitoringStatus_SinceWhen_BecameUnmonitoredWhenPlacedBelow
// proves the since-when derivation for the interception case: a plant that
// was monitored (its sensor's reading reached it because no descendant had
// an active plant yet) becomes unmonitored the instant another plant is
// placed into the descendant region, and Since is exactly that instant.
func TestGetPlantMonitoringStatus_SinceWhen_BecameUnmonitoredWhenPlacedBelow(t *testing.T) {
	repo, resolver, pool := newPMTestRepository(t)
	ctx := context.Background()

	householdID := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdID, "alice")
	scope := pmScopeFor(t, resolver, "alice")

	parentID := pmInsertRegion(t, pool, "Parent Room", nil, householdID)
	childID := pmInsertRegion(t, pool, "Child Room", &parentID, householdID)
	plantTypeID := pmInsertPlantType(t, pool, "Fern")

	parentPlantID, parentPlacedAt := pmInsertPlant(t, pool, parentID, plantTypeID, householdID, "Parent Plant")
	pmInsertSensor(t, pool, childID)

	before, found, err := repo.GetPlantMonitoringStatus(ctx, parentPlantID, scope)
	if err != nil || !found {
		t.Fatalf("GetPlantMonitoringStatus (before): found=%v err=%v", found, err)
	}
	if !before.Monitored {
		t.Fatal("Monitored = false before any descendant plant exists, want true -- the sensor's reading has nowhere closer to attribute to")
	}

	_, childPlacedAt := pmInsertPlant(t, pool, childID, plantTypeID, householdID, "Child Plant")
	if !childPlacedAt.After(parentPlacedAt) {
		t.Fatalf("test fixture invariant broken: child placed at %v, not after parent placed at %v", childPlacedAt, parentPlacedAt)
	}

	after, found, err := repo.GetPlantMonitoringStatus(ctx, parentPlantID, scope)
	if err != nil || !found {
		t.Fatalf("GetPlantMonitoringStatus (after): found=%v err=%v", found, err)
	}
	if after.Monitored {
		t.Fatal("Monitored = true after a descendant plant was placed, want false")
	}
	if after.Since == nil {
		t.Fatal("Since = nil on an unmonitored plant")
	}
	if !after.Since.Equal(childPlacedAt) {
		t.Errorf("Since = %v, want %v (the instant the intercepting plant's placement opened -- later than the parent's own placement at %v)", after.Since, childPlacedAt, parentPlacedAt)
	}
}

// TestGetPlantMonitoringStatus_SinceWhen_NeverHadSensor proves the
// since-when fallback for a plant whose subtree never had any sensor at
// all: Since is the plant's own placement valid_from (it has never, since
// being placed, had an attributable reading).
func TestGetPlantMonitoringStatus_SinceWhen_NeverHadSensor(t *testing.T) {
	repo, resolver, pool := newPMTestRepository(t)
	ctx := context.Background()

	householdID := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdID, "alice")
	scope := pmScopeFor(t, resolver, "alice")

	regionID := pmInsertRegion(t, pool, "Sensorless Room", nil, householdID)
	plantTypeID := pmInsertPlantType(t, pool, "Fern")
	plantID, placedAt := pmInsertPlant(t, pool, regionID, plantTypeID, householdID, "Lonely Fern")

	status, found, err := repo.GetPlantMonitoringStatus(ctx, plantID, scope)
	if err != nil || !found {
		t.Fatalf("GetPlantMonitoringStatus: found=%v err=%v", found, err)
	}
	if status.Monitored {
		t.Fatal("Monitored = true for a plant with no sensor anywhere in its region subtree, want false")
	}
	if status.Reason != UnmonitoredReasonNoAttributableSensor {
		t.Errorf("Reason = %q, want %q", status.Reason, UnmonitoredReasonNoAttributableSensor)
	}
	if status.Intercepting != nil {
		t.Errorf("Intercepting = %+v, want nil -- no interception, just no coverage", status.Intercepting)
	}
	if status.Since == nil || !status.Since.Equal(placedAt) {
		t.Errorf("Since = %v, want %v (this plant's own placement -- it never had an attributable reading)", status.Since, placedAt)
	}
}

// TestGetPlantMonitoringStatus_SinceWhen_SensorsRemoved proves the
// since-when fallback for a plant that once had an attributable sensor but
// no longer does: Since is the instant of the last reading that was
// attributable to it, not its own placement.
func TestGetPlantMonitoringStatus_SinceWhen_SensorsRemoved(t *testing.T) {
	repo, resolver, pool := newPMTestRepository(t)
	ctx := context.Background()

	householdID := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdID, "alice")
	scope := pmScopeFor(t, resolver, "alice")

	regionID := pmInsertRegion(t, pool, "Formerly Sensed Room", nil, householdID)
	plantTypeID := pmInsertPlantType(t, pool, "Fern")
	plantID, _ := pmInsertPlant(t, pool, regionID, plantTypeID, householdID, "Fern")
	sensorID := pmInsertSensor(t, pool, regionID)

	now := time.Now()
	// Backdate created_at so the historical readings below (which predate
	// "now") fall within the plant's attribution window
	// (v_sensor_reading_with_plant's join requires created_at <=
	// recorded_at) -- CreatePlant always stamps the real NOW(), so a plant
	// inserted moments ago would otherwise postdate every reading below.
	pmBackdatePlantCreatedAt(t, pool, plantID, now.Add(-24*time.Hour))
	pmInsertReading(t, pool, regionID, 1.0, now.Add(-2*time.Hour))
	lastReadingAt := pmInsertReading(t, pool, regionID, 2.0, now.Add(-1*time.Hour))

	pmDeleteSensor(t, pool, sensorID)

	status, found, err := repo.GetPlantMonitoringStatus(ctx, plantID, scope)
	if err != nil || !found {
		t.Fatalf("GetPlantMonitoringStatus: found=%v err=%v", found, err)
	}
	if status.Monitored {
		t.Fatal("Monitored = true after the only sensor was removed, want false")
	}
	if status.Reason != UnmonitoredReasonNoAttributableSensor {
		t.Errorf("Reason = %q, want %q", status.Reason, UnmonitoredReasonNoAttributableSensor)
	}
	if status.Since == nil {
		t.Fatal("Since = nil on an unmonitored plant")
	}
	if !status.Since.Equal(lastReadingAt) {
		t.Errorf("Since = %v, want %v (the last reading that was attributable before the sensor was removed)", status.Since, lastReadingAt)
	}
}

// -- FR23's sibling fan-out ---------------------------------------------------

// TestGetPlantMonitoringStatus_SiblingFanOut_BothMonitored proves a region
// with two active plants and one sensor monitors both plants (FR23's
// sibling fan-out never picks just one).
func TestGetPlantMonitoringStatus_SiblingFanOut_BothMonitored(t *testing.T) {
	repo, resolver, pool := newPMTestRepository(t)
	ctx := context.Background()

	householdID := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdID, "alice")
	scope := pmScopeFor(t, resolver, "alice")

	regionID := pmInsertRegion(t, pool, "Shared Room", nil, householdID)
	plantTypeID := pmInsertPlantType(t, pool, "Fern")
	plantA, _ := pmInsertPlant(t, pool, regionID, plantTypeID, householdID, "Plant A")
	plantB, _ := pmInsertPlant(t, pool, regionID, plantTypeID, householdID, "Plant B")
	pmInsertSensor(t, pool, regionID)

	for _, id := range []int64{plantA, plantB} {
		status, found, err := repo.GetPlantMonitoringStatus(ctx, id, scope)
		if err != nil || !found {
			t.Fatalf("GetPlantMonitoringStatus(%d): found=%v err=%v", id, found, err)
		}
		if !status.Monitored {
			t.Errorf("plant %d Monitored = false, want true -- FR23's sibling fan-out monitors every active plant sharing the attributing region", id)
		}
	}
}

// -- ListPlantMonitoringStatus: pagination + household scoping ---------------

// TestListPlantMonitoringStatus_PaginatesAndIsHouseholdScoped proves the
// bulk listing keyset-paginates on plant_id (same shape as ListPlants) and
// never surfaces another household's plant for a caller's scope.
func TestListPlantMonitoringStatus_PaginatesAndIsHouseholdScoped(t *testing.T) {
	repo, resolver, pool := newPMTestRepository(t)
	ctx := context.Background()

	householdA := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdA, "alice")
	aliceScope := pmScopeFor(t, resolver, "alice")

	householdB := pmInsertHousehold(t, pool)
	pmInsertMembership(t, pool, householdB, "bob")

	regionA := pmInsertRegion(t, pool, "A Room", nil, householdA)
	regionB := pmInsertRegion(t, pool, "B Room", nil, householdB)
	plantTypeID := pmInsertPlantType(t, pool, "Fern")

	var aPlantIDs []int64
	for i := 0; i < 3; i++ {
		id, _ := pmInsertPlant(t, pool, regionA, plantTypeID, householdA, fmt.Sprintf("A Plant %d", i))
		aPlantIDs = append(aPlantIDs, id)
	}
	pmInsertSensor(t, pool, regionA)
	bPlantID, _ := pmInsertPlant(t, pool, regionB, plantTypeID, householdB, "B Plant")

	page1, err := repo.ListPlantMonitoringStatus(ctx, 0, false, 2, aliceScope)
	if err != nil {
		t.Fatalf("ListPlantMonitoringStatus (page1): %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}

	page2, err := repo.ListPlantMonitoringStatus(ctx, page1[len(page1)-1].PlantID, true, 2, aliceScope)
	if err != nil {
		t.Fatalf("ListPlantMonitoringStatus (page2): %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len = %d, want 1", len(page2))
	}

	got := append(append([]PlantMonitoringStatus{}, page1...), page2...)
	if len(got) != len(aPlantIDs) {
		t.Fatalf("total paginated results = %d, want %d", len(got), len(aPlantIDs))
	}
	seen := make(map[int64]bool, len(got))
	for _, s := range got {
		seen[s.PlantID] = true
		if s.PlantID == bPlantID {
			t.Fatalf("household B's plant %d appeared in household A's scoped listing", bPlantID)
		}
	}
	for _, want := range aPlantIDs {
		if !seen[want] {
			t.Errorf("plant %d missing from paginated listing", want)
		}
	}
}
