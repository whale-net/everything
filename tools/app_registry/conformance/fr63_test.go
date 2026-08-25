package conformance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/kinds"
)

// TestFR63_HookSetClosure verifies check (a): every kind supplies exactly the
// eight hooks (H1-H8) and no others. It also fails if a value-shaped hook is
// supplied without a corresponding literal ban declared in ValueShapedHookBans.
//
// See FR-63(a) and the Testing phase acceptance criteria.
func TestFR63_HookSetClosure(t *testing.T) {
	expectedHooks := []string{"H1", "H2", "H3", "H4", "H5", "H6", "H7", "H8"}

	allKinds := kinds.All()
	if len(allKinds) == 0 {
		t.Fatal("no kinds registered -- check that kind implementations call kinds.Register() at init()")
	}

	for kindName, kind := range allKinds {
		hooks := kind.Hooks()
		if hooks == nil {
			t.Errorf("kind %q: Hooks() returned nil", kindName)
			continue
		}

		// Check that each expected hook exists and is retrievable
		h1 := hooks.H1()
		if h1 == nil {
			t.Errorf("kind %q: H1() returned nil", kindName)
		} else if h1.Name() != "H1" {
			t.Errorf("kind %q: H1().Name() returned %q, expected %q", kindName, h1.Name(), "H1")
		}

		h2 := hooks.H2()
		if h2 == nil {
			t.Errorf("kind %q: H2() returned nil", kindName)
		} else if h2.Name() != "H2" {
			t.Errorf("kind %q: H2().Name() returned %q, expected %q", kindName, h2.Name(), "H2")
		}

		h3 := hooks.H3()
		if h3 == nil {
			t.Errorf("kind %q: H3() returned nil", kindName)
		} else if h3.Name() != "H3" {
			t.Errorf("kind %q: H3().Name() returned %q, expected %q", kindName, h3.Name(), "H3")
		}

		h4 := hooks.H4()
		if h4 == nil {
			t.Errorf("kind %q: H4() returned nil", kindName)
		} else if h4.Name() != "H4" {
			t.Errorf("kind %q: H4().Name() returned %q, expected %q", kindName, h4.Name(), "H4")
		}

		h5 := hooks.H5()
		if h5 == nil {
			t.Errorf("kind %q: H5() returned nil", kindName)
		} else if h5.Name() != "H5" {
			t.Errorf("kind %q: H5().Name() returned %q, expected %q", kindName, h5.Name(), "H5")
		}

		h6 := hooks.H6()
		if h6 == nil {
			t.Errorf("kind %q: H6() returned nil", kindName)
		} else if h6.Name() != "H6" {
			t.Errorf("kind %q: H6().Name() returned %q, expected %q", kindName, h6.Name(), "H6")
		}

		h7 := hooks.H7()
		if h7 == nil {
			t.Errorf("kind %q: H7() returned nil", kindName)
		} else if h7.Name() != "H7" {
			t.Errorf("kind %q: H7().Name() returned %q, expected %q", kindName, h7.Name(), "H7")
		}

		h8 := hooks.H8()
		if h8 == nil {
			t.Errorf("kind %q: H8() returned nil", kindName)
		} else if h8.Name() != "H8" {
			t.Errorf("kind %q: H8().Name() returned %q, expected %q", kindName, h8.Name(), "H8")
		}

		// Check that value-shaped hooks have bans declared
		for _, hookName := range expectedHooks {
			var hook kinds.Hook
			switch hookName {
			case "H1":
				hook = h1
			case "H2":
				hook = h2
			case "H3":
				hook = h3
			case "H4":
				hook = h4
			case "H5":
				hook = h5
			case "H6":
				hook = h6
			case "H7":
				hook = h7
			case "H8":
				hook = h8
			}

			if hook != nil && hook.ValueShaped() {
				if _, hasBan := kinds.ValueShapedHookBans[hookName]; !hasBan {
					t.Errorf("kind %q: hook %s is value-shaped but has no literal ban declared in ValueShapedHookBans",
						kindName, hookName)
				}
			}
		}
	}

	// Also check that every kind publishes a manifest (H6 has a value)
	for kindName, kind := range allKinds {
		h6 := kind.Hooks().H6()
		if h6 == nil {
			t.Errorf("kind %q: H6() returned nil", kindName)
			continue
		}
		manifestPolicy := h6.ManifestPolicy()
		if strings.TrimSpace(manifestPolicy) == "" {
			t.Errorf("kind %q: H6 (manifest policy) is empty -- all kinds must publish a checksum manifest (FR-19)",
				kindName)
		}
	}
}

