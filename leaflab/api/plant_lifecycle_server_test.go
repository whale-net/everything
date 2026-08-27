package main

// Plant lifecycle RPC-handler tests (#1221, FR54, FR24, FR22.3, FR22.5, FR1.2).
//
// These exercise CreatePlant / CorrectPlant / MovePlant / RetirePlant /
// GetPlantPlacementTimeline against a fully migrated database. Handlers are
// invoked as direct Go method calls (server.CreatePlant(ctx, req), not over
// a dialed gRPC connection): AuthModeNone's server interceptor unconditionally
// overwrites incoming claims with a fixed "dev-user" identity (see
// grpcauth.authenticate), so a claims-carrying context set by a bufconn
// client never reaches the handler as anything other than dev-user. Calling
// the handler method directly applies grpcauth.ContextWithClaims exactly as
// the interceptor would, and is what actually lets each subtest authenticate
// as a distinct principal.
//
// Reuses getMigrationsPath/runMigrationsManually/timescaleDBImage from
// server_test.go (same package).

import (
	"context"
	"testing"
	"time"

	apierrors "github.com/whale-net/everything/leaflab/api/apierrors"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// plantLifecycleFixture holds a live server + repo wired to a fully migrated,
// isolated database.
type plantLifecycleFixture struct {
	server *LeafLabAPIServer
	repo   *Repository
	db     *dbtest.Postgres
}

// newPlantLifecycleFixture provisions a freshly migrated database and a
// LeafLabAPIServer wired to it.
func newPlantLifecycleFixture(ctx context.Context, t *testing.T) *plantLifecycleFixture {
	t.Helper()

	dbContainer := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Image: timescaleDBImage,
	})
	runMigrationsManually(ctx, t, dbContainer.Pool, getMigrationsPath(t))

	repo := NewRepository(dbContainer.Pool)
	server := &LeafLabAPIServer{
		repo:   repo,
		logger: logging.Get("api-test"),
	}

	return &plantLifecycleFixture{server: server, repo: repo, db: dbContainer}
}

// ctxAs returns a context carrying claims for the given principal, exactly
// as grpcauth's server interceptor would inject them.
func ctxAs(principal string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: principal, Roles: []string{}})
}

// createHousehold inserts a household and returns its id.
func (f *plantLifecycleFixture) createHousehold(ctx context.Context, t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ($1) RETURNING household_id
	`, name).Scan(&id); err != nil {
		t.Fatalf("insert household %s: %v", name, err)
	}
	return id
}

// addMember adds principal as a member of householdID with the given role
// (e.g. "Owner" for a member, "Helper" for a grantee) — per #1191's
// authorization model, any active household_member row grants the same
// household-scoped reach regardless of role.
func (f *plantLifecycleFixture) addMember(ctx context.Context, t *testing.T, householdID int64, principal, role string) {
	t.Helper()
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO household_member (household_id, principal_id, role, valid_from)
		VALUES ($1, $2, $3, NOW())
	`, householdID, principal, role); err != nil {
		t.Fatalf("add member %s to household %d: %v", principal, householdID, err)
	}
}

// createRootRegion inserts a root region (parent_region_id IS NULL) owned by
// householdID and returns its id.
func (f *plantLifecycleFixture) createRootRegion(ctx context.Context, t *testing.T, householdID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO region (name, household_id) VALUES ($1, $2) RETURNING region_id
	`, name, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

// createPlantType inserts a plant_type row and returns its id.
func (f *plantLifecycleFixture) createPlantType(ctx context.Context, t *testing.T, commonName string) int64 {
	t.Helper()
	var id int64
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO plant_type (common_name) VALUES ($1) RETURNING plant_type_id
	`, commonName).Scan(&id); err != nil {
		t.Fatalf("insert plant_type %s: %v", commonName, err)
	}
	return id
}

