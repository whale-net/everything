package conformance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is NFR1.b's fail-closed household-scope conformance check:
// "an endpoint with no explicit household-scope check denies, and no write
// path may set a foreign-household foreign key. Every RPC is covered by an
// authorization test asserting that a non-member is refused and that a
// foreign-household reference is rejected (FR1.2); adding an RPC without
// one fails the build." It extends this package's conformance_test target
// (see paths_test.go's package doc comment and this package's BUILD.bazel)
// rather than forking a second one, per the issue's instruction.
//
// It reads leaflab/api's rpcAuthzRegistrations (authz_registry.go) --
// parsed as source text via go/ast, never imported: leaflab/api is package
// main, not importable as a library, exactly like paths_test.go's
// enumerateAnonymousAllowlist reads auth.go's anonymousMethods -- as the
// source-of-truth for what each RPC is declared to do, and cross-checks
// that declaration against the real source (test function names in
// leaflab/api, and server.go's handler/AssertSameHousehold call sites),
// mirroring tools/app_registry/conformance/'s source-analysis pattern.
//
// Four independent assertions, run as four separate Test funcs so a
// failure in one names exactly what's missing without masking the others:
//   - TestNFR1b_EveryRPCHasRegistryCoverage: every RPC declared in
//     api.proto (other than the FR63/NFR1.a anonymous allowlist) has an
//     rpcAuthzRegistrations entry.
//   - TestNFR1b_EveryRPCHasNonMemberAndForeignReferenceTests: every
//     registered RPC's entry names a NonMemberTest (or a documented
//     ScopeGapReason exempting it) and, when it declares
//     ForeignRefFields, a ForeignRefTest -- and both named tests actually
//     exist in leaflab/api.
//   - TestNFR1b_EveryHandlerObtainsScopeBeforeTouchingRepository: every
//     registered RPC's handler in server.go reaches a leaflab/api/authz/
//     call before its first (non-exempt) deviceRepository call, in
//     source order.
//   - TestNFR1b_EveryForeignFKWriteAssertsSameHousehold: every entity kind
//     named in ForeignRefFields has a matching authz.AssertSameHousehold
//     call site reachable from that RPC's handler, and vice versa.

// rpcAuthzEntry is one leaflab/api/authz_registry.go rpcAuthzRegistrations
// entry, parsed from source by parseRPCAuthzRegistrations. Field meanings
// mirror leaflab/api/authz_registry.go's rpcAuthzRegistration doc comment
// exactly -- see that type for the authoritative description of each.
type rpcAuthzEntry struct {
	Kind                    string   // "read" or "write" (rpcKindRead/rpcKindWrite's suffix, lowercased)
	ForeignRefFields        []string // bare authz.EntityKind identifier names, e.g. "EntityRegion"
	NonMemberTest           string
	ForeignRefTest          string
	ScopeGapReason          string
	PreAuthzExemptRepoCalls []string
}

// parseRPCAuthzRegistrations parses src (leaflab/api/authz_registry.go's
// contents) for the `var rpcAuthzRegistrations = map[string]
// rpcAuthzRegistration{ ... }` literal and returns one rpcAuthzEntry per
// map entry, keyed by RPC short name. Uses go/ast (not regex) because the
// registry's value type has nested composite literals (ForeignRefFields'
// []authz.EntityKind, PreAuthzExemptRepoCalls' []string) a line-oriented
// regex can't reliably delimit.
func parseRPCAuthzRegistrations(t *testing.T, src string) map[string]rpcAuthzEntry {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "authz_registry.go", src, 0)
	if err != nil {
		t.Fatalf("parse authz_registry.go: %v", err)
	}

	var mapLit *ast.CompositeLit
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "rpcAuthzRegistrations" || len(vs.Values) != 1 {
				continue
			}
			cl, ok := vs.Values[0].(*ast.CompositeLit)
			if ok {
				mapLit = cl
			}
		}
	}
	if mapLit == nil {
		t.Fatal("no `var rpcAuthzRegistrations = map[string]rpcAuthzRegistration{ ... }` literal found in " +
			"authz_registry.go -- check the api:conformance_srcs data dependency in BUILD.bazel or the file's shape")
		return nil
	}

	out := map[string]rpcAuthzEntry{}
	for _, elt := range mapLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyLit, ok := kv.Key.(*ast.BasicLit)
		if !ok || keyLit.Kind != token.STRING {
			t.Fatalf("rpcAuthzRegistrations key %v is not a string literal", kv.Key)
			continue
		}
		rpcName, err := strconv.Unquote(keyLit.Value)
		if err != nil {
			t.Fatalf("unquote rpcAuthzRegistrations key %s: %v", keyLit.Value, err)
		}
		entryLit, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("rpcAuthzRegistrations[%q] value is not a composite literal", rpcName)
			continue
		}
		out[rpcName] = parseRPCAuthzEntry(t, rpcName, entryLit)
	}
	return out
}

