package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// -- FR10/FR12 activation: the five admin RPCs -------------------------------
//
// Unit coverage against fakeRepo (server_test.go), proving requireAdminEligible's
// gate and each handler's own logic (validation, delegation shape, audit
// content) without a live Postgres connection. See
// admin_elevation_integration_test.go for the real-SQL half of this
// coverage -- notably RenewElevation only ever extending an *already open*
// row, never opening a new one, which lives in repository.go's SQL, not in
// server.go.

// adminCtx returns a context carrying grpcauth.Claims for subject with the
// leaflab-admin realm role -- i.e. eligible for every RPC in this section.
func adminCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: []string{RoleAdmin}})
}

// nonAdminCtx returns a context carrying grpcauth.Claims for subject with
// no leaflab-admin role -- a regular household-member caller, ineligible
// for every RPC in this section.
func nonAdminCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: []string{"some-other-role"}})
}

func wantPermissionDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want a permission-denied refusal, got nil error")
	}
	f, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no contract.Failure detail: %v", err)
	}
	if f.Class != string(contract.FailurePermissionDenied) {
		t.Errorf("Failure.Class = %q, want %q", f.Class, contract.FailurePermissionDenied)
	}
}

// TestRequireAdminEligible_RefusesNonAdmin_AllFiveRPCs proves every one of
// the five admin RPCs gates on requireAdminEligible *before* touching the
// repository at all -- a non-admin caller is refused, and no admin
// repository method is ever reached (FR10, FR12 activation: "eligibility
// is a precondition ... never proof of either by itself").
func TestRequireAdminEligible_RefusesNonAdmin_AllFiveRPCs(t *testing.T) {
	ctx := nonAdminCtx("mallory")

	t.Run("ResolveToHousehold", func(t *testing.T) {
		repo := &fakeRepo{}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		_, err := server.ResolveToHousehold(ctx, &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_PersonIdentifier{PersonIdentifier: "alice"}})
		wantPermissionDenied(t, err)
		if repo.adminByPersonCalls != 0 || len(repo.auditEntries) != 0 {
			t.Errorf("repo was reached before the admin gate: adminByPersonCalls=%d auditEntries=%d", repo.adminByPersonCalls, len(repo.auditEntries))
		}
	})

	t.Run("Elevate", func(t *testing.T) {
		repo := &fakeRepo{}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		_, err := server.Elevate(ctx, &pb.ElevateRequest{TargetHouseholdId: 1, Reason: "investigating"})
		wantPermissionDenied(t, err)
		if len(repo.openElevationCalls) != 0 {
			t.Errorf("repo was reached before the admin gate: openElevationCalls=%d", len(repo.openElevationCalls))
		}
	})

	t.Run("RenewElevation", func(t *testing.T) {
		repo := &fakeRepo{}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		_, err := server.RenewElevation(ctx, &pb.RenewElevationRequest{TargetHouseholdId: 1, Reason: "still investigating"})
		wantPermissionDenied(t, err)
		if len(repo.renewElevationCalls) != 0 {
			t.Errorf("repo was reached before the admin gate: renewElevationCalls=%d", len(repo.renewElevationCalls))
		}
	})

	t.Run("EndElevation", func(t *testing.T) {
		repo := &fakeRepo{}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		_, err := server.EndElevation(ctx, &pb.EndElevationRequest{TargetHouseholdId: 1})
		wantPermissionDenied(t, err)
		if len(repo.endElevationCalls) != 0 {
			t.Errorf("repo was reached before the admin gate: endElevationCalls=%d", len(repo.endElevationCalls))
		}
	})

	t.Run("GetElevationStatus", func(t *testing.T) {
		repo := &fakeRepo{}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		_, err := server.GetElevationStatus(ctx, &pb.GetElevationStatusRequest{TargetHouseholdId: 1})
		wantPermissionDenied(t, err)
		if repo.activeElevationCalls != 0 {
			t.Errorf("repo was reached before the admin gate: activeElevationCalls=%d", repo.activeElevationCalls)
		}
	})
}

// -- FR10.2: ResolveToHousehold's standing lane ------------------------------

// forbiddenFieldSubstrings names field-name fragments that must never
// appear on ResolveToHousehold's response types -- FR10.2's "that is the
// whole lane": no readings, region/plant structure, config payloads or
// audit rows, only FR79's health projection.
var forbiddenFieldSubstrings = []string{"reading", "region", "plant", "config", "audit"}