// TestPlantLifecycle_CreatePlaceCorrectMoveRetireTimeline_AsMemberAndGrantee
// walks the full FR54/FR24/FR22.3/FR22.5 lifecycle — create+place, correct,
// move, retire, then read the timeline of a retired plant — once as a
// household Owner (member) and once as a Helper (grantee), per the issue's
// persona trace (GR-5 through GR-10).
func TestPlantLifecycle_CreatePlaceCorrectMoveRetireTimeline_AsMemberAndGrantee(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	for _, tc := range []struct {
		name      string
		role      string
		principal string
	}{
		{name: "as member (Owner)", role: "Owner", principal: "owner-1"},
		{name: "as grantee (Helper)", role: "Helper", principal: "helper-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			f := newPlantLifecycleFixture(ctx, t)
			hid := f.createHousehold(ctx, t, "Household "+tc.name)
			f.addMember(ctx, t, hid, tc.principal, tc.role)
			region1 := f.createRootRegion(ctx, t, hid, "Region 1")
			region2 := f.createRootRegion(ctx, t, hid, "Region 2")
			plantTypeID := f.createPlantType(ctx, t, "Ficus")

			callerCtx := ctxAs(tc.principal)

			// Create + place (FR54: acquire-and-place in one operation).
			createResp, err := f.server.CreatePlant(callerCtx, &pb.CreatePlantRequest{
				Name:        "Fred",
				PlantTypeId: plantTypeID,
				RegionId:    region1,
			})
			if err != nil {
				t.Fatalf("CreatePlant: %v", err)
			}
			plantID := createResp.PlantId
			if plantID == 0 {
				t.Fatal("expected non-zero plant_id")
			}

			timeline, err := f.server.GetPlantPlacementTimeline(callerCtx, &pb.GetPlantPlacementTimelineRequest{PlantId: plantID})
			if err != nil {
				t.Fatalf("GetPlantPlacementTimeline after create: %v", err)
			}
			if len(timeline.Placements) != 1 || timeline.Placements[0].RegionId != region1 || timeline.Placements[0].ValidTo != 0 {
				t.Fatalf("expected one open placement in region %d, got %+v", region1, timeline.Placements)
			}

			// Correct (FR24: distinct from move — does not open a placement interval).
			if _, err := f.server.CorrectPlant(callerCtx, &pb.CorrectPlantRequest{
				PlantId:       plantID,
				CorrectedName: "Freddy",
			}); err != nil {
				t.Fatalf("CorrectPlant: %v", err)
			}
			timeline, err = f.server.GetPlantPlacementTimeline(callerCtx, &pb.GetPlantPlacementTimelineRequest{PlantId: plantID})
			if err != nil {
				t.Fatalf("GetPlantPlacementTimeline after correct: %v", err)
			}
			if len(timeline.Placements) != 1 {
				t.Fatalf("FR24: correcting a plant must not open a placement interval, got %d entries", len(timeline.Placements))
			}

			// Move (FR54/FR24: distinct from correct — closes current, opens new).
			moveResp, err := f.server.MovePlant(callerCtx, &pb.MovePlantRequest{
				PlantId:     plantID,
				NewRegionId: region2,
			})
			if err != nil {
				t.Fatalf("MovePlant: %v", err)
			}
			if moveResp.NewRegionId != region2 {
				t.Errorf("expected MovePlantResponse.new_region_id %d, got %d", region2, moveResp.NewRegionId)
			}
			timeline, err = f.server.GetPlantPlacementTimeline(callerCtx, &pb.GetPlantPlacementTimelineRequest{PlantId: plantID})
			if err != nil {
				t.Fatalf("GetPlantPlacementTimeline after move: %v", err)
			}
			if len(timeline.Placements) != 2 {
				t.Fatalf("expected two placement intervals after one move (nothing updated in place), got %d", len(timeline.Placements))
			}
			if timeline.Placements[0].ValidTo == 0 {
				t.Error("expected the first (pre-move) interval to be closed")
			}
			if timeline.Placements[1].ValidTo != 0 || timeline.Placements[1].RegionId != region2 {
				t.Errorf("expected an open interval in region %d, got %+v", region2, timeline.Placements[1])
			}
			if timeline.Placements[1].RelocationInduced {
				t.Error("FR24: an API-driven MovePlant call must not be marked relocation_induced")
			}

			// Retire (FR22.5: names the operation and the acting principal).
			retireResp, err := f.server.RetirePlant(callerCtx, &pb.RetirePlantRequest{PlantId: plantID})
			if err != nil {
				t.Fatalf("RetirePlant: %v", err)
			}
			if retireResp.RetiredOperation != "retire_plant" {
				t.Errorf("expected retired_operation %q, got %q", "retire_plant", retireResp.RetiredOperation)
			}
			if retireResp.RetiredPrincipal != tc.principal {
				t.Errorf("FR8: expected retired_principal %q, got %q", tc.principal, retireResp.RetiredPrincipal)
			}
			if retireResp.RetiredAt == 0 {
				t.Error("expected a non-zero retired_at")
			}

			// FR22.3: the timeline remains readable after retirement, and carries
			// the retirement metadata.
			timeline, err = f.server.GetPlantPlacementTimeline(callerCtx, &pb.GetPlantPlacementTimelineRequest{PlantId: plantID})
			if err != nil {
				t.Fatalf("GetPlantPlacementTimeline after retirement (FR22.3): %v", err)
			}
			if timeline.RetiredAt == 0 || timeline.RetiredOperation != "retire_plant" || timeline.RetiredPrincipal != tc.principal {
				t.Errorf("expected timeline to carry retirement metadata, got %+v", timeline)
			}
			if len(timeline.Placements) != 2 || timeline.Placements[1].ValidTo == 0 {
				t.Errorf("expected retirement to close the final placement interval, got %+v", timeline.Placements)
			}

			// FR22.5: a retired plant accepts no new writes.
			if _, err := f.server.CorrectPlant(callerCtx, &pb.CorrectPlantRequest{PlantId: plantID, CorrectedName: "Nope"}); err == nil {
				t.Error("expected CorrectPlant on a retired plant to be refused")
			} else if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition {
				t.Errorf("expected FailedPrecondition, got %v", st.Code())
			} else if detail := apierrors.ErrorDetailFromStatus(st); detail == nil || detail.MessageKey != apierrors.PlantRetired {
				t.Errorf("expected message_key %q, got %+v", apierrors.PlantRetired, detail)
			}
			if _, err := f.server.MovePlant(callerCtx, &pb.MovePlantRequest{PlantId: plantID, NewRegionId: region1}); err == nil {
				t.Error("expected MovePlant on a retired plant to be refused")
			} else if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition {
				t.Errorf("expected FailedPrecondition, got %v", st.Code())
			}

			// FR22.5: excluded from default listings — the guard's query shape
			// (idx_plant_active) no longer includes this plant.
			var stillActiveCount int
			if err := f.db.Pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM plant WHERE plant_id = $1 AND removed_at IS NULL
			`, plantID).Scan(&stillActiveCount); err != nil {
				t.Fatalf("check active listing: %v", err)
			}
			if stillActiveCount != 0 {
				t.Error("FR22.5: a retired plant must be excluded from the active-listing guard")
			}

			// Nothing is hard-deleted (FR22.1).
			var plantRowCount int
			if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant WHERE plant_id = $1`, plantID).Scan(&plantRowCount); err != nil {
				t.Fatalf("count plant rows: %v", err)
			}
			if plantRowCount != 1 {
				t.Errorf("FR22.1: expected the plant row to still exist, got count %d", plantRowCount)
			}
		})
	}
}

