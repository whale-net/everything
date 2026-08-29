package conformance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// This file is NFR1.b's negative-fixture suite, the counterpart to
// negative_fixtures_test.go's NFR1.a/NFR18.1 coverage: table-driven tests
// proving the four checks in nfr1b_test.go actually catch violations,
// rather than only passing vacuously on the real tree. Every case here
// synthesizes small fixture source strings/registries and feeds them
// through the same violation-message functions the real checks use
// (registryCoverageOffenses, testCoverageOffenses,
// handlerFailClosedOffenses, foreignFKOffenses) -- it never edits the real
// tree. Each negative case asserts on the failure message, per the issue's
// Testing acceptance criteria:
//   - an RPC with no non-member test fails
//   - an RPC with no foreign-reference test fails
//   - a handler querying without a scope fails
//   - a write setting a foreign FK without the assertion fails
//
// (Plus registryCoverageOffenses's own "an RPC with no coverage at all"
// case, which the issue's Validation section separately calls out as the
// red/green demonstration.)

// TestNFR1b_RegistryCoverage_TableDriven covers "an RPC with no coverage
// fails the build": a new RPC added to api.proto with no
// rpcAuthzRegistrations entry.
func TestNFR1b_RegistryCoverage_TableDriven(t *testing.T) {
	const proto = `
syntax = "proto3";
package leaflab.api.v1;

service LeafLabAPI {
  rpc GetHealth(HealthRequest) returns (HealthReply);
  rpc GetBoards(BoardsRequest) returns (BoardsReply);
  rpc PushDeviceConfig(PushDeviceConfigRequest) returns (PushDeviceConfigReply);
}
`
	rpcs := enumerateRPCsFromProto(t, proto)
	if len(rpcs) != 3 {
		t.Fatalf("fixture setup: enumerateRPCsFromProto found %d RPCs, want 3 (%v)", len(rpcs), rpcs)
	}
	allowlist := []string{"GetHealth"}

	tests := []struct {
		name          string
		registry      map[string]rpcAuthzEntry
		wantOffenders []string
		wantClean     bool
	}{
		{
			name: "every non-allowlisted RPC registered: clean",
			registry: map[string]rpcAuthzEntry{
				"GetBoards":        {Kind: "read"},
				"PushDeviceConfig": {Kind: "write"},
			},
			wantClean: true,
		},
		{
			name: "new RPC added to the proto with no registry entry at all",
			registry: map[string]rpcAuthzEntry{
				"GetBoards": {Kind: "read"},
				// PushDeviceConfig deliberately missing.
			},
			wantOffenders: []string{"PushDeviceConfig"},
		},
		{
			name:          "no RPC registered: every non-allowlisted RPC flagged",
			registry:      map[string]rpcAuthzEntry{},
			wantOffenders: []string{"GetBoards", "PushDeviceConfig"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := registryCoverageOffenses(rpcs, allowlist, tc.registry)
			if tc.wantClean {
				if len(msgs) != 0 {
					t.Fatalf("registryCoverageOffenses returned %d offenses, want 0 (clean fixture should pass): %v", len(msgs), msgs)
				}
				return
			}
			if len(msgs) != len(tc.wantOffenders) {
				t.Fatalf("registryCoverageOffenses returned %d offenses, want %d: %v", len(msgs), len(tc.wantOffenders), msgs)
			}
			joined := strings.Join(msgs, "\n")
			for _, want := range tc.wantOffenders {
				if !strings.Contains(joined, "RPC "+want+" has no entry in leaflab/api/authz_registry.go's") {
					t.Errorf("offense messages do not name RPC %s as required; got:\n%s", want, joined)
				}
				if !strings.Contains(joined, "add one naming its Kind") {
					t.Errorf("offense message for %s is not actionable (must say to add a registry entry); got:\n%s", want, joined)
				}
			}
		})
	}
}

