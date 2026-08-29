package conformance

import (
	"strings"
	"testing"
)

// This file is NFR1.a and NFR18.1's negative-fixture suite: table-driven
// tests proving the conformance checks in auth_coverage_test.go and
// bff_policy_test.go actually catch violations, rather than only passing
// vacuously on the real tree. Every case here synthesizes small fixture
// source strings and feeds them through the same enumeration functions
// (enumerateRPCsFromProto, enumerateAnonymousAllowlist,
// authInterceptorWired, enumerateBFFRoutes) and the same violation-message
// functions (rpcCoverageOffenses, allowlistSizeOffense, bffRouteOffenses,
// nfr181Offenses) the real checks use -- it never edits the real tree.
// Each negative case asserts on the failure message, per the issue's
// Testing acceptance criteria, so a regression that still "detects
// something" but stops naming the offending RPC/route is itself caught.

// TestAuthCoverage_RPCCoverage_TableDriven is NFR1.a's RPC coverage
// negative fixture: a new unauthenticated RPC added to the proto without
// allowlist coverage, and the case where the authenticating interceptor
// chain has been removed entirely (authInterceptorWired's "wired" input
// goes false even though other RPCs are otherwise fine).
func TestAuthCoverage_RPCCoverage_TableDriven(t *testing.T) {
	const proto = `
syntax = "proto3";
package leaflab.api.v1;

service LeafLabAPI {
  rpc GetHealth(HealthRequest) returns (HealthReply);
  rpc GetBoards(BoardsRequest) returns (BoardsReply);
  rpc GetNewSensorData(NewSensorDataRequest) returns (NewSensorDataReply);
}
`
	rpcs := enumerateRPCsFromProto(t, proto)
	if len(rpcs) != 3 {
		t.Fatalf("fixture setup: enumerateRPCsFromProto found %d RPCs, want 3 (%v)", len(rpcs), rpcs)
	}

	tests := []struct {
		name          string
		allowlist     []string
		wired         bool
		wantOffenders []string // RPC names expected to appear (one substring match each) in the offense messages
		wantClean     bool     // true: no offenses at all (positive control)
	}{
		{
			name:      "allowlisted GetHealth plus wired interceptor: clean",
			allowlist: []string{"GetHealth"},
			wired:     true,
			wantClean: true,
		},
		{
			name:          "new RPC added with neither allowlist nor working interceptor",
			allowlist:     []string{"GetHealth"},
			wired:         false,
			wantOffenders: []string{"GetBoards", "GetNewSensorData"},
		},
		{
			name:          "interceptor chain removed entirely: every non-allowlisted RPC flagged, not just the new one",
			allowlist:     []string{"GetHealth"},
			wired:         false, // simulates NewAuthEnforcementUnaryInterceptor/StreamInterceptor no longer wired in main.go
			wantOffenders: []string{"GetBoards", "GetNewSensorData"},
		},
		{
			name:      "new RPC explicitly allowlisted alongside GetHealth: not itself flagged by this check (the exactly-one-entry check catches it separately)",
			allowlist: []string{"GetHealth", "GetNewSensorData"},
			wired:     false,
			// GetBoards is still uncovered -- allowlisting one RPC doesn't
			// exempt a sibling RPC that was never wired or allowlisted.
			wantOffenders: []string{"GetBoards"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := rpcCoverageOffenses(rpcs, tc.allowlist, tc.wired)
			if tc.wantClean {
				if len(msgs) != 0 {
					t.Fatalf("rpcCoverageOffenses returned %d offenses, want 0 (clean fixture should pass): %v", len(msgs), msgs)
				}
				return
			}
			if len(msgs) != len(tc.wantOffenders) {
				t.Fatalf("rpcCoverageOffenses returned %d offenses, want %d: %v", len(msgs), len(tc.wantOffenders), msgs)
			}
			joined := strings.Join(msgs, "\n")
			for _, want := range tc.wantOffenders {
				if !strings.Contains(joined, "RPC "+want+" is not in leaflab/api/auth.go's anonymousMethods allowlist") {
					t.Errorf("offense messages do not name RPC %s as required; got:\n%s", want, joined)
				}
				// The message must be actionable: it must say what to do
				// about the violation, not just that one exists.
				if !strings.Contains(joined, "wire the interceptor") || !strings.Contains(joined, "add "+want+" to anonymousMethods") {
					t.Errorf("offense message for %s is not actionable (must say to wire the interceptor or add the RPC to anonymousMethods); got:\n%s", want, joined)
				}
			}
		})
	}
}

