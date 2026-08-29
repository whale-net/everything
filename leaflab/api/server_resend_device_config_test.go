package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"google.golang.org/protobuf/proto"
)

// -- FR42: GetResendAvailability/ResendDeviceConfig ("the one safe button") -
//
// Unit coverage against fakeRepo/fakeAuthz/fakePublisher, proving: a
// re-send publishes the accepted payload byte-identically to the retained
// topic without ever inserting a device_config row or assigning a version;
// availability (NOT_REPORTING via the shared A23 function, NOTHING_TO_RESEND
// for both its named sub-conditions, AVAILABLE otherwise) is computed by
// resendAvailability and never diverges between GetResendAvailability's
// answer and ResendDeviceConfig's own refusal; the write is audited despite
// writing no config row; and NFR2's board-scoped not-found applies to both
// RPCs. Rate limiting itself (the `resend` bucket) is covered at the
// interceptor level in ratelimit_interceptor_test.go, since ResendDeviceConfig
// the handler has no rate-limit logic of its own -- the interceptor chain
// enforces it before the handler ever runs.

// fakePublisher implements configPublisher entirely in memory, recording
// every PublishRetained call's exchange/routingKey/body -- so a test can
// assert FR42.1's "byte-identical to the retained topic" and "pressing
// twice publishes twice" without a live RabbitMQ connection. Publish
// (the live, non-retained push PushDeviceConfig uses) is recorded
// separately so a test can assert ResendDeviceConfig never calls it by
// mistake -- a re-send that accidentally used the live-push path would
// still "work" but would not be FR42.1's retained re-send.
type fakePublisher struct {
	retainedCalls []publishCall
	publishCalls  []publishCall
	publishErr    error
	retainedErr   error
}

type publishCall struct {
	exchange   string
	routingKey string
	body       []byte
}

func (f *fakePublisher) Publish(ctx context.Context, exchange, routingKey string, body interface{}) error {
	f.publishCalls = append(f.publishCalls, publishCall{exchange, routingKey, bodyBytes(body)})
	return f.publishErr
}

func (f *fakePublisher) PublishRetained(ctx context.Context, exchange, routingKey string, body interface{}) error {
	f.retainedCalls = append(f.retainedCalls, publishCall{exchange, routingKey, bodyBytes(body)})
	return f.retainedErr
}

func bodyBytes(body interface{}) []byte {
	b, ok := body.([]byte)
	if !ok {
		panic(fmt.Sprintf("fakePublisher: body is %T, want []byte (ResendDeviceConfig/PushDeviceConfig always pass proto.Marshal's output)", body))
	}
	return b
}

// reportingRow returns a FleetBoardHealthRow that reportingStateFor
// classifies REPORTING as of now -- a healthy, non-stale board -- so
// resendAvailability's NOT_REPORTING branch is never what's under test
// unless a test deliberately wants it.
func reportingRow(t *testing.T, now time.Time) FleetBoardHealthRow {
	t.Helper()
	return FleetBoardHealthRow{
		LastSeenAt:         now,
		AcceptedConfigJSON: fleetConfigJSON(t, 60000),
	}
}

// notReportingRow returns a FleetBoardHealthRow past A23's threshold for a
// 1-minute poll interval (the same floor/multiplier fixture
// TestReportingStateFor_OneMinuteInterval_FloorNotMultiplier uses) --
// reportingStateFor(notReportingRow(t, now), now) == NOT_REPORTING.
func notReportingRow(t *testing.T, now time.Time) FleetBoardHealthRow {
	t.Helper()
	return FleetBoardHealthRow{
		LastSeenAt:         now.Add(-16 * time.Minute),
		AcceptedConfigJSON: fleetConfigJSON(t, 60000),
	}
}

// resendTestServer wires repo/authzSvc/publisher into a server the same way
// every test below does: a claimed board, permitting scope, so resolution
// succeeds and the test's own repo/publisher configuration is what
// determines the outcome.
func resendTestServer(repo *fakeRepo, publisher *fakePublisher) (*LeafLabAPIServer, *fakeAuthz) {
	authzSvc := &fakeAuthz{
		scope:      allPermittingScope{},
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: 1},
		resolveRes: authz.Resolution{HouseholdID: 42},
	}
	return NewLeafLabAPIServer(repo, authzSvc, publisher, nil, discardLogger()), authzSvc
}