// parseRPCAuthzEntry parses one rpcAuthzRegistration composite literal's
// fields into an rpcAuthzEntry. Unrecognized fields are ignored (forward
// compatible with fields this check doesn't yet use).
func parseRPCAuthzEntry(t *testing.T, rpcName string, lit *ast.CompositeLit) rpcAuthzEntry {
	t.Helper()
	var entry rpcAuthzEntry
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		fieldName, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch fieldName.Name {
		case "Kind":
			ident, ok := kv.Value.(*ast.Ident)
			if !ok {
				t.Fatalf("rpcAuthzRegistrations[%q].Kind is not a bare identifier", rpcName)
				continue
			}
			entry.Kind = strings.ToLower(strings.TrimPrefix(ident.Name, "rpcKind"))
		case "ForeignRefFields":
			entry.ForeignRefFields = parseEntityKindSlice(t, rpcName, "ForeignRefFields", kv.Value)
		case "PreAuthzExemptRepoCalls":
			entry.PreAuthzExemptRepoCalls = parseStringSlice(t, rpcName, "PreAuthzExemptRepoCalls", kv.Value)
		case "NonMemberTest":
			entry.NonMemberTest = parseStringLit(t, rpcName, "NonMemberTest", kv.Value)
		case "ForeignRefTest":
			entry.ForeignRefTest = parseStringLit(t, rpcName, "ForeignRefTest", kv.Value)
		case "ScopeGapReason":
			entry.ScopeGapReason = parseStringLit(t, rpcName, "ScopeGapReason", kv.Value)
		}
	}
	return entry
}

func parseStringLit(t *testing.T, rpcName, field string, expr ast.Expr) string {
	t.Helper()
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		t.Fatalf("rpcAuthzRegistrations[%q].%s is not a string literal", rpcName, field)
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatalf("unquote rpcAuthzRegistrations[%q].%s: %v", rpcName, field, err)
	}
	return s
}

func parseStringSlice(t *testing.T, rpcName, field string, expr ast.Expr) []string {
	t.Helper()
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("rpcAuthzRegistrations[%q].%s is not a composite literal", rpcName, field)
		return nil
	}
	var out []string
	for _, elt := range cl.Elts {
		out = append(out, parseStringLit(t, rpcName, field, elt))
	}
	return out
}

func parseEntityKindSlice(t *testing.T, rpcName, field string, expr ast.Expr) []string {
	t.Helper()
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		t.Fatalf("rpcAuthzRegistrations[%q].%s is not a composite literal", rpcName, field)
		return nil
	}
	var out []string
	for _, elt := range cl.Elts {
		sel, ok := elt.(*ast.SelectorExpr)
		if !ok {
			t.Fatalf("rpcAuthzRegistrations[%q].%s entry %v is not an authz.EntityXxx selector expression", rpcName, field, elt)
			continue
		}
		out = append(out, sel.Sel.Name)
	}
	return out
}