// TestNFR1b_NonMemberAndForeignRefTestCoverage_TableDriven covers "an RPC
// with no non-member test fails" and "an RPC with no foreign-reference
// test fails": registry entries missing NonMemberTest/ForeignRefTest, or
// naming a test function that doesn't actually exist in leaflab/api.
func TestNFR1b_NonMemberAndForeignRefTestCoverage_TableDriven(t *testing.T) {
	testFuncs := map[string]bool{
		"TestGetBoards_NonMember_Refused":             true,
		"TestPushDeviceConfig_ForeignRegion_Rejected": true,
	}

	tests := []struct {
		name        string
		registry    map[string]rpcAuthzEntry
		wantMessage string // "" means clean (no offense)
		wantClean   bool
	}{
		{
			name: "read RPC with a real NonMemberTest: clean",
			registry: map[string]rpcAuthzEntry{
				"GetBoards": {Kind: "read", NonMemberTest: "TestGetBoards_NonMember_Refused"},
			},
			wantClean: true,
		},
		{
			name: "write RPC with real NonMemberTest and ForeignRefTest: clean",
			registry: map[string]rpcAuthzEntry{
				"PushDeviceConfig": {
					Kind:             "write",
					ForeignRefFields: []string{"EntityRegion"},
					NonMemberTest:    "TestGetBoards_NonMember_Refused",
					ForeignRefTest:   "TestPushDeviceConfig_ForeignRegion_Rejected",
				},
			},
			wantClean: true,
		},
		{
			name: "RPC with no non-member test and no ScopeGapReason",
			registry: map[string]rpcAuthzEntry{
				"GetBoards": {Kind: "read"},
			},
			wantMessage: "RPC GetBoards's rpcAuthzRegistrations entry names no NonMemberTest",
		},
		{
			name: "RPC names a NonMemberTest that does not exist in leaflab/api",
			registry: map[string]rpcAuthzEntry{
				"GetBoards": {Kind: "read", NonMemberTest: "TestGetBoards_DoesNotExist"},
			},
			wantMessage: `RPC GetBoards's rpcAuthzRegistrations entry names NonMemberTest "TestGetBoards_DoesNotExist", but no such Test function exists`,
		},
		{
			name: "write RPC declares ForeignRefFields but names no ForeignRefTest",
			registry: map[string]rpcAuthzEntry{
				"PushDeviceConfig": {
					Kind:             "write",
					ForeignRefFields: []string{"EntityRegion"},
					NonMemberTest:    "TestGetBoards_NonMember_Refused",
				},
			},
			wantMessage: "RPC PushDeviceConfig declares ForeignRefFields [EntityRegion] but names no ForeignRefTest",
		},
		{
			name: "write RPC names a ForeignRefTest that does not exist in leaflab/api",
			registry: map[string]rpcAuthzEntry{
				"PushDeviceConfig": {
					Kind:             "write",
					ForeignRefFields: []string{"EntityRegion"},
					NonMemberTest:    "TestGetBoards_NonMember_Refused",
					ForeignRefTest:   "TestPushDeviceConfig_DoesNotExist",
				},
			},
			wantMessage: `RPC PushDeviceConfig's rpcAuthzRegistrations entry names ForeignRefTest "TestPushDeviceConfig_DoesNotExist", but no such Test function exists`,
		},
		{
			name: "ScopeGapReason with a tracked issue exempts NonMemberTest: clean",
			registry: map[string]rpcAuthzEntry{
				"PushDeviceConfig": {
					Kind:           "write",
					ScopeGapReason: "documented gap, see #1403",
				},
			},
			wantClean: true,
		},
		{
			name: "ScopeGapReason with no tracked issue reference fails",
			registry: map[string]rpcAuthzEntry{
				"PushDeviceConfig": {
					Kind:           "write",
					ScopeGapReason: "we'll get to it eventually",
				},
			},
			wantMessage: `RPC PushDeviceConfig's rpcAuthzRegistrations.ScopeGapReason "we'll get to it eventually" does not reference a tracked issue`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := testCoverageOffenses(tc.registry, testFuncs)
			if tc.wantClean {
				if len(msgs) != 0 {
					t.Fatalf("testCoverageOffenses returned %d offenses, want 0 (clean fixture should pass): %v", len(msgs), msgs)
				}
				return
			}
			if len(msgs) == 0 {
				t.Fatalf("testCoverageOffenses returned 0 offenses, want at least 1 containing %q", tc.wantMessage)
			}
			joined := strings.Join(msgs, "\n")
			if !strings.Contains(joined, tc.wantMessage) {
				t.Errorf("offense messages do not contain %q; got:\n%s", tc.wantMessage, joined)
			}
		})
	}
}