// acceptedConfig returns a fixture *configpb.DeviceConfig standing in for
// GetLatestAcceptedConfig's return -- ResendDeviceConfig marshals exactly
// this value and republishes it unchanged (FR42.1).
func acceptedConfig(version uint64) *configpb.DeviceConfig {
	return &configpb.DeviceConfig{
		Version: version,
		Sensors: []*configpb.SensorConfig{{Name: "soil-1", PollIntervalMs: 60000}},
	}
}

// -- Publish is byte-identical, writes nothing, and is idempotent -----------

// TestResendDeviceConfig_PublishesAcceptedPayloadByteIdentical_NoRowInsertedNoVersionAssigned
// is FR42.1's core assertion: the wire body PublishRetained receives is
// exactly proto.Marshal of the accepted config GetLatestAcceptedConfig
// returned -- not re-derived, not re-encoded -- and InsertDeviceConfigNextVersion
// is never called (no device_config row, no version assigned). The returned
// version is the accepted config's own version, echoed back unchanged.
func TestResendDeviceConfig_PublishesAcceptedPayloadByteIdentical_NoRowInsertedNoVersionAssigned(t *testing.T) {
	now := time.Now()
	cfg := acceptedConfig(5)
	repo := &fakeRepo{
		getBoardReportingHealthRow:      reportingRow(t, now),
		getBoardReportingHealthOk:       true,
		getLatestAcceptedConfigResponse: cfg,
		listConfigHistoryResponse:       []DeviceConfigHistoryRow{{Version: 5, Accepted: true, AckedAt: &now}},
	}
	publisher := &fakePublisher{}
	server, _ := resendTestServer(repo, publisher)

	resp, err := server.ResendDeviceConfig(authedTestCtx("alice"), &pb.ResendDeviceConfigRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("ResendDeviceConfig: %v", err)
	}
	if resp.Version != 5 {
		t.Errorf("Version = %d, want 5 (the accepted config's own version, echoed back unchanged)", resp.Version)
	}

	if len(publisher.retainedCalls) != 1 {
		t.Fatalf("PublishRetained calls = %d, want 1", len(publisher.retainedCalls))
	}
	if len(publisher.publishCalls) != 0 {
		t.Errorf("Publish (live, non-retained) calls = %d, want 0 -- a re-send must use PublishRetained only", len(publisher.publishCalls))
	}
	call := publisher.retainedCalls[0]
	if call.exchange != mqttExchange {
		t.Errorf("exchange = %q, want %q", call.exchange, mqttExchange)
	}
	wantRoutingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll("device-a", "/", "."))
	if call.routingKey != wantRoutingKey {
		t.Errorf("routingKey = %q, want %q", call.routingKey, wantRoutingKey)
	}
	wantWire, marshalErr := proto.Marshal(cfg)
	if marshalErr != nil {
		t.Fatalf("marshal fixture config: %v", marshalErr)
	}
	if string(call.body) != string(wantWire) {
		t.Errorf("published body differs from proto.Marshal(accepted config) -- not byte-identical\ngot:  %x\nwant: %x", call.body, wantWire)
	}

	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion calls = %d, want 0 -- a re-send must insert no device_config row and assign no version", len(repo.insertDeviceConfigNextVersionCalls))
	}
}

// TestResendDeviceConfig_PressedTwice_TwoPublishesZeroNewRowsSameVersion is
// FR42.1's idempotence assertion: pressing the button twice publishes
// twice, identically, while InsertDeviceConfigNextVersion is still never
// called and the reported version never changes.
func TestResendDeviceConfig_PressedTwice_TwoPublishesZeroNewRowsSameVersion(t *testing.T) {
	now := time.Now()
	cfg := acceptedConfig(7)
	repo := &fakeRepo{
		getBoardReportingHealthRow:      reportingRow(t, now),
		getBoardReportingHealthOk:       true,
		getLatestAcceptedConfigResponse: cfg,
		listConfigHistoryResponse:       []DeviceConfigHistoryRow{{Version: 7, Accepted: true, AckedAt: &now}},
	}
	publisher := &fakePublisher{}
	server, _ := resendTestServer(repo, publisher)

	resp1, err := server.ResendDeviceConfig(authedTestCtx("alice"), &pb.ResendDeviceConfigRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("first ResendDeviceConfig: %v", err)
	}
	resp2, err := server.ResendDeviceConfig(authedTestCtx("alice"), &pb.ResendDeviceConfigRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("second ResendDeviceConfig: %v", err)
	}

	if len(publisher.retainedCalls) != 2 {
		t.Fatalf("PublishRetained calls = %d, want 2 (pressing twice publishes twice)", len(publisher.retainedCalls))
	}
	if string(publisher.retainedCalls[0].body) != string(publisher.retainedCalls[1].body) {
		t.Errorf("the two publishes carried different bodies -- pressing twice must republish the identical payload")
	}
	if len(repo.insertDeviceConfigNextVersionCalls) != 0 {
		t.Errorf("InsertDeviceConfigNextVersion calls = %d, want 0 after two presses", len(repo.insertDeviceConfigNextVersionCalls))
	}
	if resp1.Version != resp2.Version {
		t.Errorf("Version changed between presses: %d then %d, want the same version both times", resp1.Version, resp2.Version)
	}
}