// TestNFR1b_EveryRPCHasRegistryCoverage is NFR1.b's first-line coverage
// assertion: every RPC declared in leaflab/api/proto/api.proto's
// LeafLabAPI service, other than the FR63/NFR1.a anonymous allowlist
// (GetHealth), must have an entry in leaflab/api/authz_registry.go's
// rpcAuthzRegistrations. A newly added RPC with no entry fails the build,
// naming the offending RPC.
func TestNFR1b_EveryRPCHasRegistryCoverage(t *testing.T) {
	proto := mustReadFile(t, "api/proto/api.proto")
	rpcs := enumerateRPCsFromProto(t, proto)
	if len(rpcs) == 0 {
		// Guards the guard: see auth_coverage_test.go's identical check.
		t.Fatal("found no RPCs in leaflab.api.v1.LeafLabAPI -- check the api/proto:proto_file data dependency in BUILD.bazel")
	}

	authGoSrc := mustReadFile(t, "api/auth.go")
	allowlist := enumerateAnonymousAllowlist(t, authGoSrc)

	registrySrc := mustReadFile(t, "api/authz_registry.go")
	registry := parseRPCAuthzRegistrations(t, registrySrc)
	if len(registry) == 0 {
		t.Fatal("found no entries in leaflab/api/authz_registry.go's rpcAuthzRegistrations -- check the " +
			"api:conformance_srcs data dependency in BUILD.bazel or authz_registry.go's shape")
	}

	for _, msg := range registryCoverageOffenses(rpcs, allowlist, registry) {
		t.Error(msg)
	}
}

// registryCoverageOffenses computes TestNFR1b_EveryRPCHasRegistryCoverage's
// failures, factored out like auth_coverage_test.go's rpcCoverageOffenses
// so a negative fixture can assert on violation messages directly.
func registryCoverageOffenses(rpcs rpcNames, allowlist []string, registry map[string]rpcAuthzEntry) []string {
	allow := map[string]bool{}
	for _, name := range allowlist {
		allow[name] = true
	}
	var msgs []string
	for _, rpc := range rpcs {
		if allow[rpc] {
			continue
		}
		if _, ok := registry[rpc]; !ok {
			msgs = append(msgs, fmt.Sprintf("RPC %s has no entry in leaflab/api/authz_registry.go's "+
				"rpcAuthzRegistrations -- add one naming its Kind (read/write) and, for a write RPC, the "+
				"authz.EntityKind values its write path may set a live reference to (ForeignRefFields), or add %s "+
				"to auth.go's anonymousMethods if it is truly meant to be exempt from household-scope enforcement", rpc, rpc))
		}
	}
	return msgs
}

// testFuncNameRe matches a top-level `func TestXxx(` declaration.
var testFuncNameRe = regexp.MustCompile(`(?m)^func\s+(Test\w+)\s*\(`)

// enumerateTestFuncNames returns the set of every top-level Test function
// name declared across apiFiles' *_test.go entries.
func enumerateTestFuncNames(apiFiles map[string]string) map[string]bool {
	names := map[string]bool{}
	for path, content := range apiFiles {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, m := range testFuncNameRe.FindAllStringSubmatch(content, -1) {
			names[m[1]] = true
		}
	}
	return names
}

// scopeGapReasonRe requires a ScopeGapReason to reference a tracked issue
// ("#<number>") -- an exemption from strict enforcement must point at what
// closes the gap, not just assert one exists.
var scopeGapReasonRe = regexp.MustCompile(`#\d+`)

// TestNFR1b_EveryRPCHasNonMemberAndForeignReferenceTests is NFR1.b's
// coverage assertion proper: for every RPC registered in
// rpcAuthzRegistrations, its entry must name a NonMemberTest -- a
// leaflab/api test asserting a non-member (a caller outside the target
// entity's household, or with no current household membership at all) is
// refused -- unless it instead sets ScopeGapReason (an explicit, reviewed
// exception naming the tracked issue that closes the gap; see
// authz_registry.go's doc comment). Any RPC declaring ForeignRefFields
// must additionally name a ForeignRefTest asserting a foreign-household
// reference is rejected (FR1.2). Both named tests must actually exist as
// Test functions in leaflab/api. A registered RPC missing either
// requirement fails the build, naming the RPC and which is missing.
func TestNFR1b_EveryRPCHasNonMemberAndForeignReferenceTests(t *testing.T) {
	registrySrc := mustReadFile(t, "api/authz_registry.go")
	registry := parseRPCAuthzRegistrations(t, registrySrc)
	if len(registry) == 0 {
		t.Fatal("found no entries in leaflab/api/authz_registry.go's rpcAuthzRegistrations -- check the " +
			"api:conformance_srcs data dependency in BUILD.bazel or authz_registry.go's shape")
	}

	apiFiles := globGoFiles(t, "api")
	testFuncs := enumerateTestFuncNames(apiFiles)
	if len(testFuncs) == 0 {
		t.Fatal("found no Test funcs across leaflab/api's *_test.go sources -- check the api:conformance_srcs data dependency in BUILD.bazel")
	}

	for _, msg := range testCoverageOffenses(registry, testFuncs) {
		t.Error(msg)
	}
}

