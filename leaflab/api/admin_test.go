package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/apierrors"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/staleness"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// ── FR79: toBoardHealth derivation (pure function, no DB) ──────────────────

// marshalConfig builds the protojson bytes toBoardHealth expects in
// boardHealthRow.ActiveConfigJSON, mirroring what QueryBoardHealth reads
// back from device_config.config_json.
func marshalConfig(t *testing.T, pollIntervalsMs ...uint32) []byte {
	t.Helper()
	cfg := &configpb.DeviceConfig{DeviceId: "dev-1", Version: 1}
	for _, ms := range pollIntervalsMs {
		cfg.Sensors = append(cfg.Sensors, &configpb.SensorConfig{PollIntervalMs: ms})
	}
	b, err := protojson.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return b
}

// TestToBoardHealth_A23ThresholdUsesLongestConfiguredPollInterval verifies
// A23: a board with a 30-minute poll interval is not-reporting at 90
// minutes (3x), and a board with a 1-minute interval floors at 15 minutes —
// derived from the board's *longest* configured sensor poll interval, not
// just any one sensor.
func TestToBoardHealth_A23ThresholdUsesLongestConfiguredPollInterval(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := staleness.NewConfig()

	tests := []struct {
		name          string
		pollMs        []uint32 // multiple sensors; longest wins
		lastSeenAgo   time.Duration
		wantReporting bool
	}{
		{"30m interval, 89m ago: reporting", []uint32{30 * 60 * 1000}, 89 * time.Minute, true},
		{"30m interval, 91m ago: not reporting", []uint32{30 * 60 * 1000}, 91 * time.Minute, false},
		{"1m interval floors at 15m, 14m ago: reporting", []uint32{60 * 1000}, 14 * time.Minute, true},
		{"1m interval floors at 15m, 16m ago: not reporting", []uint32{60 * 1000}, 16 * time.Minute, false},
		{"longest of several sensors wins: 5m+30m -> 90m threshold, 89m ago: reporting",
			[]uint32{5 * 60 * 1000, 30 * 60 * 1000}, 89 * time.Minute, true},
		{"longest of several sensors wins: 5m+30m -> 90m threshold, 91m ago: not reporting",
			[]uint32{5 * 60 * 1000, 30 * 60 * 1000}, 91 * time.Minute, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := boardHealthRow{
				BoardID:          1,
				DeviceID:         "dev-1",
				LastSeenEpoch:    now.Add(-tt.lastSeenAgo).Unix(),
				ActiveConfigJSON: marshalConfig(t, tt.pollMs...),
			}
			got := toBoardHealth(row, cfg, now)
			if got.Reporting != tt.wantReporting {
				t.Errorf("Reporting = %v, want %v (last seen %v ago)", got.Reporting, tt.wantReporting, tt.lastSeenAgo)
			}
		})
	}
}

// TestToBoardHealth_NoConfigUsesDefaultPollInterval verifies a board with no
// accepted config at all still gets a threshold (via defaultPollInterval),
// not a zero/unbounded one.
func TestToBoardHealth_NoConfigUsesDefaultPollInterval(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := staleness.NewConfig()

	row := boardHealthRow{
		BoardID:          1,
		DeviceID:         "dev-1",
		LastSeenEpoch:    now.Add(-1 * time.Hour).Unix(),
		ActiveConfigJSON: nil, // no accepted config
	}
	got := toBoardHealth(row, cfg, now)
	// defaultPollInterval is 60s; A23 floors at 15m regardless, so 1h ago
	// exceeds the 15m floor threshold: not reporting.
	if got.Reporting {
		t.Errorf("Reporting = true, want false (1h since last seen, no config, floor is 15m)")
	}
}

// TestToBoardHealth_ZeroPollIntervalMsFallsBackToDefault verifies a sensor
// left at poll_interval_ms=0 ("use device default") contributes
// defaultPollInterval, not a zero-duration threshold input.
func TestToBoardHealth_ZeroPollIntervalMsFallsBackToDefault(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := staleness.NewConfig()

	row := boardHealthRow{
		BoardID:          1,
		DeviceID:         "dev-1",
		LastSeenEpoch:    now.Add(-14 * time.Minute).Unix(),
		ActiveConfigJSON: marshalConfig(t, 0),
	}
	got := toBoardHealth(row, cfg, now)
	// defaultPollInterval (60s) floors at 15m; 14m ago is inside that window.
	if !got.Reporting {
		t.Errorf("Reporting = false, want true (14m ago, defaulted interval floors at 15m)")
	}
}