// -- Availability: NOT_REPORTING, both NOTHING_TO_RESEND conditions, AVAILABLE

// TestGetResendAvailability_NotReporting_UsesSharedA23Function proves
// resendAvailability's NOT_REPORTING branch agrees with reportingStateFor --
// the exact function FR79's ListFleetHealth calls per row (see
// TestReportingStateFor_IsTheOnlyA23CallSiteInLeafLabAPI for the
// source-level "exactly one call site" half of this guarantee) -- for the
// same input.
func TestGetResendAvailability_NotReporting_UsesSharedA23Function(t *testing.T) {
	now := time.Now()
	row := notReportingRow(t, now)
	if got := reportingStateFor(row, now); got != pb.ReportingState_REPORTING_STATE_NOT_REPORTING {
		t.Fatalf("fixture sanity check: reportingStateFor(notReportingRow) = %v, want NOT_REPORTING", got)
	}

	repo := &fakeRepo{getBoardReportingHealthRow: row, getBoardReportingHealthOk: true}
	server, _ := resendTestServer(repo, nil)

	resp, err := server.GetResendAvailability(authedTestCtx("alice"), &pb.GetResendAvailabilityRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("GetResendAvailability: %v", err)
	}
	if resp.Available {
		t.Error("Available = true, want false for a board past A23's threshold")
	}
	if resp.Reason != pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_NOT_REPORTING {
		t.Errorf("Reason = %v, want RESEND_AVAILABILITY_REASON_NOT_REPORTING", resp.Reason)
	}
	if resp.Sentence == "" || resp.Alternative == "" {
		t.Error("Sentence/Alternative empty, want both populated for an unavailable reason (FR59.2/FR59.3)")
	}
	// GetLatestAcceptedConfig/ListConfigHistory must never be reached once
	// NOT_REPORTING is already decided -- resendAvailability's earlier
	// return.
	if repo.getLatestAcceptedConfigCalls != 0 {
		t.Errorf("GetLatestAcceptedConfig calls = %d, want 0 (NOT_REPORTING decided first)", repo.getLatestAcceptedConfigCalls)
	}
}

// TestGetResendAvailability_NothingToResend_NoConfigEverAccepted covers
// FR42.2's first NOTHING_TO_RESEND condition: no configuration has ever
// been accepted for this board.
func TestGetResendAvailability_NothingToResend_NoConfigEverAccepted(t *testing.T) {
	now := time.Now()
	repo := &fakeRepo{
		getBoardReportingHealthRow:      reportingRow(t, now),
		getBoardReportingHealthOk:       true,
		getLatestAcceptedConfigResponse: nil,
	}
	server, _ := resendTestServer(repo, nil)

	resp, err := server.GetResendAvailability(authedTestCtx("alice"), &pb.GetResendAvailabilityRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("GetResendAvailability: %v", err)
	}
	if resp.Available {
		t.Error("Available = true, want false when no config was ever accepted")
	}
	if resp.Reason != pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_NOTHING_TO_RESEND {
		t.Errorf("Reason = %v, want RESEND_AVAILABILITY_REASON_NOTHING_TO_RESEND", resp.Reason)
	}
}