// testCoverageOffenses computes TestNFR1b_EveryRPCHasNonMemberAndForeignReferenceTests's
// failures, factored out like the other checks in this package.
func testCoverageOffenses(registry map[string]rpcAuthzEntry, testFuncs map[string]bool) []string {
	rpcs := make([]string, 0, len(registry))
	for rpc := range registry {
		rpcs = append(rpcs, rpc)
	}
	sort.Strings(rpcs) // deterministic failure order

	var msgs []string
	for _, rpc := range rpcs {
		entry := registry[rpc]

		switch {
		case entry.ScopeGapReason != "":
			if !scopeGapReasonRe.MatchString(entry.ScopeGapReason) {
				msgs = append(msgs, fmt.Sprintf("RPC %s's rpcAuthzRegistrations.ScopeGapReason %q does not reference "+
					"a tracked issue (\"#<number>\") -- a household-scope exemption must point at the issue that "+
					"closes it", rpc, entry.ScopeGapReason))
			}
		case entry.NonMemberTest == "":
			msgs = append(msgs, fmt.Sprintf("RPC %s's rpcAuthzRegistrations entry names no NonMemberTest -- add a "+
				"leaflab/api test asserting a non-member (a caller outside the target household, or with no current "+
				"household membership) is refused, and name it there, or set ScopeGapReason (referencing a tracked "+
				"issue) if this is a known, deliberate gap", rpc))
		case !testFuncs[entry.NonMemberTest]:
			msgs = append(msgs, fmt.Sprintf("RPC %s's rpcAuthzRegistrations entry names NonMemberTest %q, but no "+
				"such Test function exists in leaflab/api -- add it, or correct the name", rpc, entry.NonMemberTest))
		}

		if len(entry.ForeignRefFields) == 0 {
			continue
		}
		switch {
		case entry.ForeignRefTest == "":
			msgs = append(msgs, fmt.Sprintf("RPC %s declares ForeignRefFields %v but names no ForeignRefTest -- add "+
				"a leaflab/api test asserting a foreign-household reference in one of those fields is rejected "+
				"(FR1.2), and name it there", rpc, entry.ForeignRefFields))
		case !testFuncs[entry.ForeignRefTest]:
			msgs = append(msgs, fmt.Sprintf("RPC %s's rpcAuthzRegistrations entry names ForeignRefTest %q, but no "+
				"such Test function exists in leaflab/api -- add it, or correct the name", rpc, entry.ForeignRefTest))
		}
	}
	return msgs
}

// callSite is one call expression found in an RPC handler's body, in
// source order: either an authz-qualifying call, or a deviceRepository
// call named by repoMethod.
type callSite struct {
	pos        token.Pos
	isAuthz    bool
	repoMethod string
}

// isAuthzCall reports whether call reaches into leaflab/api/authz/: a
// direct authz.<Func>(...) call (e.g. authz.AssertSameHousehold), a
// s.authzSvc.<Method>(...) call (e.g. Resolve, ResolveInScope,
// ScopeForPrincipal, ResolveBoardByDeviceID), or an s.<method>(...) call
// whose name itself names a scope/authorization concern (scopeForCaller,
// authorizeBoardAccess, and any future handler helper following the same
// naming convention) -- matched by case-insensitive substring on "scope"/
// "authorize"/"authz" rather than an exact, closed name list, so a new
// helper following the convention is picked up without editing this
// check.
func isAuthzCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch x := sel.X.(type) {
	case *ast.Ident:
		if x.Name == "authz" {
			return true
		}
		if x.Name == "s" {
			name := strings.ToLower(sel.Sel.Name)
			return strings.Contains(name, "scope") || strings.Contains(name, "authorize") || strings.Contains(name, "authz")
		}
	case *ast.SelectorExpr:
		// s.authzSvc.<Method>(...)
		if x.Sel != nil && x.Sel.Name == "authzSvc" {
			return true
		}
	}
	return false
}

// repoCallMethod reports the deviceRepository method name if call is an
// s.repo.<Method>(...) call, and whether it matched.
func repoCallMethod(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	xIdent, ok := inner.X.(*ast.Ident)
	if !ok || xIdent.Name != "s" || inner.Sel.Name != "repo" {
		return "", false
	}
	return sel.Sel.Name, true
}