// TestToBoardHealth_PushOutstanding verifies push_outstanding and its
// duration are derived only from a rejected/pending latest device_config row.
func TestToBoardHealth_PushOutstanding(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := staleness.NewConfig()
	accepted := false
	pushedEpoch := now.Add(-5 * time.Minute).Unix()

	row := boardHealthRow{
		BoardID:           1,
		DeviceID:          "dev-1",
		LastSeenEpoch:     now.Unix(),
		LatestAccepted:    &accepted,
		LatestPushedEpoch: &pushedEpoch,
	}
	got := toBoardHealth(row, cfg, now)
	if !got.PushOutstanding {
		t.Fatalf("PushOutstanding = false, want true")
	}
	if got.PushOutstandingSeconds != 300 {
		t.Errorf("PushOutstandingSeconds = %d, want 300", got.PushOutstandingSeconds)
	}
}

func TestToBoardHealth_NoPushOutstandingWhenLatestAccepted(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := staleness.NewConfig()
	accepted := true

	row := boardHealthRow{
		BoardID:        1,
		DeviceID:       "dev-1",
		LastSeenEpoch:  now.Unix(),
		LatestAccepted: &accepted,
	}
	got := toBoardHealth(row, cfg, now)
	if got.PushOutstanding {
		t.Errorf("PushOutstanding = true, want false (latest config already accepted)")
	}
	if got.PushOutstandingSeconds != 0 {
		t.Errorf("PushOutstandingSeconds = %d, want 0", got.PushOutstandingSeconds)
	}
}

// ── FR79: no computed health score or severity ranking ─────────────────────

