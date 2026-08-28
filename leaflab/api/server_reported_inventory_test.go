package main

import (
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// This file covers FR49's read path (GetReportedInventory/GetConfigDrift,
// reported_inventory.go) and the "a report, never a source" invariant at
// the Go-dispatch level (fakeRepo/fakeAuthz, no Docker/network) -- the
// same style server_diff_config_versions_test.go uses for FR37.
//
// leaflab/conformance/manifest_never_source_test.go already proves this
// structurally (leaflab/api/config can't even reach manifest data); the
// behavioural test below (
// TestPushDeviceConfig_Edit_ReportedInventoryNeverContributesToMaterialisation)
// proves the same invariant one layer up, wired through this binary's own
// GetReportedInventory-backed fakeRepo fields (getReportedInventoryFound/
// -Entries), rather than through LoadBoardSensorIdentities the way
// server_push_device_config_scope_test.go's older
// TestPushDeviceConfig_Edit_MaterialisationBase_NeverTheStaleManifest does.

const reportedInventoryTestBoardID = int64(66)

var reportedInventoryTestBoardRef = authz.EntityRef{Kind: authz.EntityBoard, ID: reportedInventoryTestBoardID}

// newReportedInventoryTestServer wires a server whose caller is authorized
// for reportedInventoryTestBoardRef -- the household check FR49's RPCs
// share with GetDeviceConfig (authorizeBoardAccess) isn't what these tests
// are about.
func newReportedInventoryTestServer(repo *fakeRepo) *LeafLabAPIServer {
	authzSvc := &fakeAuthz{
		scope:      allPermittingScope{},
		resolveRef: reportedInventoryTestBoardRef,
		resolveRes: authz.Resolution{HouseholdID: 1},
	}
	return NewLeafLabAPIServer(repo, authzSvc, nil, nil, discardLogger(), defaultPollIntervalBounds)
}

// reportedRow builds a ReportedInventoryRow for a directly-addressed
// (empty mux_path) sensor -- the shape GetReportedInventory's repository
// method returns, keyed the same way config.CanonicalKey keys a pushed
// config entry (FR18), so a test can construct a reported-inventory
// fixture that either matches or diverges from a desired-state entry
// built with sensorAt/resolveSensorTypeIDResponses.
func reportedRow(i2c uint32, sensorTypeID int64, typeName, name string) ReportedInventoryRow {
	return ReportedInventoryRow{
		Key:            hwkey.Key{I2CAddress: hwkey.Address(uint16(i2c)), SensorTypeID: hwkey.SensorTypeID(sensorTypeID)},
		SensorTypeName: typeName,
		Name:           name,
		Unit:           "lux",
		ChipModel:      "BH1750",
	}
}

// -- GetReportedInventory --------------------------------------------------

// TestGetReportedInventory_ReturnsLastManifestWithReportedAt proves the
// happy path: found=true, the manifest's entries, and the reported_at
// instant it carried, all pass through unchanged.
func TestGetReportedInventory_ReturnsLastManifestWithReportedAt(t *testing.T) {
	reportedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &fakeRepo{
		getReportedInventoryFound:   true,
		getReportedInventoryEntries: []ReportedInventoryRow{reportedRow(0x23, 1, "illuminance", "light")},
		getReportedInventoryAt:      reportedAt,
	}
	server := newReportedInventoryTestServer(repo)

	resp, err := server.GetReportedInventory(authedTestCtx("alice"), &pb.GetReportedInventoryRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Found {
		t.Fatal("Found = false, want true")
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(resp.Entries))
	}
	got := resp.Entries[0]
	if got.Name != "light" || got.I2CAddress != 0x23 || got.SensorType != firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE {
		t.Errorf("Entries[0] = %+v, want name=light i2c=0x23 type=ILLUMINANCE", got)
	}
	wantInstant := contract.ToInstant(reportedAt)
	if resp.ReportedAt == nil || resp.ReportedAt.UnixMillis != wantInstant.UnixMillis {
		t.Errorf("ReportedAt = %v, want %v", resp.ReportedAt, wantInstant)
	}
}

// TestGetReportedInventory_NeverReported_FoundFalseNotEmptyInventory proves
// FR49's documented distinction: a board that has never sent a manifest
// reports found=false, not a false "empty inventory" that would be
// indistinguishable from a manifest that reported zero sensors.
func TestGetReportedInventory_NeverReported_FoundFalseNotEmptyInventory(t *testing.T) {
	server := newReportedInventoryTestServer(&fakeRepo{ /* getReportedInventoryFound defaults to false */ })

	resp, err := server.GetReportedInventory(authedTestCtx("alice"), &pb.GetReportedInventoryRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Found {
		t.Fatal("Found = true, want false for a board that has never sent a manifest")
	}
	if len(resp.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0", len(resp.Entries))
	}
	if resp.ReportedAt != nil {
		t.Errorf("ReportedAt = %v, want nil when found is false", resp.ReportedAt)
	}
}

// -- GetConfigDrift ----------------------------------------------------------

// TestGetConfigDrift_InDesiredNotReported_FailedInstantiation is FR49's
// core drift case: the accepted config names two entries, but the board's
// reported manifest -- a strict subset -- only instantiated one of them
// (e.g. an unknown chip type, invalid address, unresolvable bus endpoint,
// or exhausted pool silently dropped the other in ApplyFactory()). The
// entry missing from the reported inventory must classify as
// IN_DESIRED_NOT_REPORTED, not simply be absent from the drift response.
func TestGetConfigDrift_InDesiredNotReported_FailedInstantiation(t *testing.T) {
	reportedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{
		resolveSensorTypeIDResponses: map[string]int64{"illuminance": 1, "temperature": 2},
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{
				sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
				sensorAt("temp", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE), // board failed to instantiate this one
			},
		},
		// Strict subset of the desired state above: "temp" never made it
		// into the board's last reported manifest.
		getReportedInventoryFound:   true,
		getReportedInventoryEntries: []ReportedInventoryRow{reportedRow(0x23, 1, "illuminance", "light")},
		getReportedInventoryAt:      reportedAt,
	}
	server := newReportedInventoryTestServer(repo)

	resp, err := server.GetConfigDrift(authedTestCtx("alice"), &pb.GetConfigDriftRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2 (one matched, one in_desired_not_reported): %+v", len(resp.Entries), resp.Entries)
	}
	byAddr := map[uint32]pb.DriftClassification{}
	for _, e := range resp.Entries {
		byAddr[e.I2CAddress] = e.Classification
	}
	if byAddr[0x23] != pb.DriftClassification_DRIFT_CLASSIFICATION_MATCHED {
		t.Errorf("0x23 classification = %v, want MATCHED", byAddr[0x23])
	}
	if byAddr[0x44] != pb.DriftClassification_DRIFT_CLASSIFICATION_IN_DESIRED_NOT_REPORTED {
		t.Errorf("0x44 classification = %v, want IN_DESIRED_NOT_REPORTED -- this is the failed-instantiation case FR49 exists to surface", byAddr[0x44])
	}
	wantInstant := contract.ToInstant(reportedAt)
	if resp.ReportedAt == nil || resp.ReportedAt.UnixMillis != wantInstant.UnixMillis {
		t.Errorf("ReportedAt = %v, want %v", resp.ReportedAt, wantInstant)
	}
}

// TestGetConfigDrift_ReportedNotInDesired_ReportedLoudly proves the
// "should be impossible under replace semantics" case is surfaced as its
// own classification rather than silently dropped from the response.
func TestGetConfigDrift_ReportedNotInDesired_ReportedLoudly(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses: map[string]int64{"illuminance": 1},
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{
				sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			},
		},
		// The board reports an entry the desired state doesn't name at
		// all -- out of sync with replace semantics (FR82).
		getReportedInventoryFound: true,
		getReportedInventoryEntries: []ReportedInventoryRow{
			reportedRow(0x23, 1, "illuminance", "light"),
			reportedRow(0x99, 1, "illuminance", "ghost"),
		},
	}
	server := newReportedInventoryTestServer(repo)

	resp, err := server.GetConfigDrift(authedTestCtx("alice"), &pb.GetConfigDriftRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byAddr := map[uint32]pb.DriftClassification{}
	for _, e := range resp.Entries {
		byAddr[e.I2CAddress] = e.Classification
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2: %+v -- reported_not_in_desired must be reported, not swallowed", len(resp.Entries), resp.Entries)
	}
	if byAddr[0x99] != pb.DriftClassification_DRIFT_CLASSIFICATION_REPORTED_NOT_IN_DESIRED {
		t.Errorf("0x99 classification = %v, want REPORTED_NOT_IN_DESIRED", byAddr[0x99])
	}
	if byAddr[0x23] != pb.DriftClassification_DRIFT_CLASSIFICATION_MATCHED {
		t.Errorf("0x23 classification = %v, want MATCHED", byAddr[0x23])
	}
}