// collectHandlerCallSites walks body and returns every authz-qualifying or
// deviceRepository call site found anywhere in it (including inside
// nested if/for blocks), sorted by source position -- statement nesting
// does not exempt a call from this check; only textual order matters.
func collectHandlerCallSites(body *ast.BlockStmt) []callSite {
	var sites []callSite
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isAuthzCall(call) {
			sites = append(sites, callSite{pos: call.Pos(), isAuthz: true})
			return true
		}
		if method, ok := repoCallMethod(call); ok {
			sites = append(sites, callSite{pos: call.Pos(), repoMethod: method})
		}
		return true
	})
	sort.Slice(sites, func(i, j int) bool { return sites[i].pos < sites[j].pos })
	return sites
}

// handlerFailClosedOffenses computes one RPC handler's fail-closed
// offenses from its call sites: the first non-exempt deviceRepository call
// reached with no prior authz-qualifying call, if any. Reports at most one
// message -- once a handler has touched the repository unguarded, later
// calls in the same handler add no new information.
func handlerFailClosedOffenses(rpc string, sites []callSite, exemptRepoCalls map[string]bool) []string {
	authzSeen := false
	for _, s := range sites {
		if s.isAuthz {
			authzSeen = true
			continue
		}
		if authzSeen || exemptRepoCalls[s.repoMethod] {
			continue
		}
		return []string{fmt.Sprintf(
			"RPC %s's handler calls repository method %s before any leaflab/api/authz/ call (Scope resolution, "+
				"ResolveInScope, or AssertSameHousehold) -- resolve the caller's Scope (or the target entity) first, "+
				"or add %q to this RPC's rpcAuthzRegistrations.PreAuthzExemptRepoCalls if this is a documented, "+
				"reviewed exception (set ScopeGapReason too)", rpc, s.repoMethod, s.repoMethod),
		}
	}
	return nil
}

// parseServerHandlerBodies parses src (server.go's contents) and returns
// every *LeafLabAPIServer method's body, keyed by method name.
func parseServerHandlerBodies(t *testing.T, src string) map[string]*ast.BlockStmt {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", src, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	out := map[string]*ast.BlockStmt{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 || fd.Body == nil {
			continue
		}
		if !isLeafLabAPIServerReceiver(fd.Recv.List[0].Type) {
			continue
		}
		out[fd.Name.Name] = fd.Body
	}
	return out
}

func isLeafLabAPIServerReceiver(expr ast.Expr) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "LeafLabAPIServer"
}

// TestNFR1b_EveryHandlerObtainsScopeBeforeTouchingRepository is NFR1.b's
// fail-closed assertion: every LeafLabAPIServer RPC handler in server.go,
// other than the anonymous allowlist (GetHealth), must obtain an
// authz.Scope (or resolve/validate an entity through leaflab/api/authz/)
// before it calls anything on its deviceRepository, in source order -- a
// handler that queries the pool without first doing so fails, unless that
// specific repository call is named in this RPC's rpcAuthzRegistrations
// entry's PreAuthzExemptRepoCalls (an explicit, reviewed exception; see
// authz_registry.go's doc comment for today's sole entry,
// PushDeviceConfig's GetOrCreateBoard self-registration upsert).
func TestNFR1b_EveryHandlerObtainsScopeBeforeTouchingRepository(t *testing.T) {
	proto := mustReadFile(t, "api/proto/api.proto")
	rpcs := enumerateRPCsFromProto(t, proto)
	if len(rpcs) == 0 {
		t.Fatal("found no RPCs in leaflab.api.v1.LeafLabAPI -- check the api/proto:proto_file data dependency in BUILD.bazel")
	}

	authGoSrc := mustReadFile(t, "api/auth.go")
	allowlist := enumerateAnonymousAllowlist(t, authGoSrc)
	allow := map[string]bool{}
	for _, name := range allowlist {
		allow[name] = true
	}

	registrySrc := mustReadFile(t, "api/authz_registry.go")
	registry := parseRPCAuthzRegistrations(t, registrySrc)

	serverGoSrc := mustReadFile(t, "api/server.go")
	handlers := parseServerHandlerBodies(t, serverGoSrc)
	if len(handlers) == 0 {
		t.Fatal("found no *LeafLabAPIServer method declarations in leaflab/api/server.go -- check the api:conformance_srcs data dependency in BUILD.bazel")
	}

	for _, rpc := range rpcs {
		if allow[rpc] {
			continue
		}
		body, ok := handlers[rpc]
		if !ok {
			// No handler found for a registered RPC: TestAuthCoverage /
			// TestNFR1b_EveryRPCHasRegistryCoverage already surface a
			// missing/misnamed handler; this check has nothing to walk.
			continue
		}
		exempt := map[string]bool{}
		for _, m := range registry[rpc].PreAuthzExemptRepoCalls {
			exempt[m] = true
		}
		sites := collectHandlerCallSites(body)
		for _, msg := range handlerFailClosedOffenses(rpc, sites, exempt) {
			t.Error(msg)
		}
	}
}