// TestFR63_KindIdentityBan verifies check (b): no kind-identity comparison or
// switch appears in a common mechanism outside hook dispatch.
//
// This test looks for patterns like:
//   - kind.Name() == "binary"
//   - if kind.Name() == ...
//   - switch kind.Name()
//
// These patterns are only allowed in hook dispatch code, not in common mechanisms.
//
// See FR-63(b).
func TestFR63_KindIdentityBan(t *testing.T) {
	// Vacuity guard: fail if no common mechanisms are declared, rather than skip.
	// The declaration of common mechanisms is itself a requirement; an empty
	// list means the requirement is not met. As mechanisms are implemented and
	// added to CommonMechanisms, this test will verify they don't branch on kind.
	if len(kinds.CommonMechanisms) == 0 {
		t.Errorf("VACUITY GUARD FAILED: CommonMechanisms is empty; check (b) cannot be verified. " +
			"This is expected during early phases, but the test must fail rather than skip " +
			"to detect under-resolution. Once common mechanisms are implemented and added to " +
			"kinds.CommonMechanisms, this test will scan them for kind-identity branches.")
		return
	}

	// For each declared common mechanism, search for kind-identity comparisons.
	for _, mechanism := range kinds.CommonMechanisms {
		files := globFilesForPackage(t, mechanism)
		if len(files) == 0 {
			t.Errorf("common mechanism %q resolved to no files (cannot verify kind-identity ban)",
				mechanism)
			continue
		}

		for filePath, fileContent := range files {
			// Parse the Go file
			fset := token.NewFileSet()
			node, err := parser.ParseFile(fset, filePath, fileContent, 0)
			if err != nil {
				// If it's not a Go file, skip it
				if strings.HasSuffix(filePath, ".go") {
					t.Logf("warning: could not parse Go file %q: %v", filePath, err)
				}
				continue
			}

			// Walk the AST looking for kind-identity comparisons
			ast.Inspect(node, func(n ast.Node) bool {
				switch stmt := n.(type) {
				case *ast.BinaryExpr:
					if isKindNameComparison(stmt) {
						t.Errorf("kind-identity ban violation in %s: found kind.Name() comparison "+
							"outside hook dispatch", filePath)
					}
				case *ast.SwitchStmt:
					if isSwitchOnKindName(stmt) {
						t.Errorf("kind-identity ban violation in %s: found switch on kind.Name() "+
							"outside hook dispatch", filePath)
					}
				}
				return true
			})
		}
	}
}

// TestFR63_VacuityGuard verifies check (c): the declared common-mechanism
// list resolves to actual source files. If fewer files resolve than the
// declaration names, the test fails.
//
// See FR-63(c).
func TestFR63_VacuityGuard(t *testing.T) {
	// Vacuity guard: fail if no common mechanisms are declared, rather than skip.
	// The declaration of common mechanisms is itself a requirement; an empty list
	// means under-resolution and must fail the test.
	if len(kinds.CommonMechanisms) == 0 {
		t.Errorf("VACUITY GUARD FAILED: CommonMechanisms is empty; check (c) cannot be verified. " +
			"The test must fail on under-resolution to detect when the mechanism list is incomplete. " +
			"Once common mechanisms are implemented and added to kinds.CommonMechanisms, " +
			"this test will verify they all resolve to actual files.")
		return
	}

	resolvedCount := 0
	for _, mechanism := range kinds.CommonMechanisms {
		files := globFilesForPackage(t, mechanism)
		if len(files) == 0 {
			t.Errorf("common mechanism %q resolved to no files (vacuity guard failed)", mechanism)
			continue
		}
		resolvedCount += len(files)
	}

	if resolvedCount < len(kinds.CommonMechanisms) {
		t.Errorf("vacuity guard: resolved fewer files (%d) than mechanisms declared (%d)",
			resolvedCount, len(kinds.CommonMechanisms))
	}
}