// TestBoardHealthProto_ExposesOnlyFR79Fields is a regression guard: FR79
// says the fleet listing has "no computed health score or severity
// ranking" — this asserts the wire message's exact field set, so adding a
// score/rank/severity field to BoardHealth breaks this test loudly instead
// of silently.
func TestBoardHealthProto_ExposesOnlyFR79Fields(t *testing.T) {
	want := map[string]bool{
		"device_id":                true,
		"board_id":                 true,
		"last_seen_age_seconds":    true,
		"reporting":                true,
		"active_config_version":    true,
		"push_outstanding":         true,
		"push_outstanding_seconds": true,
		"sensor_count":             true,
	}

	msg := (&pb.BoardHealth{}).ProtoReflect()
	fields := msg.Descriptor().Fields()
	got := make(map[string]bool, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		got[string(fields.Get(i).Name())] = true
	}

	for name := range want {
		if !got[name] {
			t.Errorf("BoardHealth missing expected FR79 field %q", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("BoardHealth has unexpected field %q (FR79: no health score or severity ranking, no fields beyond the four field groups)", name)
		}
	}
}

// TestResolveResponse_ExposesOnlyHouseholdAndBoards guards FR10.2's "and
// nothing else": ResolveResponse must carry exactly household_id and boards.
func TestResolveResponse_ExposesOnlyHouseholdAndBoards(t *testing.T) {
	msg := (&pb.ResolveResponse{}).ProtoReflect()
	fields := msg.Descriptor().Fields()
	var names []string
	for i := 0; i < fields.Len(); i++ {
		names = append(names, string(fields.Get(i).Name()))
	}
	if len(names) != 2 {
		t.Fatalf("ResolveResponse has %d fields %v, want exactly [household_id boards]", len(names), names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if !seen["household_id"] || !seen["boards"] {
		t.Errorf("ResolveResponse fields = %v, want [household_id boards]", names)
	}
}

// ── FR10, A22: elevationStateResponse ───────────────────────────────────────

func TestElevationStateResponse_RemainingSecondsPositive(t *testing.T) {
	expiresAt := time.Now().Add(45 * time.Minute)
	state := elevationStateResponse(7, "on-call ticket 123", expiresAt)

	if !state.Active {
		t.Fatalf("Active = false, want true")
	}
	if state.TargetHouseholdId != 7 {
		t.Errorf("TargetHouseholdId = %d, want 7", state.TargetHouseholdId)
	}
	if state.Reason != "on-call ticket 123" {
		t.Errorf("Reason = %q, want %q", state.Reason, "on-call ticket 123")
	}
	// Allow a couple seconds of test execution slack either side of 45m.
	if state.RemainingSeconds < 44*60 || state.RemainingSeconds > 45*60 {
		t.Errorf("RemainingSeconds = %d, want ~2700 (45m)", state.RemainingSeconds)
	}
}

// TestElevationStateResponse_ClampsNegativeRemaining verifies remaining time
// never reports negative even if called against an already-expired window
// (A22: "remaining time is readable while elevated" implies a sane floor).
func TestElevationStateResponse_ClampsNegativeRemaining(t *testing.T) {
	expiresAt := time.Now().Add(-10 * time.Minute) // already expired
	state := elevationStateResponse(7, "reason", expiresAt)
	if state.RemainingSeconds != 0 {
		t.Errorf("RemainingSeconds = %d, want 0 (clamped, not negative)", state.RemainingSeconds)
	}
}

// ── NFR2: resolveNotFoundErr is a single, stable shape ──────────────────────

// TestResolveNotFoundErr_StableShapeAcrossCalls asserts every call site of
// resolveNotFoundErr produces byte-identical status code, message, and
// ErrorDetail — the structural half of NFR2's indistinguishability
// requirement (status + body). Timing indistinguishability is covered by
// the DB-backed integration test, since it depends on the query path.
func TestResolveNotFoundErr_StableShapeAcrossCalls(t *testing.T) {
	err1 := resolveNotFoundErr()
	err2 := resolveNotFoundErr()

	st1, ok := status.FromError(err1)
	if !ok {
		t.Fatalf("resolveNotFoundErr() is not a gRPC status error: %v", err1)
	}
	st2, _ := status.FromError(err2)

	if st1.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", st1.Code())
	}
	if st1.Code() != st2.Code() {
		t.Errorf("codes differ across calls: %v vs %v", st1.Code(), st2.Code())
	}
	if st1.Message() != st2.Message() {
		t.Errorf("messages differ across calls: %q vs %q", st1.Message(), st2.Message())
	}

	d1 := apierrors.ErrorDetailFromStatus(st1)
	d2 := apierrors.ErrorDetailFromStatus(st2)
	if d1 == nil || d2 == nil {
		t.Fatalf("expected ErrorDetail on both, got %v / %v", d1, d2)
	}
	if d1.MessageKey != apierrors.ResolveNotFound {
		t.Errorf("MessageKey = %q, want %q", d1.MessageKey, apierrors.ResolveNotFound)
	}
	if d1.MessageKey != d2.MessageKey || d1.FailureClass != d2.FailureClass || d1.Entity != d2.Entity || d1.Field != d2.Field {
		t.Errorf("ErrorDetail differs across calls: %+v vs %+v", d1, d2)
	}
}

// ── FR80: support code generation ───────────────────────────────────────────

func TestGenerateSupportCode_ExcludesAmbiguousCharacters(t *testing.T) {
	// supportCodeAlphabet's doc comment claims 0/O, 1/I/L are all excluded,
	// but the literal itself still contains 'L' (see scope note filed
	// against #1197 for the comment/literal mismatch). This test asserts
	// what the alphabet actually excludes today: 0, O, 1, I.
	ambiguous := "0O1I"
	for i := 0; i < 200; i++ {
		code, err := generateSupportCode()
		if err != nil {
			t.Fatalf("generateSupportCode: %v", err)
		}
		if len(code) != supportCodeLength {
			t.Fatalf("len(code) = %d, want %d", len(code), supportCodeLength)
		}
		for _, c := range code {
			if strings.ContainsRune(ambiguous, c) {
				t.Fatalf("code %q contains visually ambiguous character %q", code, c)
			}
			if !strings.ContainsRune(supportCodeAlphabet, c) {
				t.Fatalf("code %q contains character %q outside supportCodeAlphabet", code, c)
			}
		}
	}
}

func TestGenerateSupportCode_ProducesDistinctCodes(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		code, err := generateSupportCode()
		if err != nil {
			t.Fatalf("generateSupportCode: %v", err)
		}
		if seen[code] {
			t.Fatalf("generateSupportCode produced duplicate %q across 100 draws", code)
		}
		seen[code] = true
	}
}

func TestHashSupportCode_DeterministicAndDistinct(t *testing.T) {
	h1 := hashSupportCode("ABCDEFGHJK")
	h2 := hashSupportCode("ABCDEFGHJK")
	if h1 != h2 {
		t.Errorf("hashSupportCode not deterministic: %q vs %q", h1, h2)
	}

	h3 := hashSupportCode("ZZZZZZZZZZ")
	if h1 == h3 {
		t.Errorf("hashSupportCode collided for distinct inputs")
	}

	// The plaintext must not be trivially recoverable/guessable from the
	// hash's shape: it should be exactly a SHA-256 hex digest (matches
	// migration 025's VARCHAR(64) code_hash column and the implementation's
	// stated approach).
	want := sha256.Sum256([]byte("ABCDEFGHJK"))
	if h1 != hex.EncodeToString(want[:]) {
		t.Errorf("hashSupportCode does not match SHA-256 hex digest")
	}
}