// TestResolveToHouseholdResponse_HasOnlyHealthFields is a structural proof
// (over the proto descriptor, not a particular instance) that
// ResolveToHouseholdResponse and AdminBoardHealth carry no field whose name
// suggests a wider projection than FR79's health fields -- "model the
// response as its own message type" per the Implementation section, not a
// wider one with fields blanked at runtime.
func TestResolveToHouseholdResponse_HasOnlyHealthFields(t *testing.T) {
	checkFields := func(t *testing.T, name string, fields []string) {
		t.Helper()
		for _, f := range fields {
			for _, forbidden := range forbiddenFieldSubstrings {
				if containsFold(f, forbidden) {
					t.Errorf("%s has field %q, which looks like it carries %q -- FR10.2's standing lane must never widen past FR79's health fields", name, f, forbidden)
				}
			}
		}
	}

	respDesc := (&pb.ResolveToHouseholdResponse{}).ProtoReflect().Descriptor()
	var respFields []string
	for i := 0; i < respDesc.Fields().Len(); i++ {
		respFields = append(respFields, string(respDesc.Fields().Get(i).Name()))
	}
	checkFields(t, "ResolveToHouseholdResponse", respFields)

	healthDesc := (&pb.AdminBoardHealth{}).ProtoReflect().Descriptor()
	var healthFields []string
	for i := 0; i < healthDesc.Fields().Len(); i++ {
		healthFields = append(healthFields, string(healthDesc.Fields().Get(i).Name()))
	}
	checkFields(t, "AdminBoardHealth", healthFields)

	// Positive control: the fields that *should* be there, per FR79 (device
	// id, board display name, household identity, last-seen age, active
	// version, outstanding push, sensor count).
	wantHealthFields := []string{"device_id", "board_display_name", "household_id", "household_name", "last_seen_at", "active_version", "outstanding_push", "sensor_count"}
	if len(healthFields) != len(wantHealthFields) {
		t.Errorf("AdminBoardHealth has %d fields %v, want exactly %v", len(healthFields), healthFields, wantHealthFields)
	}
}

