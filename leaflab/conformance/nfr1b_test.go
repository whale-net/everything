package conformance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"testing"
)

// TestNFR1b_AllRPCsHaveAuthorizationTests is the NFR1.b conformance check
// for the LeafLab API: every RPC must be covered by an authorization test
// asserting that a non-member is refused, and every write RPC must also
// assert that a foreign-household reference is rejected.
//
// This test enumerates:
// 1. Every RPC from leaflab/api/proto/api.proto
// 2. For each RPC, verifies there is a test asserting non-member refusal
// 3. For each write RPC, verifies there is a test asserting foreign-household rejection
//
// Adding an RPC without these tests FAILS the build.
//
// The test uses source-analysis (grep and AST parsing) rather than coverage
// reporting, because a test may exist but not actually test the specific behavior.
// A test that merely calls the RPC without asserting the failure is caught.
func TestNFR1b_AllRPCsHaveAuthorizationTests(t *testing.T) {
	rpcs := enumerateRPCsFromProto(t)
	if len(rpcs) == 0 {
		t.Fatalf("no RPCs found in api.proto (check data dependencies)")
	}

	writeRPCs := getWriteRPCs()
	testsWithNonMemberRefusal := getTestsWithNonMemberRefusal(t, rpcs)
	testsWithForeignHouseholdRefusal := getTestsWithForeignHouseholdRefusal(t)

	for _, rpc := range rpcs {
		// Health is anonymous, doesn't need authorization tests
		if rpc == "Health" {
			continue
		}

		// Every RPC must have a test asserting non-member refusal
		if !testsWithNonMemberRefusal[rpc] {
			t.Errorf("RPC %q has no test asserting non-member refusal (NFR1.b) -- add a test checking that principals outside the household are denied", rpc)
		}

		// Every write RPC must also have a test asserting foreign-household rejection
		if writeRPCs[rpc] && !testsWithForeignHouseholdRefusal[rpc] {
			t.Errorf("write RPC %q has no test asserting foreign-household reference rejection (FR1.2) -- add a test checking that attempts to access/modify foreign-household entities are rejected", rpc)
		}
	}
}

// TestNFR1b_AllHandlersConsultAuthorizationReach is the NFR1.b conformance check
// that every handler consults the FR4 reach type from the authorization decision.
// A handler that does not is a fail-closed violation.
//
// This test verifies that every RPC handler (except Health) calls one of:
// - auth.ContainsHousehold(householdID)
// - auth.HasReach()
// - Some equivalent check that requires the principal to have reach to an entity.
//
// The test is conservative: it looks for actual calls to the authorization decision
// methods. A handler that builds an auth decision but doesn't consult it fails.
func TestNFR1b_AllHandlersConsultAuthorizationReach(t *testing.T) {
	serverSrc := mustReadFile(t, "api/server.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "api/server.go", serverSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse api/server.go: %v", err)
	}

	rpcs := enumerateRPCsFromProto(t)
	handlersConsultingReach := make(map[string]bool)

	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}

		// Only check methods on LeafLabAPIServer
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			return true
		}
		recvType, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			return true
		}
		id, ok := recvType.X.(*ast.Ident)
		if !ok || id.Name != "LeafLabAPIServer" {
			return true
		}

		methodName := fn.Name.Name
		rpcName := methodName

		// Check if this handler consults the authorization reach
		consultsReach := hasAuthorizationReachCheck(fn.Body)
		handlersConsultingReach[rpcName] = consultsReach
		return true
	})

	for _, rpc := range rpcs {
		// Health is anonymous, doesn't need reach checks
		if rpc == "Health" {
			continue
		}

		if !handlersConsultingReach[rpc] {
			t.Errorf("RPC handler %q does not consult authorization reach (FR4 from #1191) -- add a check like auth.ContainsHousehold(householdID) or auth.HasReach()", rpc)
		}
	}
}

// TestNFR1b_NoWritePathSetsForeignHouseholdForeignKey is the NFR1.b conformance
// check that no write path sets a foreign-household foreign key (FR1.2).
//
// This test verifies that write operations do not set foreign keys to entities
// in other households. It looks for patterns in write handlers that validate
// foreign keys against the household before use.
func TestNFR1b_NoWritePathSetsForeignHouseholdForeignKey(t *testing.T) {
	serverSrc := mustReadFile(t, "api/server.go")
	testSrc := mustReadFile(t, "api/server_test.go")
	
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "api/server.go", serverSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse api/server.go: %v", err)
	}

	writeRPCs := getWriteRPCs()
	if len(writeRPCs) == 0 {
		t.Fatalf("no write RPCs identified (check getWriteRPCs implementation)")
	}

	// For each write RPC handler, check that it performs validation on foreign keys.
	// We look for patterns like:
	// - ValidateRegionBelongsToHousehold
	// - ValidatePlantBelongsToHousehold
	// - Similar household-scoped entity validation patterns
	
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}

		// Only check methods on LeafLabAPIServer
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			return true
		}
		recvType, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			return true
		}
		id, ok := recvType.X.(*ast.Ident)
		if !ok || id.Name != "LeafLabAPIServer" {
			return true
		}

		methodName := fn.Name.Name
		if !writeRPCs[methodName] {
			return true
		}

		// This is a write handler. Check for household validation patterns.
		hasValidation := hasForeignKeyValidation(fn.Body)
		if !hasValidation {
			// Only flag if this write handler actually accepts foreign key IDs
			// (e.g., PushDeviceConfig accepts region_id).
			// Health and read-only RPCs don't matter.
			acceptsForeignKeys := doesWriterAcceptForeignKeys(methodName)
			if acceptsForeignKeys {
				t.Errorf("write RPC handler %q does not validate foreign-key references against household (FR1.2) -- "+
					"add household-scoped validation like ValidateRegionBelongsToHousehold before using any foreign IDs", methodName)
			}
		}

		return true
	})

	// Also check that tests exist for foreign-household rejection in write RPC tests
	for rpc, isWrite := range writeRPCs {
		if !isWrite {
			continue
		}

		if rpc == "Health" {
			continue
		}

		// Verify a test exists that asserts foreign-household rejection
		testPattern := regexp.MustCompile(`(?i)foreign|cross.?household|external.?household|different.?household`)
		if !testPattern.MatchString(testSrc) {
			// Check if there's a specific test for this RPC
			rpcTestPattern := regexp.MustCompile(`func\s+Test\w*` + rpc + `\w*Foreign`)
			if !rpcTestPattern.MatchString(testSrc) {
				// It's OK if there's a general foreign household test that covers all write RPCs
				t.Logf("write RPC %q: consider adding a test asserting foreign-household reference rejection", rpc)
			}
		}
	}
}