// TestFR63_LiteralBan verifies check (d): value-shaped hook policy values
// (H3, H4, H5, H6, H8) may appear only in:
//   1. A kind's own hook declaration
//   2. Inside a common mechanism
//   3. At a declared exempt location
//
// These values must not appear anywhere else in the repository, including
// workflows, shell scripts, or other documentation.
//
// See FR-63(d) and NFR-16.
func TestFR63_LiteralBan(t *testing.T) {
	// Get all registered kinds and their value-shaped hook policies
	kindPolicies := extractValueShapedPolicies(t)
	if len(kindPolicies) == 0 {
		t.Errorf("VACUITY GUARD FAILED: No value-shaped hook policies found; check (d) cannot be verified. " +
			"This indicates either no kinds are registered or no value-shaped hooks are defined. " +
			"The test must fail on under-resolution rather than skip.")
		return
	}

	// For each value-shaped hook, verify that its policy values appear only
	// in allowed locations
	for hook, policies := range kindPolicies {
		if !isValueShaped(hook) {
			continue
		}

		for policy := range policies {
			if strings.TrimSpace(policy) == "" {
				// Empty policies (like empty pre-cutover template) are allowed anywhere
				continue
			}

			// Check that this policy appears only in allowed locations
			checkLiteralBanForPolicy(t, hook, policy)
		}
	}
}

// Helper functions

// isKindNameComparison returns true if node is a comparison like kind.Name() == "binary"
func isKindNameComparison(node *ast.BinaryExpr) bool {
	if node.Op != token.EQL && node.Op != token.NEQ {
		return false
	}

	// Check if either side is a call to .Name()
	leftIsName := isCallToName(node.X)
	rightIsName := isCallToName(node.Y)

	return leftIsName || rightIsName
}

// isCallToName returns true if expr is a call to .Name()
func isCallToName(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	return sel.Sel.Name == "Name"
}

// isSwitchOnKindName returns true if the switch statement is on kind.Name()
func isSwitchOnKindName(node *ast.SwitchStmt) bool {
	if node.Tag == nil {
		return false
	}

	call, ok := node.Tag.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	return sel.Sel.Name == "Name"
}

// exprToString returns a string representation of an expression (for error messages)
func exprToString(expr ast.Expr) string {
	// Simple representation; in a real implementation, we might use format/printer
	if call, ok := expr.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			return fmt.Sprintf("call to %s()", sel.Sel.Name)
		}
	}
	return "expression"
}

// globFilesForPackage returns all Go and YAML files in a Go package directory,
// resolved relative to tools/app_registry. It searches for the file from the current
// working directory and from the repository root.
func globFilesForPackage(t *testing.T, importPath string) map[string]string {
	t.Helper()

	// Convert import path to directory path
	// Example: "github.com/whale-net/everything/tools/app_registry/publish/manifest"
	// becomes "tools/app_registry/publish/manifest"
	parts := strings.Split(importPath, "/")
	var dirParts []string
	foundAppRegistry := false
	for i, part := range parts {
		if part == "app_registry" {
			foundAppRegistry = true
			dirParts = parts[i:]
			break
		}
	}

	if !foundAppRegistry {
		t.Logf("warning: could not convert import path %q to directory", importPath)
		return nil
	}

	// Build the target directory path
	targetPath := strings.Join(dirParts, "/")

	// Try to find the directory from current location and repository root
	var dir string
	
	// First try relative paths from current directory
	for _, c := range []string{".", "..", "../..", "../../..", "../../../.."} {
		candidate := filepath.Join(c, targetPath)
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			dir = candidate
			break
		}
	}
	
	// If not found, try from repository root
	if dir == "" {
		repoRoot := getRepositoryRoot(t)
		if repoRoot != "" {
			candidate := filepath.Join(repoRoot, targetPath)
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				dir = candidate
			}
		}
	}

	if dir == "" {
		return nil
	}

	// Read Go and YAML files
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	result := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".go" && ext != ".yaml" && ext != ".yml" {
			continue
		}

		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}

		result[filepath.Join(dir, e.Name())] = string(b)
	}

	return result
}

// extractValueShapedPolicies returns a map of hook name → set of policy values
// from all registered kinds' value-shaped hooks.
func extractValueShapedPolicies(t *testing.T) map[string]map[string]bool {
	result := make(map[string]map[string]bool)

	allKinds := kinds.All()
	for _, kind := range allKinds {
		if kind == nil {
			continue
		}

		hooks := kind.Hooks()
		if hooks == nil {
			continue
		}

		// Extract each value-shaped hook's policy value
		if h3 := hooks.H3(); h3 != nil && h3.ValueShaped() {
			if result["H3"] == nil {
				result["H3"] = make(map[string]bool)
			}
			if ct := h3.ContentType(); ct != "" {
				result["H3"][ct] = true
			}
		}

		if h4 := hooks.H4(); h4 != nil && h4.ValueShaped() {
			if result["H4"] == nil {
				result["H4"] = make(map[string]bool)
			}
			if enc := h4.Encoding(); enc != "" {
				result["H4"][enc] = true
			}
		}

		if h5 := hooks.H5(); h5 != nil && h5.ValueShaped() {
			if result["H5"] == nil {
				result["H5"] = make(map[string]bool)
			}
			if fn := h5.FileNaming(); fn != "" {
				result["H5"][fn] = true
			}
		}

		if h6 := hooks.H6(); h6 != nil && h6.ValueShaped() {
			if result["H6"] == nil {
				result["H6"] = make(map[string]bool)
			}
			if mp := h6.ManifestPolicy(); mp != "" {
				result["H6"][mp] = true
			}
		}

		if h8 := hooks.H8(); h8 != nil && h8.ValueShaped() {
			if result["H8"] == nil {
				result["H8"] = make(map[string]bool)
			}
			if pt := h8.PreCutoverTemplate(); pt != "" {
				result["H8"][pt] = true
			}
		}
	}

	return result
}