// TestCreatePlant_RegionInAnotherHousehold_Refused verifies FR1.2: a
// CreatePlant call naming a region in another household is refused as
// not-found (NFR2 — no existence oracle).
func TestCreatePlant_RegionInAnotherHousehold_Refused(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := newPlantLifecycleFixture(ctx, t)
	hid1 := f.createHousehold(ctx, t, "Household 1")
	hid2 := f.createHousehold(ctx, t, "Household 2")
	f.addMember(ctx, t, hid1, "principal-1", "Owner")
	region2 := f.createRootRegion(ctx, t, hid2, "Region in household 2")
	plantTypeID := f.createPlantType(ctx, t, "Ficus")

	_, err := f.server.CreatePlant(ctxAs("principal-1"), &pb.CreatePlantRequest{
		Name:        "Fred",
		PlantTypeId: plantTypeID,
		RegionId:    region2,
	})
	if err == nil {
		t.Fatal("FR1.2: expected placing a plant in another household's region to be refused")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Errorf("NFR2: expected codes.NotFound, got %v", err)
	}
}

// TestMovePlant_RegionInAnotherHousehold_Refused verifies FR1.2: moving a
// plant into a region outside its household is refused.
func TestMovePlant_RegionInAnotherHousehold_Refused(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := newPlantLifecycleFixture(ctx, t)
	hid1 := f.createHousehold(ctx, t, "Household 1")
	hid2 := f.createHousehold(ctx, t, "Household 2")
	f.addMember(ctx, t, hid1, "principal-1", "Owner")
	region1 := f.createRootRegion(ctx, t, hid1, "Region 1")
	region2 := f.createRootRegion(ctx, t, hid2, "Region in household 2")
	plantTypeID := f.createPlantType(ctx, t, "Ficus")

	callerCtx := ctxAs("principal-1")
	createResp, err := f.server.CreatePlant(callerCtx, &pb.CreatePlantRequest{
		Name:        "Fred",
		PlantTypeId: plantTypeID,
		RegionId:    region1,
	})
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}

	_, err = f.server.MovePlant(callerCtx, &pb.MovePlantRequest{
		PlantId:     createResp.PlantId,
		NewRegionId: region2,
	})
	if err == nil {
		t.Fatal("FR1.2: expected moving a plant into another household's region to be refused")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Errorf("expected codes.FailedPrecondition, got %v", err)
	}

	// Verify the plant did not move.
	timeline, err := f.server.GetPlantPlacementTimeline(callerCtx, &pb.GetPlantPlacementTimelineRequest{PlantId: createResp.PlantId})
	if err != nil {
		t.Fatalf("GetPlantPlacementTimeline: %v", err)
	}
	if len(timeline.Placements) != 1 || timeline.Placements[0].RegionId != region1 {
		t.Errorf("expected the refused move to leave the plant in region %d, got %+v", region1, timeline.Placements)
	}
}