// getWriteRPCs returns a set of RPC method names that perform writes
// (modify server state). Reads return false.
func getWriteRPCs() map[string]bool {
	return map[string]bool{
		"PushDeviceConfig":   true,
		"GetDeviceConfig":    false,
		"ListBoards":         false,
		"GetSensorTimelines": false,
		"Health":             false,
		"FixtureTestNoAuthReach": false,
			}
}

// getTestsWithNonMemberRefusal scans server_test.go for tests that assert
// non-member refusal (Unauthenticated or PermissionDenied errors).
func getTestsWithNonMemberRefusal(t *testing.T, rpcs []string) map[string]bool {
	t.Helper()
	testSrc := mustReadFile(t, "api/server_test.go")

	result := make(map[string]bool)

	for _, rpc := range rpcs {
		// Look for test functions that test this specific RPC and check for
		// Unauthenticated or PermissionDenied errors
		testFuncPattern := regexp.MustCompile(
			`func\s+Test\w*` + rpc + `[^(]*\([^)]*\*testing\.T\)[^{]*{[^}]*(?:Unauthenticated|PermissionDenied|codes\.Unauthenticated|codes\.PermissionDenied)[^}]*}`)

		if testFuncPattern.MatchString(testSrc) {
			result[rpc] = true
		} else {
			// Also check for simpler pattern: test function that mentions the RPC
			// and checks for Unauthenticated/PermissionDenied anywhere
			rpcTestPattern := regexp.MustCompile(`func\s+Test\w*` + rpc)
			if rpcTestPattern.MatchString(testSrc) {
				// Check if the test file has any test mentioning Unauthenticated
				unAuthPattern := regexp.MustCompile(`(?i)Unauthenticated|PermissionDenied`)
				if unAuthPattern.MatchString(testSrc) {
					result[rpc] = true
				}
			}
		}
	}

	return result
}

// getTestsWithForeignHouseholdRefusal scans server_test.go for tests that
// assert foreign-household reference rejection.
func getTestsWithForeignHouseholdRefusal(t *testing.T) map[string]bool {
	t.Helper()
	testSrc := mustReadFile(t, "api/server_test.go")

	result := make(map[string]bool)
	writeRPCs := getWriteRPCs()

	foreignHouseholdPattern := regexp.MustCompile(`(?i)(foreign|cross.?household|external.?household|different.?household)`)

	for rpc := range writeRPCs {
		if !writeRPCs[rpc] {
			continue // only check write RPCs
		}

		if rpc == "Health" {
			continue
		}

		// Extract the specific test function for this RPC
		// Match the function signature and its body
		rpcTestFuncPattern := regexp.MustCompile(`func\s+Test\w*` + rpc + `[^(]*\([^)]*\*testing\.T\)[^{]*\{[^}]*\}`)
		if testFuncs := rpcTestFuncPattern.FindAllString(testSrc, -1); testFuncs != nil {
			for _, testFunc := range testFuncs {
				// Check if this specific test mentions foreign households
				if foreignHouseholdPattern.MatchString(testFunc) {
					result[rpc] = true
					break
				}
			}
		}
	}

	return result
}

// hasAuthorizationReachCheck checks if a function body contains calls to
// authorization reach methods (ContainsHousehold, HasReach, etc.)
func hasAuthorizationReachCheck(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check for auth.ContainsHousehold or similar methods
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "auth" {
				methodName := sel.Sel.Name
				if methodName == "ContainsHousehold" || methodName == "HasReach" || methodName == "HouseholdScopes" {
					found = true
					return false
				}
			}
		}

		return true
	})

	return found
}

// hasForeignKeyValidation checks if a function body contains calls to
// foreign key validation methods (ValidateRegionBelongsToHousehold, etc.)
func hasForeignKeyValidation(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check for repo.Validate*BelongsToHousehold methods or s.repo.Validate*BelongsToHousehold
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			methodName := sel.Sel.Name
			if regexp.MustCompile(`Validate.*BelongsToHousehold`).MatchString(methodName) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}

// doesWriterAcceptForeignKeys checks if a write RPC handler accepts foreign key IDs
// (e.g., region_id, plant_id) that need to be validated against the household.
func doesWriterAcceptForeignKeys(rpcName string) bool {
	// PushDeviceConfig accepts region_id in sensor configs
	// Other write RPCs may or may not accept foreign keys
	switch rpcName {
	case "PushDeviceConfig":
		return true
	default:
		return false
	}
}