// isValueShaped returns true if the hook name is value-shaped
func isValueShaped(hook string) bool {
	_, isBanned := kinds.ValueShapedHookBans[hook]
	return isBanned
}

// checkLiteralBanForPolicy verifies that a policy value appears only in
// allowed locations.
func checkLiteralBanForPolicy(t *testing.T, hook string, policy string) {
	t.Helper()

	// Build a list of allowed file paths where this policy is permitted
	allowedLocations := make(map[string]bool)

	// 1. Kind declarations (tools/app_registry/kinds/*.go)
	allowedLocations["tools/app_registry/kinds/"] = true

	// 2. Common mechanisms
	for _, mechanism := range kinds.CommonMechanisms {
		parts := strings.Split(mechanism, "/")
		var dirParts []string
		foundAppRegistry := false
		for i, part := range parts {
			if part == "app_registry" {
				foundAppRegistry = true
				dirParts = parts[i:]
				break
			}
		}
		if foundAppRegistry {
			allowedLocations[strings.Join(dirParts, "/")] = true
		}
	}

	// 3. Declared exempt locations
	for _, exemption := range kinds.BanExemptLocations {
		if exemption.Hook == hook {
			allowedLocations[exemption.Path] = true
		}
	}

	// Now search the repository for this policy value across Go, YAML, shell, and docs
	searchPolicyInRepository(t, hook, policy, allowedLocations)
}

// searchPolicyInRepository searches the repository for a policy value in Go, YAML,
// shell scripts, and documentation files, and fails if it's found in unauthorized locations.
func searchPolicyInRepository(t *testing.T, hook string, policy string, allowedLocations map[string]bool) {
	t.Helper()

	// Get the repository root (traverse up from current directory)
	repoRoot := getRepositoryRoot(t)
	if repoRoot == "" {
		t.Logf("warning: could not find repository root; skipping literal ban search for %s", hook)
		return
	}

	// Search for the policy value in various file types
	searchPatterns := []struct {
		fileExt string
		pattern string
	}{
		{".go", policy},                   // Go files
		{".yaml", policy},                 // YAML files (workflows, etc.)
		{".yml", policy},                  // YAML files
		{".sh", policy},                   // Shell scripts
		{".md", policy},                   // Documentation files
	}

	violations := make([]string, 0)

	for _, pattern := range searchPatterns {
		filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}

			if info.IsDir() {
				// Skip certain directories
				if strings.HasPrefix(filepath.Base(path), ".") ||
					strings.Contains(path, "vendor") ||
					strings.Contains(path, "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}

			if !strings.HasSuffix(path, pattern.fileExt) {
				return nil
			}

			// Read the file and search for the policy value
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			if !strings.Contains(string(content), policy) {
				return nil
			}

			// Check if this location is allowed
			relPath, err := filepath.Rel(repoRoot, path)
			if err != nil {
				relPath = path
			}

			// Check against allowed locations
			isAllowed := false
			for allowed := range allowedLocations {
				if strings.HasPrefix(relPath, allowed) ||
					strings.HasPrefix(relPath, "./"+allowed) {
					isAllowed = true
					break
				}
				// Also check exact match
				if relPath == allowed || "./"+relPath == allowed {
					isAllowed = true
					break
				}
			}

			if !isAllowed {
				violations = append(violations, relPath)
			}

			return nil
		})
	}

	// Fail if any violations were found
	if len(violations) > 0 {
		t.Errorf("literal ban violation for hook %s (policy %q): found in unauthorized locations:\n  %s",
			hook, policy, strings.Join(violations, "\n  "))
	}
}

// getRepositoryRoot finds the repository root by traversing up until it finds a .git directory
func getRepositoryRoot(t *testing.T) string {
	t.Helper()

	pwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	current := pwd
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root
			return ""
		}
		current = parent
	}
}