// TestCorrectPlant_AndMovePlant_NameDistinctOperationsInAudit verifies FR24:
// the API states which operation was performed — correct and move are
// recorded as distinctly named operations in the audit trail (FR8), not a
// single generic "update".
func TestCorrectPlant_AndMovePlant_NameDistinctOperationsInAudit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := newPlantLifecycleFixture(ctx, t)
	hid := f.createHousehold(ctx, t, "Household 1")
	f.addMember(ctx, t, hid, "principal-1", "Owner")
	region1 := f.createRootRegion(ctx, t, hid, "Region 1")
	region2 := f.createRootRegion(ctx, t, hid, "Region 2")
	plantTypeID := f.createPlantType(ctx, t, "Ficus")

	callerCtx := ctxAs("principal-1")
	createResp, err := f.server.CreatePlant(callerCtx, &pb.CreatePlantRequest{
		Name:        "Fred",
		PlantTypeId: plantTypeID,
		RegionId:    region1,
	})
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	plantID := createResp.PlantId

	if _, err := f.server.CorrectPlant(callerCtx, &pb.CorrectPlantRequest{PlantId: plantID, CorrectedName: "Freddy"}); err != nil {
		t.Fatalf("CorrectPlant: %v", err)
	}
	if _, err := f.server.MovePlant(callerCtx, &pb.MovePlantRequest{PlantId: plantID, NewRegionId: region2}); err != nil {
		t.Fatalf("MovePlant: %v", err)
	}

	var correctCount, moveCount int
	if err := f.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_record WHERE entity_type = 'plant' AND entity_id = $1 AND action = 'correct_plant'
	`, plantID).Scan(&correctCount); err != nil {
		t.Fatalf("count correct_plant audit rows: %v", err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_record WHERE entity_type = 'plant' AND entity_id = $1 AND action = 'move_plant'
	`, plantID).Scan(&moveCount); err != nil {
		t.Fatalf("count move_plant audit rows: %v", err)
	}
	if correctCount != 1 {
		t.Errorf("FR24/FR8: expected one correct_plant audit record, got %d", correctCount)
	}
	if moveCount != 1 {
		t.Errorf("FR24/FR8: expected one move_plant audit record, got %d", moveCount)
	}
}

