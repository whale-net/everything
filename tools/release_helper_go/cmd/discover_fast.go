package cmd

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/bazelbuild/buildtools/build"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// --- fast mode: static Starlark-AST discovery -----------------------------
//
// ListAllApps/ListAllHelmCharts normally discover release_app/release_helm_chart
// targets via `bazel query` (loading phase) + `bazel cquery` (analysis phase),
// which pays for a full Bazel server round trip even though every field the
// two rule implementations (release.bzl's _app_metadata_impl and
// _helm_chart_metadata_impl) emit is already a literal keyword argument the
// BUILD.bazel file passes to the release_app/release_helm_chart macro --
// nothing in real call sites is computed (no variables, string
// concatenation, glob(), or select()). --fast mode exploits that: it parses
// every BUILD.bazel file's AST directly with buildtools (the same library
// buildifier/buildozer/gazelle use to manipulate BUILD files without
// evaluating them) and replicates the macros' derivation logic in Go,
// never invoking bazel at all.
//
// This intentionally only recognizes the release_app(...)/release_helm_chart(...)
// macro calls -- not raw app_metadata(...)/helm_chart_metadata(...) rule
// calls -- which is also how it gets testonly fixture exclusion for free:
// //tools/appmeta/testdata/BUILD.bazel's fixture-app_metadata calls
// app_metadata(...) directly specifically so it can set testonly=True
// (release_app has no testonly passthrough), and a direct rule call never
// matches the fast scan.
//
// See docs/RELEASE_HELPER_FAST_MODE.md for the correctness assumption this
// depends on and how it's kept honest.

// fastDiscoveryDirName marks a directory that never contains real source
// BUILD.bazel files and should not be descended into during a --fast scan:
// Bazel's own convenience symlinks/output roots, VCS metadata, and
// dependency caches. Matched by exact name; "bazel-*" is matched by prefix
// separately since it also covers "bazel-<reponame>".
var fastDiscoverySkipDirNames = map[string]bool{
	".git":         true,
	".claude":      true,
	"node_modules": true,
}

// findBuildFiles walks workspaceRoot for every BUILD.bazel file.
func findBuildFiles(workspaceRoot string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(workspaceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != workspaceRoot && (fastDiscoverySkipDirNames[name] || strings.HasPrefix(name, "bazel-")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "BUILD.bazel" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s for BUILD.bazel files: %w", workspaceRoot, err)
	}
	sort.Strings(files)
	return files, nil
}

// packageForBuildFile returns the Bazel package label ("//a/b", or "//" for
// the workspace root) that absPath's containing directory corresponds to.
func packageForBuildFile(workspaceRoot, absPath string) (string, error) {
	rel, err := filepath.Rel(workspaceRoot, filepath.Dir(absPath))
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "//", nil
	}
	return "//" + rel, nil
}

// resolveLabel resolves a label string as written in a BUILD.bazel file
// (":foo", bare "foo", or an already-absolute "//pkg:foo") against pkg
// ("//a/b" or "//"), returning the canonical absolute "//pkg:name" form --
// matching what `str(label)` and canonicalLabel (metadata.go) produce from
// a real cquery for the same target.
func resolveLabel(pkg, label string) string {
	label = canonicalLabel(label)
	if strings.HasPrefix(label, "//") {
		return label
	}
	name := strings.TrimPrefix(label, ":")
	if pkg == "//" {
		return "//:" + name
	}
	return pkg + ":" + name
}

func attrStringOr(r *build.Rule, key, def string) string {
	if v := r.AttrString(key); v != "" {
		return v
	}
	return def
}

func attrBool(r *build.Rule, key string) bool {
	return r.AttrLiteral(key) == "True"
}

func attrInt32(r *build.Rule, key string) int32 {
	lit := r.AttrLiteral(key)
	if lit == "" {
		return 0
	}
	n, err := strconv.Atoi(lit)
	if err != nil {
		return 0
	}
	return int32(n)
}

// topLevelAssign returns the RHS of a top-level "NAME = ..." statement in f,
// or nil if there is none.
func topLevelAssign(f *build.File, name string) build.Expr {
	for _, stmt := range f.Stmt {
		if a, ok := stmt.(*build.AssignExpr); ok {
			if lhs, ok := a.LHS.(*build.Ident); ok && lhs.Name == name {
				return a.RHS
			}
		}
	}
	return nil
}