// parseAPIFuncDecls parses every non-test .go source in apiFiles and
// returns each top-level func/method's *ast.FuncDecl, keyed by name.
// Excludes *_test.go: TestNFR1b_EveryForeignFKWriteAssertsSameHousehold
// analyzes leaflab/api's production write paths only, per the issue's
// "every write path in leaflab/api" wording -- a test helper calling
// AssertSameHousehold directly must not satisfy this check.
func parseAPIFuncDecls(t *testing.T, apiFiles map[string]string) map[string]*ast.FuncDecl {
	t.Helper()
	out := map[string]*ast.FuncDecl{}
	for path, content := range apiFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, content, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			out[fd.Name.Name] = fd
		}
	}
	return out
}

// rpcWritePathAnalysis is one RPC's transitively-reachable-call-chain
// analysis, computed by analyzeWritePath: whether authz.AssertSameHousehold
// is reached anywhere in it, and every authz.EntityKind identifier (e.g.
// "EntityRegion") found in a `Kind: authz.EntityXxx` composite-literal
// field anywhere in that same reachable set.
type rpcWritePathAnalysis struct {
	AssertSameHouseholdReached bool
	EntityKinds                map[string]bool
}

// analyzeWritePath does a breadth-first walk of funcs' call graph starting
// at rpc's own func/method body, following only same-receiver method calls
// (s.<name>(...), matching this package's single-receiver-type shape --
// see isAuthzCall's doc comment) and bare same-package function calls.
// This lets a helper like validatePushRegions -- called by PushDeviceConfig
// but not itself an RPC handler -- count toward PushDeviceConfig's
// analysis without hardcoding that specific call chain.
//
// EntityKinds is deliberately scoped to only the function(s) that
// themselves directly contain an authz.AssertSameHousehold call, not every
// function reachable from the RPC handler: a handler may resolve an
// entity's own household for an unrelated reason (e.g. PushDeviceConfig
// resolving its own board's household via s.authzSvc.Resolve, using an
// authz.EntityRef{Kind: authz.EntityBoard, ...} literal) without that
// EntityKind being one this write path asserts a live *reference* against.
// Collecting only from the AssertSameHousehold-containing function itself
// keeps this check's entity-kind claim tied to the specific call site it
// describes.
func analyzeWritePath(rpc string, funcs map[string]*ast.FuncDecl) rpcWritePathAnalysis {
	analysis := rpcWritePathAnalysis{EntityKinds: map[string]bool{}}
	visited := map[string]bool{}
	queue := []string{rpc}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		fd, ok := funcs[name]
		if !ok {
			continue
		}

		containsAssert := false
		var kindsHere []string
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				switch fn := node.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := fn.X.(*ast.Ident); ok {
						if x.Name == "authz" && fn.Sel.Name == "AssertSameHousehold" {
							containsAssert = true
						}
						if x.Name == "s" {
							queue = append(queue, fn.Sel.Name)
						}
					}
				case *ast.Ident:
					queue = append(queue, fn.Name)
				}
			case *ast.KeyValueExpr:
				keyIdent, ok := node.Key.(*ast.Ident)
				if !ok || keyIdent.Name != "Kind" {
					return true
				}
				sel, ok := node.Value.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "authz" && strings.HasPrefix(sel.Sel.Name, "Entity") {
					kindsHere = append(kindsHere, sel.Sel.Name)
				}
			}
			return true
		})
		if containsAssert {
			analysis.AssertSameHouseholdReached = true
			for _, k := range kindsHere {
				analysis.EntityKinds[k] = true
			}
		}
	}
	return analysis
}