// TestGetConfigDrift_NeverReported_EveryDesiredEntryInDesiredNotReported
// covers the boundary named in api.proto's GetConfigDriftResponse doc
// comment: a board that has never sent a manifest classifies every
// desired-state entry as IN_DESIRED_NOT_REPORTED, with no reported_at at
// all.
func TestGetConfigDrift_NeverReported_EveryDesiredEntryInDesiredNotReported(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses: map[string]int64{"illuminance": 1},
		getLatestAcceptedConfigResponse: &configpb.DeviceConfig{
			Sensors: []*configpb.SensorConfig{
				sensorAt("light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			},
		},
		// getReportedInventoryFound defaults to false: no manifest ever received.
	}
	server := newReportedInventoryTestServer(repo)

	resp, err := server.GetConfigDrift(authedTestCtx("alice"), &pb.GetConfigDriftRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Classification != pb.DriftClassification_DRIFT_CLASSIFICATION_IN_DESIRED_NOT_REPORTED {
		t.Fatalf("Entries = %+v, want exactly one IN_DESIRED_NOT_REPORTED entry", resp.Entries)
	}
	if resp.ReportedAt != nil {
		t.Errorf("ReportedAt = %v, want nil when the board has never reported", resp.ReportedAt)
	}
}

// -- FR49 x FR82.3: the reported manifest never contributes to materialisation --

// TestPushDeviceConfig_Edit_ReportedInventoryNeverContributesToMaterialisation
// is this task's core regression coverage: fakeRepo's reported-inventory
// fields (getReportedInventoryFound/-Entries) are configured with a
// manifest that both disagrees with the accepted config (a different name
// at the same hardware key) and, more importantly, is missing an entry
// the accepted config still names -- simulating an entry the board failed
// to instantiate (FR49's lossy echo). An EDIT push touching neither entry
// must still carry both forward from the accepted config, verbatim,
// proving:
//   - the reported manifest contributes nothing to the materialised
//     payload (if it did, "accepted-light" would read back as
//     "manifest-light"), and
//   - the entry missing from the manifest is NOT silently deleted just
//     because the board never confirmed it (the regression this task
//     guards against: FR49 exists precisely so a failed-instantiation
//     entry survives being carried forward by an EDIT push instead of
//     being dropped).
//
// PushDeviceConfig itself never even calls GetReportedInventory (see
// server.go's deviceRepository interface and config_push.go) -- this test
// proves that from the outside, by configuring reported-inventory data
// that would visibly leak into the result if it were consulted.
func TestPushDeviceConfig_Edit_ReportedInventoryNeverContributesToMaterialisation(t *testing.T) {
	repo := &fakeRepo{
		resolveSensorTypeIDResponses:     map[string]int64{"illuminance": 1, "temperature": 2},
		insertDeviceConfigNextVersionErr: errInjectedStorageFailure,
	}
	repo.getOrCreateBoardID = scopeTestBoardID
	repo.getLatestAcceptedConfigResponse = &configpb.DeviceConfig{
		Sensors: []*configpb.SensorConfig{
			sensorAt("accepted-light", 0x23, firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE),
			// "accepted-temp" is the entry the board failed to instantiate --
			// present in the accepted config, absent from what it reported.
			sensorAt("accepted-temp", 0x44, firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE),
		},
	}
	// The board's last reported manifest: disagrees on "light"'s name, and
	// is missing "accepted-temp" entirely (the failed-instantiation case).
	repo.getReportedInventoryFound = true
	repo.getReportedInventoryEntries = []ReportedInventoryRow{
		reportedRow(0x23, 1, "illuminance", "manifest-light"),
	}

	server, _ := newScopeTestServer(repo)

	_, err := server.PushDeviceConfig(authedTestCtx("alice"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Scope:    pb.PushScope_PUSH_SCOPE_EDIT,
		Sensors:  nil, // no adds/changes -- everything must carry forward from the accepted config alone
	})
	assertReachedStorage(t, err)

	entries := repo.insertDeviceConfigNextVersionCalls[0].entries
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 -- the accepted-temp entry must survive being carried forward, not be silently deleted just because the board never confirmed it (FR49 regression): %+v", len(entries), entries)
	}
	byName := map[string]string{}
	for _, e := range entries {
		byName[e.Sensor.Name] = e.Sensor.Name
	}
	if _, ok := byName["accepted-light"]; !ok {
		t.Errorf("materialised entries = %+v, want 'accepted-light' -- the reported manifest's disagreeing name ('manifest-light') must never leak into materialisation", entries)
	}
	if _, ok := byName["accepted-temp"]; !ok {
		t.Errorf("materialised entries = %+v, want 'accepted-temp' still present -- an entry missing from the reported inventory must not be dropped by an EDIT push", entries)
	}
}