// TestGetResendAvailability_NothingToResend_MostRecentPushRejected covers
// FR42.2's second, independent NOTHING_TO_RESEND condition: an older
// accepted version exists (GetLatestAcceptedConfig returns non-nil), but
// the *most recent* push was rejected -- still "nothing to resend", not
// "resend the stale older version instead".
func TestGetResendAvailability_NothingToResend_MostRecentPushRejected(t *testing.T) {
	now := time.Now()
	repo := &fakeRepo{
		getBoardReportingHealthRow:      reportingRow(t, now),
		getBoardReportingHealthOk:       true,
		getLatestAcceptedConfigResponse: acceptedConfig(3), // an older accepted version still exists
		listConfigHistoryResponse: []DeviceConfigHistoryRow{
			{Version: 4, Accepted: false, AckedAt: &now, RejectionReason: "bad checksum"},
		},
	}
	server, _ := resendTestServer(repo, nil)

	resp, err := server.GetResendAvailability(authedTestCtx("alice"), &pb.GetResendAvailabilityRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("GetResendAvailability: %v", err)
	}
	if resp.Available {
		t.Error("Available = true, want false when the most recent push was rejected")
	}
	if resp.Reason != pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_NOTHING_TO_RESEND {
		t.Errorf("Reason = %v, want RESEND_AVAILABILITY_REASON_NOTHING_TO_RESEND (an older accepted version does not make this resendable)", resp.Reason)
	}
}

// TestGetResendAvailability_Available_ReportingAndAcceptedAndNotRejected is
// the "not over-refused" companion: a healthy, reporting board with an
// accepted, non-rejected latest push is AVAILABLE, with empty
// sentence/alternative.
func TestGetResendAvailability_Available_ReportingAndAcceptedAndNotRejected(t *testing.T) {
	now := time.Now()
	repo := &fakeRepo{
		getBoardReportingHealthRow:      reportingRow(t, now),
		getBoardReportingHealthOk:       true,
		getLatestAcceptedConfigResponse: acceptedConfig(5),
		listConfigHistoryResponse:       []DeviceConfigHistoryRow{{Version: 5, Accepted: true, AckedAt: &now}},
	}
	server, _ := resendTestServer(repo, nil)

	resp, err := server.GetResendAvailability(authedTestCtx("alice"), &pb.GetResendAvailabilityRequest{DeviceId: "device-a"})
	if err != nil {
		t.Fatalf("GetResendAvailability: %v", err)
	}
	if !resp.Available {
		t.Errorf("Available = false (reason %v), want true", resp.Reason)
	}
	if resp.Reason != pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_AVAILABLE {
		t.Errorf("Reason = %v, want RESEND_AVAILABILITY_REASON_AVAILABLE", resp.Reason)
	}
	if resp.Sentence != "" || resp.Alternative != "" {
		t.Errorf("Sentence=%q Alternative=%q, want both empty when available", resp.Sentence, resp.Alternative)
	}
}

// -- The BFF's disabled-with-reason-inline contract: never enable then fail -

// TestResendDeviceConfig_RefusesWithGetResendAvailabilitysExactReason proves
// the two RPCs can never disagree: whenever GetResendAvailability answers
// unavailable, ResendDeviceConfig refuses with the exact same
// reason/sentence/alternative (both computed by the single shared
// resendAvailability) and never reaches the publisher -- so a caller that
// renders the button disabled per GetResendAvailability's answer can never
// observe a path where the button was enabled and the press failed anyway.
func TestResendDeviceConfig_RefusesWithGetResendAvailabilitysExactReason(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		repo func() *fakeRepo
	}{
		{
			name: "not_reporting",
			repo: func() *fakeRepo {
				return &fakeRepo{getBoardReportingHealthRow: notReportingRow(t, now), getBoardReportingHealthOk: true}
			},
		},
		{
			name: "nothing_to_resend_never_accepted",
			repo: func() *fakeRepo {
				return &fakeRepo{getBoardReportingHealthRow: reportingRow(t, now), getBoardReportingHealthOk: true}
			},
		},
		{
			name: "nothing_to_resend_most_recent_rejected",
			repo: func() *fakeRepo {
				return &fakeRepo{
					getBoardReportingHealthRow:      reportingRow(t, now),
					getBoardReportingHealthOk:       true,
					getLatestAcceptedConfigResponse: acceptedConfig(3),
					listConfigHistoryResponse:       []DeviceConfigHistoryRow{{Version: 4, Accepted: false, AckedAt: &now}},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			availRepo := tc.repo()
			availServer, _ := resendTestServer(availRepo, nil)
			availResp, err := availServer.GetResendAvailability(authedTestCtx("alice"), &pb.GetResendAvailabilityRequest{DeviceId: "device-a"})
			if err != nil {
				t.Fatalf("GetResendAvailability: %v", err)
			}
			if availResp.Available {
				t.Fatalf("test case %q must be unavailable -- fix the fixture", tc.name)
			}

			publisher := &fakePublisher{}
			resendRepo := tc.repo()
			resendServer, _ := resendTestServer(resendRepo, publisher)
			_, resendErr := resendServer.ResendDeviceConfig(authedTestCtx("alice"), &pb.ResendDeviceConfigRequest{DeviceId: "device-a"})
			if resendErr == nil {
				t.Fatal("ResendDeviceConfig succeeded, want a refusal matching GetResendAvailability's unavailable answer")
			}
			if len(publisher.retainedCalls) != 0 {
				t.Errorf("PublishRetained was called %d times despite an unavailable answer -- the button must never be enabled-then-failing", len(publisher.retainedCalls))
			}

			detail, ok := contract.FromError(resendErr)
			if !ok {
				t.Fatal("ResendDeviceConfig error carries no Failure detail")
			}
			if detail.Class != string(contract.FailureRefusedWithAlternative) {
				t.Errorf("Class = %q, want %q", detail.Class, contract.FailureRefusedWithAlternative)
			}
			if detail.Reason != availResp.Sentence {
				t.Errorf("ResendDeviceConfig reason = %q, GetResendAvailability sentence = %q -- must be identical", detail.Reason, availResp.Sentence)
			}
			if detail.Alternative != availResp.Alternative {
				t.Errorf("ResendDeviceConfig alternative = %q, GetResendAvailability alternative = %q -- must be identical", detail.Alternative, availResp.Alternative)
			}
		})
	}
}