// resolveStringListAttr reads a rule's list-of-string attribute, resolving
// one level of indirection through a bare identifier referencing a
// top-level "NAME = [...]" assignment in the same file -- e.g.
// tools/app_registry/BUILD.bazel's `apps = APP_REGISTRY_APPS`. Rule.AttrStrings
// only handles a literal list expression written inline, so without this,
// every release_helm_chart call that factors its apps list into a shared
// constant (APP_REGISTRY_APPS, MANMAN_V2_APPS, MANMAN_V1_APPS, FCM_APPS --
// see docs/RELEASE_HELPER_FAST_MODE.md) would silently resolve to an empty
// apps/app_refs list instead of erroring or matching bazel's real output.
func resolveStringListAttr(f *build.File, r *build.Rule, key string) []string {
	expr := r.Attr(key)
	if id, ok := expr.(*build.Ident); ok {
		expr = topLevelAssign(f, id.Name)
	}
	return build.Strings(expr)
}

var deployUnitEnum = map[string]appmetapb.DeployUnit{
	"chart": appmetapb.DeployUnit_DEPLOY_UNIT_CHART,
	"image": appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
	"none":  appmetapb.DeployUnit_DEPLOY_UNIT_NONE,
}

// appManifestFromRule replicates release_app()'s derivation (release.bzl)
// and _app_metadata_impl's metadata construction from the literal keyword
// arguments a single `release_app(...)` call passes.
func appManifestFromRule(pkg string, r *build.Rule) (AppMetadata, error) {
	name := r.Name()
	if name == "" {
		return AppMetadata{}, fmt.Errorf("%s: release_app call has no name", pkg)
	}

	effectiveName := attrStringOr(r, "app_name", name)
	appType := r.AttrString("app_type")
	isContainerApp := appType != "cli" && appType != "binary" && appType != "firmware"

	domain := r.AttrString("domain")
	if domain == "" {
		return AppMetadata{}, fmt.Errorf("%s: release_app(name=%q) has no domain", pkg, name)
	}

	deployUnitStr := r.AttrString("deploy_unit")
	if deployUnitStr == "" {
		if isContainerApp {
			deployUnitStr = "chart"
		} else {
			deployUnitStr = "none"
		}
	}
	deployUnit, ok := deployUnitEnum[deployUnitStr]
	if !ok {
		return AppMetadata{}, fmt.Errorf("%s: release_app(name=%q) has invalid deploy_unit %q", pkg, name, deployUnitStr)
	}

	binaryName := attrStringOr(r, "binary_name", effectiveName)
	binaryTarget := resolveLabel(pkg, binaryName)

	var imageTarget string
	if isContainerApp {
		imageTarget = resolveLabel(pkg, name+"_image")
	}

	var openapiSpecTarget string
	if isContainerApp && r.AttrString("fastapi_app") != "" && r.AttrString("language") == "python" {
		openapiSpecTarget = resolveLabel(pkg, name+"_openapi_spec")
	}

	manifest := &appmetapb.AppManifest{
		Name:              effectiveName,
		Domain:            domain,
		Description:       r.AttrString("description"),
		Language:          r.AttrString("language"),
		Registry:          attrStringOr(r, "registry", "ghcr.io"),
		Organization:      attrStringOr(r, "organization", "whale-net"),
		RepoName:          domain + "-" + effectiveName,
		Version:           attrStringOr(r, "version", "latest"),
		BinaryTarget:      binaryTarget,
		ImageTarget:       imageTarget,
		OpenapiSpecTarget: openapiSpecTarget,
		AppType:           appType,
		Port:              attrInt32(r, "port"),
		Replicas:          attrInt32(r, "replicas"),
		Command:           r.AttrStrings("command"),
		Args:              r.AttrStrings("args"),
		DeployUnit:        deployUnit,
	}

	if attrBool(r, "health_check_enabled") {
		manifest.HealthCheck = &appmetapb.HealthCheck{
			Enabled: true,
			Path:    attrStringOr(r, "health_check_path", "/health"),
		}
	}

	if ingressHost := r.AttrString("ingress_host"); ingressHost != "" {
		manifest.Ingress = &appmetapb.Ingress{
			Host:          ingressHost,
			TlsSecretName: r.AttrString("ingress_tls_secret"),
		}
	}

	reqCPU := r.AttrString("resources_requests_cpu")
	reqMem := r.AttrString("resources_requests_memory")
	limCPU := r.AttrString("resources_limits_cpu")
	limMem := r.AttrString("resources_limits_memory")
	if reqCPU != "" || reqMem != "" || limCPU != "" || limMem != "" {
		manifest.Resources = &appmetapb.Resources{
			RequestsCpu:    reqCPU,
			RequestsMemory: reqMem,
			LimitsCpu:      limCPU,
			LimitsMemory:   limMem,
		}
	}

	return AppMetadata{AppManifest: manifest, BazelTarget: resolveLabel(pkg, name+"_metadata")}, nil
}