// TestRetirePlant_ReadingsRemainReachable verifies FR22.3: a retired plant's
// readings remain reachable for a postmortem — retirement closes the
// plant's placement interval going forward, but does not erase or hide the
// historical attribution of readings recorded while it was active.
func TestRetirePlant_ReadingsRemainReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := newPlantLifecycleFixture(ctx, t)
	hid := f.createHousehold(ctx, t, "Household 1")
	f.addMember(ctx, t, hid, "principal-1", "Owner")
	region1 := f.createRootRegion(ctx, t, hid, "Region 1")
	plantTypeID := f.createPlantType(ctx, t, "Ficus")

	callerCtx := ctxAs("principal-1")
	createResp, err := f.server.CreatePlant(callerCtx, &pb.CreatePlantRequest{
		Name:        "Fred",
		PlantTypeId: plantTypeID,
		RegionId:    region1,
	})
	if err != nil {
		t.Fatalf("CreatePlant: %v", err)
	}
	plantID := createResp.PlantId

	// Record a reading in region1 while the plant is active. A board+sensor
	// is required by sensor_reading's foreign keys.
	var boardID, sensorID int64
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO board (device_id, registered_at, last_seen_at) VALUES ('board-1', NOW(), NOW())
		RETURNING board_id
	`).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit)
		VALUES ($1, (SELECT sensor_type_id FROM sensor_type WHERE name = 'temperature'), $2, 'sensor-1', 'degC')
		RETURNING sensor_id
	`, boardID, region1).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	readingTime := time.Now()
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO sensor_reading (sensor_id, region_id, value, uptime_s, recorded_at)
		VALUES ($1, $2, 21.5, 100, $3)
	`, sensorID, region1, readingTime); err != nil {
		t.Fatalf("insert reading: %v", err)
	}

	if _, err := f.server.RetirePlant(callerCtx, &pb.RetirePlantRequest{PlantId: plantID}); err != nil {
		t.Fatalf("RetirePlant: %v", err)
	}

	// The reading, taken while the plant was active, still resolves back to
	// the (now retired) plant via plant_region_history — retirement closes
	// the interval going forward, it does not erase the historical window.
	result, err := f.repo.GetActivePlantsInRegionAtTime(ctx, region1, readingTime)
	if err != nil {
		t.Fatalf("GetActivePlantsInRegionAtTime: %v", err)
	}
	found := false
	for _, p := range result {
		if p.PlantID == plantID {
			found = true
		}
	}
	if !found {
		t.Errorf("FR22.3: expected the retired plant to still resolve for a reading taken during its occupancy, got %+v", result)
	}

	// The plant itself remains readable by explicit id, including through the
	// history path.
	timeline, err := f.server.GetPlantPlacementTimeline(callerCtx, &pb.GetPlantPlacementTimelineRequest{PlantId: plantID})
	if err != nil {
		t.Fatalf("FR22.3: expected the retired plant's timeline to remain readable, got error: %v", err)
	}
	if timeline.RetiredAt == 0 {
		t.Error("expected retirement metadata on the timeline response")
	}

	// The reading row itself is untouched (FR22.1: nothing hard-deleted).
	var readingCount int
	if err := f.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM sensor_reading WHERE sensor_id = $1`, sensorID).Scan(&readingCount); err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if readingCount != 1 {
		t.Errorf("expected the reading to remain, got count %d", readingCount)
	}
}