// parseHandlerBody parses src's single function declaration and returns
// its *ast.BlockStmt, for feeding into collectHandlerCallSites -- a
// fixture-scale stand-in for parseServerHandlerBodies that doesn't require
// the receiver to be named exactly LeafLabAPIServer.
func parseHandlerBody(t *testing.T, src string) *ast.BlockStmt {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture source: %v", err)
	}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
			return fd.Body
		}
	}
	t.Fatal("fixture source has no function declaration")
	return nil
}

// TestNFR1b_HandlerFailClosed_TableDriven covers "a handler querying
// without a scope fails": a handler whose first deviceRepository call
// precedes any leaflab/api/authz/ call, and the PreAuthzExemptRepoCalls
// carve-out that lets a specific, named repository call precede it
// anyway.
func TestNFR1b_HandlerFailClosed_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		exempt      []string
		wantMessage string // "" means clean
	}{
		{
			name: "authz call before repository call: clean",
			src: `package main
func (s *LeafLabAPIServer) GoodRPC(ctx context.Context, req *Request) (*Reply, error) {
	scope, err := s.authzSvc.ScopeForPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.GetThing(ctx, scope)
	return rows, err
}
`,
		},
		{
			name: "repository call before any authz call fails",
			src: `package main
func (s *LeafLabAPIServer) BadRPC(ctx context.Context, req *Request) (*Reply, error) {
	rows, err := s.repo.GetThing(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.authzSvc.ScopeForPrincipal(ctx)
	_ = scope
	return rows, err
}
`,
			wantMessage: "RPC BadRPC's handler calls repository method GetThing before any leaflab/api/authz/ call",
		},
		{
			name: "repository call before authz call, but named in PreAuthzExemptRepoCalls: clean",
			src: `package main
func (s *LeafLabAPIServer) ExemptRPC(ctx context.Context, req *Request) (*Reply, error) {
	board, err := s.repo.GetOrCreateBoard(ctx, req.DeviceID)
	if err != nil {
		return nil, err
	}
	ref, res, err := s.authzSvc.ResolveBoardByDeviceID(ctx, req.DeviceID)
	_ = ref
	_ = res
	return board, err
}
`,
			exempt: []string{"GetOrCreateBoard"},
		},
		{
			name: "repository call before authz call, exemption list names a different method: still fails",
			src: `package main
func (s *LeafLabAPIServer) ExemptRPC(ctx context.Context, req *Request) (*Reply, error) {
	board, err := s.repo.GetOrCreateBoard(ctx, req.DeviceID)
	return board, err
}
`,
			exempt:      []string{"SomeOtherMethod"},
			wantMessage: "RPC ExemptRPC's handler calls repository method GetOrCreateBoard before any leaflab/api/authz/ call",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := parseHandlerBody(t, tc.src)
			sites := collectHandlerCallSites(body)
			if len(sites) == 0 {
				t.Fatal("fixture setup: collectHandlerCallSites found no call sites")
			}
			exempt := map[string]bool{}
			for _, m := range tc.exempt {
				exempt[m] = true
			}
			// The RPC name isn't parsed out of the fixture (helper funcs
			// vary), so name it directly from the fixture's own func name
			// via a quick scan -- every fixture above declares exactly one
			// function whose name is what wantMessage expects.
			rpc := fixtureFuncName(t, tc.src)
			msgs := handlerFailClosedOffenses(rpc, sites, exempt)
			if tc.wantMessage == "" {
				if len(msgs) != 0 {
					t.Fatalf("handlerFailClosedOffenses returned %d offenses, want 0 (clean fixture should pass): %v", len(msgs), msgs)
				}
				return
			}
			if len(msgs) != 1 {
				t.Fatalf("handlerFailClosedOffenses returned %d offenses, want 1: %v", len(msgs), msgs)
			}
			if !strings.Contains(msgs[0], tc.wantMessage) {
				t.Errorf("offense message = %q, want a message containing %q", msgs[0], tc.wantMessage)
			}
		})
	}
}

