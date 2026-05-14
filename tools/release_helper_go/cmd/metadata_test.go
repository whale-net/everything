package cmd

import (
	"path/filepath"
	"testing"
)

func TestMetadataFilePath(t *testing.T) {
	tests := []struct {
		target  string
		want    string
		wantErr bool
	}{
		{
			target: "//demo/hello_go:hello-go_metadata",
			want:   filepath.Join("/ws", "bazel-bin", "demo/hello_go", "hello-go_metadata_metadata.json"),
		},
		{
			target: "//manmanv2/api:control-api_metadata",
			want:   filepath.Join("/ws", "bazel-bin", "manmanv2/api", "control-api_metadata_metadata.json"),
		},
		{
			target:  "invalid-target",
			wantErr: true,
		},
		{
			target:  "//nodash",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		got, err := metadataFilePath("/ws", tt.target)
		if tt.wantErr {
			if err == nil {
				t.Errorf("metadataFilePath(%q): expected error, got %q", tt.target, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("metadataFilePath(%q): unexpected error: %v", tt.target, err)
			continue
		}
		if got != tt.want {
			t.Errorf("metadataFilePath(%q) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestGetAppMetadata(t *testing.T) {
	target := "//demo/hello_go:hello-go_metadata"
	path := metaPath("demo/hello_go", "hello-go_metadata")
	jsonData := sampleMetaJSON("hello-go", "demo")

	fs := newFakeFS().add(path, jsonData)
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"build", target}})

	meta, err := GetAppMetadata(target, bazel, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.Name != "hello-go" {
		t.Errorf("Name = %q, want %q", meta.Name, "hello-go")
	}
	if meta.Domain != "demo" {
		t.Errorf("Domain = %q, want %q", meta.Domain, "demo")
	}
	if meta.BazelTarget != target {
		t.Errorf("BazelTarget = %q, want %q", meta.BazelTarget, target)
	}
	if meta.Registry != "ghcr.io" {
		t.Errorf("Registry = %q, want %q", meta.Registry, "ghcr.io")
	}
}

func TestGetAppMetadataBuildFails(t *testing.T) {
	target := "//demo/hello_go:hello-go_metadata"
	bazel := newFakeBazel() // no matching calls → error
	fs := newFakeFS()

	_, err := GetAppMetadata(target, bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error when bazel build fails")
	}
}

func TestGetAppMetadataFileMissing(t *testing.T) {
	target := "//demo/hello_go:hello-go_metadata"
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"build", target}})
	fs := newFakeFS() // no files added

	_, err := GetAppMetadata(target, bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error when metadata file is missing")
	}
}

func TestGetAppMetadataInvalidJSON(t *testing.T) {
	target := "//demo/hello_go:hello-go_metadata"
	path := metaPath("demo/hello_go", "hello-go_metadata")
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"build", target}})
	fs := newFakeFS().add(path, []byte("not json"))

	_, err := GetAppMetadata(target, bazel, fs, fakeWorkspaceRoot)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

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
		t.Fatal("expected error when bazel query fails")
	}
}

func TestListAllAppsEmptyResult(t *testing.T) {
	bazel := newFakeBazel(fakeBazelCall{argsContain: []string{"kind(app_metadata"}, output: ""})
	fs := newFakeFS()

	result, err := ListAllApps(bazel, fs, fakeWorkspaceRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 apps, got %d", len(result))
	}
}

func TestAppMetadataFullName(t *testing.T) {
	m := AppMetadata{Name: "hello-go", Domain: "demo"}
	if got := m.FullName(); got != "demo-hello-go" {
		t.Errorf("FullName() = %q, want %q", got, "demo-hello-go")
	}
}