// helmChartManifestFromRule replicates release_helm_chart()'s derivation
// and _helm_chart_metadata_impl's metadata construction. appsByLabel maps
// every discovered app_metadata target's canonical label to its manifest,
// so this can resolve the chart's `apps` label list to domain-qualified
// app_refs the same way the real rule reads AppMetadataInfo off each dep.
func helmChartManifestFromRule(f *build.File, pkg string, r *build.Rule, appsByLabel map[string]AppMetadata) (HelmChartMetadata, error) {
	name := r.Name()
	if name == "" {
		return HelmChartMetadata{}, fmt.Errorf("%s: release_helm_chart call has no name", pkg)
	}

	domain := r.AttrString("domain")
	if domain == "" {
		return HelmChartMetadata{}, fmt.Errorf("%s: release_helm_chart(name=%q) has no domain", pkg, name)
	}
	namespace := r.AttrString("namespace")
	if namespace == "" {
		return HelmChartMetadata{}, fmt.Errorf("%s: release_helm_chart(name=%q) has no namespace", pkg, name)
	}

	baseChartName := attrStringOr(r, "chart_name", name)
	actualChartName := fmt.Sprintf("helm-%s-%s", domain, baseChartName)

	appLabels := resolveStringListAttr(f, r, "apps")
	appNames := make([]string, 0, len(appLabels))
	appRefs := make([]string, 0, len(appLabels))
	for _, raw := range appLabels {
		resolved := resolveLabel(pkg, raw)
		app, ok := appsByLabel[resolved]
		if !ok {
			return HelmChartMetadata{}, fmt.Errorf("%s: release_helm_chart(name=%q) references unknown app_metadata target %q", pkg, name, resolved)
		}
		appNames = append(appNames, app.Name)
		appRefs = append(appRefs, app.Domain+"/"+app.Name)
	}

	manifest := &appmetapb.ChartManifest{
		Name:        actualChartName,
		Domain:      domain,
		Version:     attrStringOr(r, "chart_version", "0.0.0-dev"),
		Namespace:   namespace,
		Environment: attrStringOr(r, "environment", "production"),
		Apps:        appNames,
		// chart_target is attr.string on helm_chart_metadata (not
		// attr.label), so bazel never canonicalizes it -- it stays exactly
		// as release_helm_chart passes it: ":" + name, not a resolved
		// "//pkg:name" label. Mirror that literally rather than resolving.
		ChartTarget: ":" + name,
		AppRefs:     appRefs,
	}

	return HelmChartMetadata{ChartManifest: manifest, BazelTarget: resolveLabel(pkg, name+"_chart_metadata")}, nil
}

// discoverFast walks workspaceRoot once and parses every release_app and
// release_helm_chart call site, returning both manifests sets. Both
// ListAllAppsFast and ListAllHelmChartsFast fast wrappers share this so
// `manifest-set` (which calls both) only pays for one filesystem walk.
func discoverFast(workspaceRoot string) ([]AppMetadata, []HelmChartMetadata, error) {
	files, err := findBuildFiles(workspaceRoot)
	if err != nil {
		return nil, nil, err
	}

	var apps []AppMetadata
	appsByLabel := make(map[string]AppMetadata)
	type pendingChart struct {
		file *build.File
		pkg  string
		r    *build.Rule
	}
	var pendingCharts []pendingChart

	for _, path := range files {
		data, err := realFS{}.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", path, err)
		}
		f, err := build.ParseBuild(path, data)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", path, err)
		}
		pkg, err := packageForBuildFile(workspaceRoot, path)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve package for %s: %w", path, err)
		}

		for _, r := range f.Rules("release_app") {
			app, err := appManifestFromRule(pkg, r)
			if err != nil {
				return nil, nil, err
			}
			apps = append(apps, app)
			appsByLabel[app.BazelTarget] = app
		}
		for _, r := range f.Rules("release_helm_chart") {
			pendingCharts = append(pendingCharts, pendingChart{file: f, pkg: pkg, r: r})
		}
	}

	var charts []HelmChartMetadata
	for _, pc := range pendingCharts {
		chart, err := helmChartManifestFromRule(pc.file, pc.pkg, pc.r, appsByLabel)
		if err != nil {
			return nil, nil, err
		}
		charts = append(charts, chart)
	}

	sort.Slice(apps, func(i, j int) bool { return apps[i].FullName() < apps[j].FullName() })
	sort.Slice(charts, func(i, j int) bool { return charts[i].Name < charts[j].Name })
	return apps, charts, nil
}

// ListAllAppsFast is the --fast counterpart to ListAllApps: same output
// contract (sorted by FullName), no bazel invocation.
func ListAllAppsFast(workspaceRoot string) ([]AppMetadata, error) {
	apps, _, err := discoverFast(workspaceRoot)
	return apps, err
}

// ListAllHelmChartsFast is the --fast counterpart to ListAllHelmCharts.
func ListAllHelmChartsFast(workspaceRoot string) ([]HelmChartMetadata, error) {
	_, charts, err := discoverFast(workspaceRoot)
	return charts, err
}