// fixtureFuncName returns the name of src's single top-level function
// declaration, for TestNFR1b_HandlerFailClosed_TableDriven's fixtures.
func fixtureFuncName(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture source: %v", err)
	}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			return fd.Name.Name
		}
	}
	t.Fatal("fixture source has no function declaration")
	return ""
}

// TestNFR1b_ForeignFKAssertion_TableDriven covers "a write setting a
// foreign FK without the assertion fails": a registered ForeignRefFields
// entry with no reachable authz.AssertSameHousehold call site, plus the
// mirror-image case (an AssertSameHousehold call site for an entity kind
// the registry doesn't declare).
func TestNFR1b_ForeignFKAssertion_TableDriven(t *testing.T) {
	const src = `package main

import "github.com/whale-net/everything/leaflab/api/authz"

func (s *LeafLabAPIServer) GoodWrite(ctx context.Context, req *Request) (*Reply, error) {
	return s.validateRegion(ctx, req)
}

func (s *LeafLabAPIServer) validateRegion(ctx context.Context, req *Request) (*Reply, error) {
	ref := authz.LiveRef{EntityRef: authz.EntityRef{Kind: authz.EntityRegion, ID: req.RegionID}, Field: "region_id"}
	if err := authz.AssertSameHousehold(ctx, s.authzSvc, req.WriterHousehold, ref); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *LeafLabAPIServer) MissingAssertWrite(ctx context.Context, req *Request) (*Reply, error) {
	return nil, nil
}

func (s *LeafLabAPIServer) UndeclaredWrite(ctx context.Context, req *Request) (*Reply, error) {
	ref := authz.LiveRef{EntityRef: authz.EntityRef{Kind: authz.EntityBoard, ID: req.BoardID}, Field: "board_id"}
	if err := authz.AssertSameHousehold(ctx, s.authzSvc, req.WriterHousehold, ref); err != nil {
		return nil, err
	}
	return nil, nil
}
`

	funcs := parseAPIFuncDecls(t, map[string]string{"api/fixture.go": src})
	if len(funcs) == 0 {
		t.Fatal("fixture setup: parseAPIFuncDecls found no function declarations")
	}

	tests := []struct {
		name        string
		rpc         string
		declared    []string
		wantMessage string // "" means clean
	}{
		{
			name:     "declared ForeignRefFields matches a real AssertSameHousehold call site: clean",
			rpc:      "GoodWrite",
			declared: []string{"EntityRegion"},
		},
		{
			name:        "declared ForeignRefFields but handler never reaches AssertSameHousehold",
			rpc:         "MissingAssertWrite",
			declared:    []string{"EntityRegion"},
			wantMessage: "no authz.AssertSameHousehold call site is reachable from its handler",
		},
		{
			name:        "handler reaches AssertSameHousehold for a kind the registry doesn't declare",
			rpc:         "UndeclaredWrite",
			declared:    nil,
			wantMessage: "its rpcAuthzRegistrations entry declares no ForeignRefFields",
		},
		{
			name:        "declared kind doesn't match the entity kind actually asserted",
			rpc:         "UndeclaredWrite",
			declared:    []string{"EntityPlant"},
			wantMessage: "no authz.AssertSameHousehold call site reachable from its handler references that entity kind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			analysis := analyzeWritePath(tc.rpc, funcs)
			msgs := foreignFKOffenses(tc.rpc, tc.declared, analysis)
			if tc.wantMessage == "" {
				if len(msgs) != 0 {
					t.Fatalf("foreignFKOffenses returned %d offenses, want 0 (clean fixture should pass): %v", len(msgs), msgs)
				}
				return
			}
			if len(msgs) == 0 {
				t.Fatalf("foreignFKOffenses returned 0 offenses, want at least 1 containing %q", tc.wantMessage)
			}
			joined := strings.Join(msgs, "\n")
			if !strings.Contains(joined, tc.wantMessage) {
				t.Errorf("offense messages do not contain %q; got:\n%s", tc.wantMessage, joined)
			}
		})
	}
}
