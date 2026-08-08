package cmd

import (
	"fmt"
	"testing"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

func TestListAllApps(t *testing.T) {
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
		{pkg: "demo/hello_python", targetSuffix: "hello-python_metadata", name: "hello-python", domain: "demo"},
		{pkg: "manmanv2/api", targetSuffix: "control-api_metadata", name: "control-api", domain: "manmanv2"},
	}
	fs, bazel := buildFakeInfra(apps)

	result, err := ListAllApps(bazel, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 apps, got %d: %v", len(result), result)
	}
	// Results are sorted by name
	if result[0].Name != "control-api" {
		t.Errorf("expected first app to be 'control-api' (sorted), got %q", result[0].Name)
	}
}

func TestListAllAppsQueryError(t *testing.T) {
	bazel := newFakeBazel() // returns error for everything
	fs := newFakeFS()

	_, err := ListAllApps(bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error when bazel cquery fails")
	}
}

func TestListAllAppsCqueryError(t *testing.T) {
	bazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, argsNotContain: []string{"cquery"}, output: "//demo/hello_go:hello-go_metadata"},
		fakeBazelCall{argsContain: []string{"cquery"}, err: fmt.Errorf("analysis error in an unrelated target")},
	)
	fs := newFakeFS()

	_, err := ListAllApps(bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error when cquery fails, even partially")
	}
}

func TestListAllAppsMalformedLine(t *testing.T) {
	bazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, argsNotContain: []string{"cquery"}, output: "//demo/hello_go:hello-go_metadata"},
		fakeBazelCall{argsContain: []string{"cquery"}, output: "no-tab-in-this-line"},
	)
	fs := newFakeFS()

	_, err := ListAllApps(bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error for malformed cquery line")
	}
}

func TestListAllAppsInvalidJSON(t *testing.T) {
	bazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, argsNotContain: []string{"cquery"}, output: "//demo/hello_go:hello-go_metadata"},
		fakeBazelCall{argsContain: []string{"cquery"}, output: "@@//demo/hello_go:hello-go_metadata\tnot json"},
	)
	fs := newFakeFS()

	_, err := ListAllApps(bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error for invalid metadata JSON")
	}
}

func TestListAllAppsEmptyResult(t *testing.T) {
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, argsNotContain: []string{"cquery"}, output: ""})
	fs := newFakeFS()

	result, err := ListAllApps(bazel, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 apps, got %d", len(result))
	}
}

func TestListAllAppsCanonicalizesLabels(t *testing.T) {
	// cquery emits labels in @@//pkg:name canonical form; ListAllApps must
	// strip the @@ prefix so downstream rdeps queries get plain //pkg:name.
	bazel := newFakeBazel(
		fakeBazelCall{argsContain: []string{"query", "kind(app_metadata"}, argsNotContain: []string{"cquery"}, output: "//demo/hello_go:hello-go_metadata"},
		fakeBazelCall{argsContain: []string{"cquery"}, output: "@@//demo/hello_go:hello-go_metadata\t" +
			`{"name":"hello-go","domain":"demo","binary_target":"@@//demo/hello_go:hello-go","image_target":"@@//demo/hello_go:hello-go_image"}`},
	)

	result, err := ListAllApps(bazel, newFakeFS(), fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 app, got %d", len(result))
	}
	if got := result[0].BazelTarget; got != "//demo/hello_go:hello-go_metadata" {
		t.Errorf("BazelTarget = %q, want stripped form", got)
	}
	if got := result[0].BinaryTarget; got != "//demo/hello_go:hello-go" {
		t.Errorf("BinaryTarget = %q, want stripped form", got)
	}
	if got := result[0].ImageTarget; got != "//demo/hello_go:hello-go_image" {
		t.Errorf("ImageTarget = %q, want stripped form", got)
	}
}

func TestAppMetadataFullName(t *testing.T) {
	m := AppMetadata{AppManifest: &appmetapb.AppManifest{Name: "hello-go", Domain: "demo"}}
	if got := m.FullName(); got != "demo-hello-go" {
		t.Errorf("FullName() = %q, want %q", got, "demo-hello-go")
	}
}