// -- FR8.2: audited despite writing no device_config row --------------------

// TestResendDeviceConfig_AuditedWithActingPrincipal proves a successful
// re-send calls RecordAuditEntry with the acting principal as
// ActorSubject, the registered Action/EntityKind
// (auditRegistrations[resendDeviceConfigFullMethod]), and a nil EntityID --
// no new entity was created (see TestAuditor_RecordsReSendWithNoDeviceConfigRow's
// doc comment for the same nil-EntityID convention at the real-Postgres
// level).
func TestResendDeviceConfig_AuditedWithActingPrincipal(t *testing.T) {
	now := time.Now()
	repo := &fakeRepo{
		getBoardReportingHealthRow:      reportingRow(t, now),
		getBoardReportingHealthOk:       true,
		getLatestAcceptedConfigResponse: acceptedConfig(5),
		listConfigHistoryResponse:       []DeviceConfigHistoryRow{{Version: 5, Accepted: true, AckedAt: &now}},
	}
	server, _ := resendTestServer(repo, &fakePublisher{})

	if _, err := server.ResendDeviceConfig(authedTestCtx("alice"), &pb.ResendDeviceConfigRequest{DeviceId: "device-a"}); err != nil {
		t.Fatalf("ResendDeviceConfig: %v", err)
	}

	if len(repo.auditEntries) != 1 {
		t.Fatalf("audit entries recorded = %d, want 1", len(repo.auditEntries))
	}
	entry := repo.auditEntries[0]
	reg := auditRegistrations[resendDeviceConfigFullMethod]
	if entry.ActorSubject != "alice" {
		t.Errorf("ActorSubject = %q, want %q (the acting principal)", entry.ActorSubject, "alice")
	}
	if entry.Action != reg.Action {
		t.Errorf("Action = %q, want %q (the registered action)", entry.Action, reg.Action)
	}
	if entry.EntityKind != reg.EntityKind {
		t.Errorf("EntityKind = %q, want %q", entry.EntityKind, reg.EntityKind)
	}
	if entry.TargetHouseholdID == nil || *entry.TargetHouseholdID != 42 {
		t.Errorf("TargetHouseholdID = %v, want pointer to 42 (the resolved household)", entry.TargetHouseholdID)
	}
}

// -- NFR2: a non-member gets the exact same not-found as a nonexistent id ---