// TestAuthCoverage_BFFRouteCoverage_TableDriven is NFR1.a's BFF route
// coverage negative fixture: a new route registered on the mux without
// being wrapped in app.auth.RequireAuthFunc, and not present in the
// enumerated public set.
func TestAuthCoverage_BFFRouteCoverage_TableDriven(t *testing.T) {
	tests := []struct {
		name          string
		src           string
		wantOffenders []string
		wantClean     bool
	}{
		{
			name: "every route authenticated or public: clean",
			src: `package main
func setupRoutes(mux *http.ServeMux, app *App) {
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.handleLogin)
	mux.HandleFunc("/boards", app.auth.RequireAuthFunc(app.handleBoards))
}
`,
			wantClean: true,
		},
		{
			name: "new BFF route registered without RequireAuthFunc wrapping and not on the public list",
			src: `package main
func setupRoutes(mux *http.ServeMux, app *App) {
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/boards", app.auth.RequireAuthFunc(app.handleBoards))
	mux.HandleFunc("/boards/export", app.handleBoardsExport)
}
`,
			wantOffenders: []string{"/boards/export"},
		},
		{
			name: "route wrapped in an intermediate helper that itself never calls RequireAuthFunc still flagged",
			src: `package main
func setupRoutes(mux *http.ServeMux, app *App) {
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/sensors/latest", app.auth.WithAccessToken(app.handleSensorsLatest))
}
`,
			wantOffenders: []string{"/sensors/latest"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			routes := enumerateBFFRoutes(t, tc.src)
			if len(routes) == 0 {
				t.Fatal("fixture setup: enumerateBFFRoutes found no routes")
			}
			msgs := bffRouteOffenses(routes, bffPublicRoutes)
			if tc.wantClean {
				if len(msgs) != 0 {
					t.Fatalf("bffRouteOffenses returned %d offenses, want 0 (clean fixture should pass): %v", len(msgs), msgs)
				}
				return
			}
			if len(msgs) != len(tc.wantOffenders) {
				t.Fatalf("bffRouteOffenses returned %d offenses, want %d: %v", len(msgs), len(tc.wantOffenders), msgs)
			}
			joined := strings.Join(msgs, "\n")
			for _, want := range tc.wantOffenders {
				if !strings.Contains(joined, `BFF route "`+want+`" is registered without app.auth.RequireAuthFunc`) {
					t.Errorf("offense messages do not name route %s as required; got:\n%s", want, joined)
				}
				if !strings.Contains(joined, "wrap it in app.auth.RequireAuthFunc") {
					t.Errorf("offense message for %s is not actionable (must say to wrap it in RequireAuthFunc); got:\n%s", want, joined)
				}
			}
		})
	}
}

// TestAuthCoverage_AllowlistShape_TableDriven is NFR1.a's
// anonymousMethods-allowlist-shape negative fixture: a second entry added
// to the allowlist alongside GetHealth must fail, and a single entry that
// isn't GetHealth must also fail (a different failure message, but still
// a failure).
func TestAuthCoverage_AllowlistShape_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		entries     []string
		wantMessage string // "" means clean (no offense)
	}{
		{
			name:    "exactly GetHealth: clean",
			entries: []string{"GetHealth"},
		},
		{
			name:        "second entry added alongside GetHealth",
			entries:     []string{"GetHealth", "GetBoards"},
			wantMessage: "allowlist has 2 entries",
		},
		{
			name:        "empty allowlist",
			entries:     nil,
			wantMessage: "allowlist has 0 entries",
		},
		{
			name:        "single entry that isn't GetHealth",
			entries:     []string{"GetBoards"},
			wantMessage: `sole entry is "GetBoards", want "GetHealth"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := allowlistSizeOffense(tc.entries)
			if tc.wantMessage == "" {
				if msg != "" {
					t.Fatalf("allowlistSizeOffense(%v) = %q, want \"\" (clean fixture should pass)", tc.entries, msg)
				}
				return
			}
			if !strings.Contains(msg, tc.wantMessage) {
				t.Fatalf("allowlistSizeOffense(%v) = %q, want a message containing %q", tc.entries, msg, tc.wantMessage)
			}
		})
	}
}

// TestBFFPolicy_NFR181_TableDriven is NFR18.1's negative fixture: a
// rounding/coarsening/suppression/label-selection helper added to
// leaflab/ui source must fail, naming the offending file and pattern.
func TestBFFPolicy_NFR181_TableDriven(t *testing.T) {
	tests := []struct {
		name          string
		files         map[string]string
		wantOffenders []string // substrings expected in the joined offense messages
		wantClean     bool
	}{
		{
			name: "ordinary handler source: clean",
			files: map[string]string{
				"ui/boards.go": `package ui

func (a *App) handleBoards(w http.ResponseWriter, r *http.Request) {
	boards := a.client.GetBoards(r.Context())
	renderBoards(w, boards)
}
`,
			},
			wantClean: true,
		},
		{
			name: "math.Round added to a BFF handler",
			files: map[string]string{
				"ui/boards.go": `package ui

func formatTemp(v float64) float64 {
	return math.Round(v)
}
`,
			},
			wantOffenders: []string{"ui/boards.go", `\bmath\.Round\b`},
		},
		{
			name: "coarsening helper added under ui/components",
			files: map[string]string{
				"ui/components/format.go": `package components

func coarsenReading(v float64) float64 {
	return v
}
`,
			},
			wantOffenders: []string{"ui/components/format.go"},
		},
		{
			name: "pattern only appears in a _test.go fixture: excluded, clean",
			files: map[string]string{
				"ui/format_test.go": `package ui

// this file's own fixture text mentions math.Round but must not trip the check
const example = "math.Round(1.2)"
`,
			},
			wantClean: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := nfr181Offenses(tc.files)
			if tc.wantClean {
				if len(msgs) != 0 {
					t.Fatalf("nfr181Offenses returned %d offenses, want 0 (clean fixture should pass): %v", len(msgs), msgs)
				}
				return
			}
			if len(msgs) == 0 {
				t.Fatalf("nfr181Offenses returned 0 offenses, want at least 1 for fixture %q", tc.name)
			}
			joined := strings.Join(msgs, "\n")
			for _, want := range tc.wantOffenders {
				if !strings.Contains(joined, want) {
					t.Errorf("offense messages do not contain %q as required; got:\n%s", want, joined)
				}
			}
			if !strings.Contains(joined, "move this shaping into leaflab-api instead") {
				t.Errorf("offense message is not actionable (must say to move the shaping into leaflab-api); got:\n%s", joined)
			}
		})
	}
}