// TestNFR1b_EveryForeignFKWriteAssertsSameHousehold is NFR1.b's foreign-FK
// assertion: every write path in leaflab/api that sets a region_id,
// plant_id, board_id or household_id -- as declared by that RPC's
// ForeignRefFields in rpcAuthzRegistrations -- must route through
// authz.AssertSameHousehold (FR1.2) before the write. A registry entry
// naming an entity kind with no corresponding AssertSameHousehold call
// site fails the build, as does an AssertSameHousehold call site (found by
// its reachable `Kind: authz.EntityXxx` literals) for an entity kind the
// registry doesn't declare -- the two must agree in both directions.
func TestNFR1b_EveryForeignFKWriteAssertsSameHousehold(t *testing.T) {
	registrySrc := mustReadFile(t, "api/authz_registry.go")
	registry := parseRPCAuthzRegistrations(t, registrySrc)
	if len(registry) == 0 {
		t.Fatal("found no entries in leaflab/api/authz_registry.go's rpcAuthzRegistrations -- check the " +
			"api:conformance_srcs data dependency in BUILD.bazel or authz_registry.go's shape")
	}

	apiFiles := globGoFiles(t, "api")
	funcs := parseAPIFuncDecls(t, apiFiles)
	if len(funcs) == 0 {
		t.Fatal("found no function declarations in leaflab/api's non-test sources -- check the api:conformance_srcs data dependency in BUILD.bazel")
	}

	rpcs := make([]string, 0, len(registry))
	for rpc := range registry {
		rpcs = append(rpcs, rpc)
	}
	sort.Strings(rpcs) // deterministic failure order

	for _, rpc := range rpcs {
		if _, ok := funcs[rpc]; !ok {
			// No handler found: TestNFR1b_EveryHandlerObtainsScopeBeforeTouchingRepository
			// already surfaces a missing/misnamed handler.
			continue
		}
		analysis := analyzeWritePath(rpc, funcs)
		for _, msg := range foreignFKOffenses(rpc, registry[rpc].ForeignRefFields, analysis) {
			t.Error(msg)
		}
	}
}

// foreignFKOffenses computes TestNFR1b_EveryForeignFKWriteAssertsSameHousehold's
// failures for one RPC, factored out like the other checks in this
// package.
func foreignFKOffenses(rpc string, declared []string, analysis rpcWritePathAnalysis) []string {
	declaredSet := map[string]bool{}
	for _, k := range declared {
		declaredSet[k] = true
	}

	if len(declared) > 0 && !analysis.AssertSameHouseholdReached {
		return []string{fmt.Sprintf("RPC %s's rpcAuthzRegistrations entry declares ForeignRefFields %v, but no "+
			"authz.AssertSameHousehold call site is reachable from its handler -- route every one of those writes "+
			"through authz.AssertSameHousehold (FR1.2) before writing, or correct ForeignRefFields", rpc, declared)}
	}
	if len(declared) == 0 && analysis.AssertSameHouseholdReached && len(analysis.EntityKinds) > 0 {
		return []string{fmt.Sprintf("RPC %s's handler reaches authz.AssertSameHousehold for entity kind(s) %v, but "+
			"its rpcAuthzRegistrations entry declares no ForeignRefFields -- add them so this write path stays "+
			"registered", rpc, setKeys(analysis.EntityKinds))}
	}

	var msgs []string
	for _, kind := range declared {
		if !analysis.EntityKinds[kind] {
			msgs = append(msgs, fmt.Sprintf("RPC %s's rpcAuthzRegistrations declares ForeignRefFields entry "+
				"authz.%s, but no authz.AssertSameHousehold call site reachable from its handler references that "+
				"entity kind -- add the call site, or remove the declaration", rpc, kind))
		}
	}
	for kind := range analysis.EntityKinds {
		if !declaredSet[kind] {
			msgs = append(msgs, fmt.Sprintf("RPC %s's handler asserts household ownership for entity kind "+
				"authz.%s via authz.AssertSameHousehold, but that kind is not in its "+
				"rpcAuthzRegistrations.ForeignRefFields -- add it so this write path stays registered", rpc, kind))
		}
	}
	return msgs
}

func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