func containsFold(s, substr string) bool {
	sl, subl := []rune(s), []rune(substr)
	for i := 0; i+len(subl) <= len(sl); i++ {
		match := true
		for j, r := range subl {
			gr := sl[i+j]
			if gr >= 'A' && gr <= 'Z' {
				gr += 'a' - 'A'
			}
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			if gr != r {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestResolveToHousehold_QueryGranularityAudit_OneRowPerCallRegardlessOfMatches
// proves FR10.4: exactly one audit row is written per ResolveToHousehold
// call, carrying the query term -- never one per returned board, no matter
// how many boards the query matches.
func TestResolveToHousehold_QueryGranularityAudit_OneRowPerCallRegardlessOfMatches(t *testing.T) {
	repo := &fakeRepo{
		adminByPersonRows: []AdminBoardHealthRow{
			{DeviceID: "device-1", HouseholdID: 1},
			{DeviceID: "device-2", HouseholdID: 1},
			{DeviceID: "device-3", HouseholdID: 1},
		},
	}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())

	resp, err := server.ResolveToHousehold(adminCtx("root"), &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_PersonIdentifier{PersonIdentifier: "alice@example.com"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Boards) != 3 {
		t.Fatalf("got %d boards, want 3", len(resp.Boards))
	}
	if len(repo.auditEntries) != 1 {
		t.Fatalf("got %d audit entries, want exactly 1 regardless of match count (FR10.4)", len(repo.auditEntries))
	}
	entry := repo.auditEntries[0]
	if entry.EntityID == nil || *entry.EntityID != "person_identifier=alice@example.com" {
		t.Errorf("audit entry EntityID = %v, want the query term", entry.EntityID)
	}
	if entry.ActorSubject != "root" {
		t.Errorf("audit entry ActorSubject = %q, want %q", entry.ActorSubject, "root")
	}
}

// TestResolveToHousehold_ResolvesByAllThreeQueryKinds proves the standing
// lane accepts a person identifier, a support reference (stubbed until
// FR80 lands -- resolves to zero boards but is still audited), and a
// partial device id, each delegating to the correct repository method and
// none other.
func TestResolveToHousehold_ResolvesByAllThreeQueryKinds(t *testing.T) {
	t.Run("person_identifier", func(t *testing.T) {
		repo := &fakeRepo{adminByPersonRows: []AdminBoardHealthRow{{DeviceID: "d1"}}}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		resp, err := server.ResolveToHousehold(adminCtx("root"), &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_PersonIdentifier{PersonIdentifier: "alice"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.adminByPersonCalls != 1 || repo.adminByPersonArg != "alice" {
			t.Errorf("AdminBoardHealthByPerson calls=%d arg=%q, want 1 call with %q", repo.adminByPersonCalls, repo.adminByPersonArg, "alice")
		}
		if repo.adminByPartialCalls != 0 {
			t.Errorf("AdminBoardHealthByPartialDeviceID was called for a person_identifier query")
		}
		if len(resp.Boards) != 1 {
			t.Errorf("got %d boards, want 1", len(resp.Boards))
		}
	})

	t.Run("support_reference_stub", func(t *testing.T) {
		repo := &fakeRepo{}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		resp, err := server.ResolveToHousehold(adminCtx("root"), &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_SupportReference{SupportReference: "SR-123"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.adminByPersonCalls != 0 || repo.adminByPartialCalls != 0 {
			t.Errorf("a support_reference query reached a person/partial-device repo method: person=%d partial=%d", repo.adminByPersonCalls, repo.adminByPartialCalls)
		}
		if len(resp.Boards) != 0 {
			t.Errorf("got %d boards for a stubbed support_reference query, want 0", len(resp.Boards))
		}
		if len(repo.auditEntries) != 1 {
			t.Errorf("got %d audit entries for a stubbed support_reference query, want 1 (still audited)", len(repo.auditEntries))
		}
	})

	t.Run("partial_device_id", func(t *testing.T) {
		repo := &fakeRepo{adminByPartialRows: []AdminBoardHealthRow{{DeviceID: "device-abc123"}}}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
		resp, err := server.ResolveToHousehold(adminCtx("root"), &pb.ResolveToHouseholdRequest{Query: &pb.ResolveToHouseholdRequest_PartialDeviceId{PartialDeviceId: "abc"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.adminByPartialCalls != 1 || repo.adminByPartialArg != "abc" {
			t.Errorf("AdminBoardHealthByPartialDeviceID calls=%d arg=%q, want 1 call with %q", repo.adminByPartialCalls, repo.adminByPartialArg, "abc")
		}
		if len(resp.Boards) != 1 {
			t.Errorf("got %d boards, want 1", len(resp.Boards))
		}
	})
}

// -- FR10.1: Elevate ----------------------------------------------------------

// TestElevate_RequiresReason proves an empty reason is refused before the
// repository is ever reached.
func TestElevate_RequiresReason(t *testing.T) {
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
	_, err := server.Elevate(adminCtx("root"), &pb.ElevateRequest{TargetHouseholdId: 1, Reason: ""})
	if err == nil {
		t.Fatal("want a refusal for an empty reason")
	}
	f, ok := contract.FromError(err)
	if !ok || f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Failure = %+v, ok=%v, want class %q", f, ok, contract.FailureInvalidArgument)
	}
	if len(repo.openElevationCalls) != 0 {
		t.Error("OpenElevation was called despite an empty reason")
	}
}

// TestElevate_OpensDefaultSixtyMinuteElevation_AndAudits proves a
// successful Elevate call writes a 60-minute (DefaultElevationDuration)
// elevation row for exactly the requested target household and reason,
// and audits it with both admin subject and target household stamped
// (FR10.1).
func TestElevate_OpensDefaultSixtyMinuteElevation_AndAudits(t *testing.T) {
	repo := &fakeRepo{householdExists: true}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())

	before := time.Now()
	resp, err := server.Elevate(adminCtx("root"), &pb.ElevateRequest{TargetHouseholdId: 42, Reason: "board reported stuck offline"})
	after := time.Now()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.householdExistsArg != 42 {
		t.Errorf("HouseholdExists checked household %d, want 42", repo.householdExistsArg)
	}
	if len(repo.openElevationCalls) != 1 {
		t.Fatalf("got %d OpenElevation calls, want 1", len(repo.openElevationCalls))
	}
	call := repo.openElevationCalls[0]
	if call.adminSubject != "root" {
		t.Errorf("OpenElevation adminSubject = %q, want %q", call.adminSubject, "root")
	}
	if call.targetHouseholdID != 42 {
		t.Errorf("OpenElevation targetHouseholdID = %d, want 42", call.targetHouseholdID)
	}
	if call.reason != "board reported stuck offline" {
		t.Errorf("OpenElevation reason = %q, want the requested reason", call.reason)
	}

	// Deliberately a literal 60 minutes, not the DefaultElevationDuration
	// symbol -- this must catch a drift in that constant's *value*
	// (FR10.1's "60 minutes"), not just prove the handler used whatever
	// the constant currently says.
	const wantDefaultDuration = 60 * time.Minute
	minExpiry := before.Add(wantDefaultDuration)
	maxExpiry := after.Add(wantDefaultDuration)
	if call.expiresAt.Before(minExpiry) || call.expiresAt.After(maxExpiry) {
		t.Errorf("OpenElevation expiresAt = %v, want within [%v, %v] (60-minute default)", call.expiresAt, minExpiry, maxExpiry)
	}

	target := int64(42)
	if call.entry.TargetHouseholdID == nil || *call.entry.TargetHouseholdID != target {
		t.Errorf("audit entry TargetHouseholdID = %v, want %d", call.entry.TargetHouseholdID, target)
	}
	if call.entry.ActorSubject != "root" {
		t.Errorf("audit entry ActorSubject = %q, want %q", call.entry.ActorSubject, "root")
	}
	if call.entry.Action != audit.ActionElevate {
		t.Errorf("audit entry Action = %q, want %q", call.entry.Action, audit.ActionElevate)
	}
	if call.entry.Reason == nil || *call.entry.Reason != "board reported stuck offline" {
		t.Errorf("audit entry Reason = %v, want the elevation reason (FR10.1 requires a reason on the audit record)", call.entry.Reason)
	}

	if resp.ExpiresAt == nil {
		t.Fatal("ElevateResponse.ExpiresAt is nil")
	}
}

// TestElevate_ConfigurableDuration proves WithElevationDuration overrides
// DefaultElevationDuration -- FR10.1's "60 minutes, configurable".
func TestElevate_ConfigurableDuration(t *testing.T) {
	repo := &fakeRepo{householdExists: true}
	const customDuration = 15 * time.Minute
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), WithElevationDuration(customDuration))

	before := time.Now()
	if _, err := server.Elevate(adminCtx("root"), &pb.ElevateRequest{TargetHouseholdId: 1, Reason: "test"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now()

	call := repo.openElevationCalls[0]
	minExpiry := before.Add(customDuration)
	maxExpiry := after.Add(customDuration)
	if call.expiresAt.Before(minExpiry) || call.expiresAt.After(maxExpiry) {
		t.Errorf("OpenElevation expiresAt = %v, want within [%v, %v] (custom %v duration)", call.expiresAt, minExpiry, maxExpiry, customDuration)
	}
}

// TestElevate_UnknownHousehold_Refused proves Elevate refuses a target
// household that does not exist, without ever calling OpenElevation.
func TestElevate_UnknownHousehold_Refused(t *testing.T) {
	repo := &fakeRepo{householdExists: false}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
	_, err := server.Elevate(adminCtx("root"), &pb.ElevateRequest{TargetHouseholdId: 999, Reason: "test"})
	if err == nil {
		t.Fatal("want a refusal for an unknown household")
	}
	f, ok := contract.FromError(err)
	if !ok || f.Class != string(contract.FailureNotFound) {
		t.Errorf("Failure = %+v, ok=%v, want class %q", f, ok, contract.FailureNotFound)
	}
	if len(repo.openElevationCalls) != 0 {
		t.Error("OpenElevation was called for an unknown household")
	}
}

// -- FR10.1: RenewElevation ---------------------------------------------------

// TestRenewElevation_RequiresRestatedReason proves an empty reason is
// refused before the repository is ever reached -- renewal, like Elevate,
// cannot skip stating a reason.
func TestRenewElevation_RequiresRestatedReason(t *testing.T) {
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
	_, err := server.RenewElevation(adminCtx("root"), &pb.RenewElevationRequest{TargetHouseholdId: 1, Reason: ""})
	if err == nil {
		t.Fatal("want a refusal for an empty reason")
	}
	f, ok := contract.FromError(err)
	if !ok || f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Failure = %+v, ok=%v, want class %q", f, ok, contract.FailureInvalidArgument)
	}
	if len(repo.renewElevationCalls) != 0 {
		t.Error("RenewElevation was called despite an empty reason")
	}
}

// TestRenewElevation_RefusesWhenNoneOpen proves the handler surfaces
// ErrNoActiveElevation as a refused_with_alternative failure -- and never
// falls back to opening a fresh elevation itself (RenewElevation must only
// ever extend an existing open row; that guarantee lives in
// repository.go's SQL, proven end-to-end in
// admin_elevation_integration_test.go -- this asserts the handler doesn't
// paper over the repository's refusal by calling OpenElevation instead).
func TestRenewElevation_RefusesWhenNoneOpen(t *testing.T) {
	repo := &fakeRepo{renewElevationErr: ErrNoActiveElevation}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
	_, err := server.RenewElevation(adminCtx("root"), &pb.RenewElevationRequest{TargetHouseholdId: 7, Reason: "restated reason"})
	if err == nil {
		t.Fatal("want a refusal when no elevation is currently open")
	}
	f, ok := contract.FromError(err)
	if !ok || f.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("Failure = %+v, ok=%v, want class %q", f, ok, contract.FailureRefusedWithAlternative)
	}
	if len(repo.openElevationCalls) != 0 {
		t.Error("RenewElevation fell back to OpenElevation when none was open -- renewal must never open a new elevation")
	}
}

// TestRenewElevation_Success_AuditsWithSubjectAndHousehold proves a
// successful renewal delegates the restated reason and new expiry
// correctly, and its audit entry stamps both admin subject and target
// household (FR10.1, FR10.3).
func TestRenewElevation_Success_AuditsWithSubjectAndHousehold(t *testing.T) {
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())

	resp, err := server.RenewElevation(adminCtx("root"), &pb.RenewElevationRequest{TargetHouseholdId: 7, Reason: "still investigating"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ExpiresAt == nil {
		t.Fatal("RenewElevationResponse.ExpiresAt is nil")
	}
	if len(repo.renewElevationCalls) != 1 {
		t.Fatalf("got %d RenewElevation calls, want 1", len(repo.renewElevationCalls))
	}
	call := repo.renewElevationCalls[0]
	if call.adminSubject != "root" || call.targetHouseholdID != 7 || call.reason != "still investigating" {
		t.Errorf("RenewElevation called with (%q, %d, %q), want (%q, %d, %q)", call.adminSubject, call.targetHouseholdID, call.reason, "root", 7, "still investigating")
	}
	household := int64(7)
	if call.entry.TargetHouseholdID == nil || *call.entry.TargetHouseholdID != household {
		t.Errorf("audit entry TargetHouseholdID = %v, want %d", call.entry.TargetHouseholdID, household)
	}
	if call.entry.ActorSubject != "root" {
		t.Errorf("audit entry ActorSubject = %q, want %q", call.entry.ActorSubject, "root")
	}
}

// -- FR10.1: EndElevation ------------------------------------------------------

// TestEndElevation_ClosesOpenElevation proves a successful EndElevation
// call delegates to the repository for exactly the caller's subject and
// the requested target household, and audits with both stamped.
func TestEndElevation_ClosesOpenElevation(t *testing.T) {
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())

	if _, err := server.EndElevation(adminCtx("root"), &pb.EndElevationRequest{TargetHouseholdId: 9}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.endElevationCalls) != 1 {
		t.Fatalf("got %d EndElevation calls, want 1", len(repo.endElevationCalls))
	}
	call := repo.endElevationCalls[0]
	if call.adminSubject != "root" || call.targetHouseholdID != 9 {
		t.Errorf("EndElevation called with (%q, %d), want (%q, %d)", call.adminSubject, call.targetHouseholdID, "root", 9)
	}
	household := int64(9)
	if call.entry.TargetHouseholdID == nil || *call.entry.TargetHouseholdID != household {
		t.Errorf("audit entry TargetHouseholdID = %v, want %d", call.entry.TargetHouseholdID, household)
	}
}

// TestEndElevation_RefusesWhenNoneOpen proves ending an elevation that
// isn't open surfaces a not-found refusal, distinct from success.
func TestEndElevation_RefusesWhenNoneOpen(t *testing.T) {
	repo := &fakeRepo{endElevationErr: ErrNoActiveElevation}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())
	_, err := server.EndElevation(adminCtx("root"), &pb.EndElevationRequest{TargetHouseholdId: 9})
	if err == nil {
		t.Fatal("want a refusal when no elevation is currently open")
	}
	f, ok := contract.FromError(err)
	if !ok || f.Class != string(contract.FailureNotFound) {
		t.Errorf("Failure = %+v, ok=%v, want class %q", f, ok, contract.FailureNotFound)
	}
}

// -- A22: GetElevationStatus --------------------------------------------------

// TestGetElevationStatus_ReflectsCurrentStateAndExpiry proves
// GetElevationStatus reports both the elevated and not-elevated states
// accurately, including the remaining-time (expires_at) field while
// elevated (A22).
func TestGetElevationStatus_ReflectsCurrentStateAndExpiry(t *testing.T) {
	t.Run("elevated", func(t *testing.T) {
		expiresAt := time.Now().Add(37 * time.Minute)
		repo := &fakeRepo{activeElevationExpiresAt: expiresAt}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())

		resp, err := server.GetElevationStatus(adminCtx("root"), &pb.GetElevationStatusRequest{TargetHouseholdId: 3})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.Elevated {
			t.Error("Elevated = false, want true")
		}
		if resp.ExpiresAt == nil {
			t.Fatal("ExpiresAt is nil while elevated, want the remaining-time instant (A22)")
		}
		gotMillis := resp.ExpiresAt.UnixMillis
		wantMillis := expiresAt.UnixMilli()
		if gotMillis != wantMillis {
			t.Errorf("ExpiresAt.UnixMillis = %d, want %d", gotMillis, wantMillis)
		}
		if repo.activeElevationSubject != "root" || repo.activeElevationHousehold != 3 {
			t.Errorf("ActiveElevation called with (%q, %d), want (%q, %d)", repo.activeElevationSubject, repo.activeElevationHousehold, "root", 3)
		}
	})

	t.Run("not_elevated", func(t *testing.T) {
		repo := &fakeRepo{activeElevationErr: ErrNoActiveElevation}
		server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())

		resp, err := server.GetElevationStatus(adminCtx("root"), &pb.GetElevationStatusRequest{TargetHouseholdId: 3})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Elevated {
			t.Error("Elevated = true, want false when no active elevation exists")
		}
		if resp.ExpiresAt != nil {
			t.Errorf("ExpiresAt = %v, want nil when not elevated", resp.ExpiresAt)
		}
	})
}

// TestGetElevationStatus_PerHouseholdIsolation proves the handler passes
// the exact requested target household through to ActiveElevation,
// unmodified -- the structural half of FR10.3's "an elevation against
// household A does not open household B": the handler never substitutes a
// different household than the one named in the request, for either an
// elevated or a not-elevated outcome.
func TestGetElevationStatus_PerHouseholdIsolation(t *testing.T) {
	repo := &fakeRepo{activeElevationErr: ErrNoActiveElevation}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger())

	if _, err := server.GetElevationStatus(adminCtx("root"), &pb.GetElevationStatusRequest{TargetHouseholdId: 55}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.activeElevationHousehold != 55 {
		t.Errorf("ActiveElevation checked household %d, want the requested household 55 (never a different one)", repo.activeElevationHousehold)
	}
}

// -- FR75: elevation confers no membership-change capability ----------------

// TestDeviceRepository_HasNoMembershipMutationMethod is the structural
// proof behind "elevation must not confer membership-change capability"
// (Implementation section, "assert"): every admin RPC handler in this file
// reaches the database exclusively through the deviceRepository interface
// -- it is reflected here to prove that interface exposes no method that
// could write household_membership. An elevated admin therefore cannot
// change membership no matter what ElevatedScope permits, because the
// capability to do so does not exist on the one seam admin handlers hold.
func TestDeviceRepository_HasNoMembershipMutationMethod(t *testing.T) {
	ifaceType := reflect.TypeOf((*deviceRepository)(nil)).Elem()
	for i := 0; i < ifaceType.NumMethod(); i++ {
		name := ifaceType.Method(i).Name
		if containsFold(name, "membership") {
			t.Errorf("deviceRepository has method %q -- an admin RPC handler holding only this interface could then change household_membership under elevation, which FR75 forbids (membership changes are member-only)", name)
		}
	}
}