// TestGetResendAvailability_NonexistentAndOutOfScope_ByteIdenticalFailure
// mirrors TestGetDeviceConfig_NonexistentAndOutOfScope_ByteIdenticalFailure
// for GetResendAvailability: a nonexistent device_id and an out-of-scope
// device_id must be indistinguishable, and authorization must short-circuit
// before resendAvailability ever reaches GetBoardReportingHealth.
func TestGetResendAvailability_NonexistentAndOutOfScope_ByteIdenticalFailure(t *testing.T) {
	callerScope := authz.NewHouseholdScope(1)

	nonexistentAuthz := &fakeAuthz{scope: callerScope, resolveErr: authz.ErrNotFound}
	nonexistentRepo := &fakeRepo{}
	nonexistentServer := NewLeafLabAPIServer(nonexistentRepo, nonexistentAuthz, nil, nil, discardLogger())
	_, nonexistentErr := nonexistentServer.GetResendAvailability(authedTestCtx("alice"), &pb.GetResendAvailabilityRequest{DeviceId: "does-not-exist"})
	if nonexistentErr == nil {
		t.Fatal("GetResendAvailability for a nonexistent device_id returned nil error, want a refusal")
	}
	if nonexistentRepo.getBoardReportingHealthCalls != 0 {
		t.Errorf("nonexistent-device refusal reached GetBoardReportingHealth %d times, want 0", nonexistentRepo.getBoardReportingHealthCalls)
	}

	outOfScopeAuthz := &fakeAuthz{
		scope:      callerScope,
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: 7},
		resolveRes: authz.Resolution{HouseholdID: 2},
	}
	outOfScopeRepo := &fakeRepo{}
	outOfScopeServer := NewLeafLabAPIServer(outOfScopeRepo, outOfScopeAuthz, nil, nil, discardLogger())
	_, outOfScopeErr := outOfScopeServer.GetResendAvailability(authedTestCtx("alice"), &pb.GetResendAvailabilityRequest{DeviceId: "device-belongs-to-household-2"})
	if outOfScopeErr == nil {
		t.Fatal("GetResendAvailability for an out-of-scope device returned nil error, want a refusal")
	}
	if outOfScopeRepo.getBoardReportingHealthCalls != 0 {
		t.Errorf("out-of-scope refusal reached GetBoardReportingHealth %d times, want 0", outOfScopeRepo.getBoardReportingHealthCalls)
	}

	if string(failureBytes(t, nonexistentErr)) != string(failureBytes(t, outOfScopeErr)) {
		t.Error("marshaled gRPC status differs between nonexistent and out-of-scope refusals -- NFR2 requires byte-identical status and body")
	}
}

// TestResendDeviceConfig_NonexistentAndOutOfScope_ByteIdenticalFailure is
// the ResendDeviceConfig half of the same NFR2 guarantee.
func TestResendDeviceConfig_NonexistentAndOutOfScope_ByteIdenticalFailure(t *testing.T) {
	callerScope := authz.NewHouseholdScope(1)

	nonexistentAuthz := &fakeAuthz{scope: callerScope, resolveErr: authz.ErrNotFound}
	nonexistentRepo := &fakeRepo{}
	nonexistentServer := NewLeafLabAPIServer(nonexistentRepo, nonexistentAuthz, nil, nil, discardLogger())
	_, nonexistentErr := nonexistentServer.ResendDeviceConfig(authedTestCtx("alice"), &pb.ResendDeviceConfigRequest{DeviceId: "does-not-exist"})
	if nonexistentErr == nil {
		t.Fatal("ResendDeviceConfig for a nonexistent device_id returned nil error, want a refusal")
	}
	if nonexistentRepo.getBoardReportingHealthCalls != 0 {
		t.Errorf("nonexistent-device refusal reached GetBoardReportingHealth %d times, want 0", nonexistentRepo.getBoardReportingHealthCalls)
	}
	if len(nonexistentRepo.auditEntries) != 0 {
		t.Errorf("nonexistent-device refusal recorded %d audit entries, want 0", len(nonexistentRepo.auditEntries))
	}

	outOfScopeAuthz := &fakeAuthz{
		scope:      callerScope,
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: 7},
		resolveRes: authz.Resolution{HouseholdID: 2},
	}
	outOfScopeRepo := &fakeRepo{}
	outOfScopeServer := NewLeafLabAPIServer(outOfScopeRepo, outOfScopeAuthz, nil, nil, discardLogger())
	_, outOfScopeErr := outOfScopeServer.ResendDeviceConfig(authedTestCtx("alice"), &pb.ResendDeviceConfigRequest{DeviceId: "device-belongs-to-household-2"})
	if outOfScopeErr == nil {
		t.Fatal("ResendDeviceConfig for an out-of-scope device returned nil error, want a refusal")
	}
	if outOfScopeRepo.getBoardReportingHealthCalls != 0 {
		t.Errorf("out-of-scope refusal reached GetBoardReportingHealth %d times, want 0", outOfScopeRepo.getBoardReportingHealthCalls)
	}

	if string(failureBytes(t, nonexistentErr)) != string(failureBytes(t, outOfScopeErr)) {
		t.Error("marshaled gRPC status differs between nonexistent and out-of-scope refusals -- NFR2 requires byte-identical status and body")
	}
}
