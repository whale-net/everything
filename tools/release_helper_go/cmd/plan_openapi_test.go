package cmd

import (
	"fmt"
	"strings"
	"testing"
)

// sampleMetaJSONWithOpenAPI returns metadata JSON with an openapi_spec_target.
func sampleMetaJSONWithOpenAPI(name, domain string) []byte {
	return []byte(fmt.Sprintf(
		`{"name":%q,"domain":%q,"language":"python","registry":"ghcr.io","organization":"whale-net","repo_name":%q,"image_target":"@@//%s/%s:%s_image","binary_target":"@@//%s/%s:%s","version":"latest","openapi_spec_target":"@@//%s/%s:%s_openapi_spec"}`,
		name, domain, domain+"-"+name,
		domain, name, name,
		domain, name, name,
		domain, name, name,
	))
}

func TestPlanOpenapiBuildsInvalidFormat(t *testing.T) {
	_, stderr, err := runTest([]string{"plan-openapi-builds", "--apps", "some-app", "--format", "invalid"})
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(stderr, "format must be one of: json, github") {
		t.Errorf("expected format error in stderr, got: %q", stderr)
	}
}

func TestPlanOpenapiBuildsFiltersToSpecs(t *testing.T) {
	// hello-go has no OpenAPI spec; hello-fastapi has one
	apps := []fakeApp{
		{pkg: "demo/hello_go", targetSuffix: "hello-go_metadata", name: "hello-go", domain: "demo"},
		{
			pkg: "demo/hello_fastapi", targetSuffix: "hello-fastapi_metadata",
			name: "hello-fastapi", domain: "demo",
			customJSON: sampleMetaJSONWithOpenAPI("hello-fastapi", "demo"),
		},
	}
	fs, bazel := buildFakeInfra(apps)

	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{
					"plan-openapi-builds",
					"--apps", "demo-hello-go,demo-hello-fastapi",
					"--format", "json",
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if strings.Contains(stdout, "hello-go") {
					t.Error("hello-go should be excluded (no openapi_spec_target)")
				}
				if !strings.Contains(stdout, "hello-fastapi") {
					t.Error("hello-fastapi should be included")
				}
			})
		})
	})
}

func TestPlanOpenapiBuildsGithubFormat(t *testing.T) {
	apps := []fakeApp{
		{
			pkg: "demo/hello_fastapi", targetSuffix: "hello-fastapi_metadata",
			name: "hello-fastapi", domain: "demo",
			customJSON: sampleMetaJSONWithOpenAPI("hello-fastapi", "demo"),
		},
	}
	fs, bazel := buildFakeInfra(apps)

	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{
					"plan-openapi-builds",
					"--apps", "demo-hello-fastapi",
					"--format", "github",
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.HasPrefix(stdout, "matrix=") {
					t.Errorf("expected github format, got: %s", stdout)
				}
			})
		})
	})
}

func TestParseAppList(t *testing.T) {
	tests := []struct {
		input string
		count int
	}{
		{"a,b,c", 3},
		{"a b c", 3},
		{"a", 1},
		{"", 0},
		{"  ", 0},
	}
	for _, tt := range tests {
		got := parseAppList(tt.input)
		if len(got) != tt.count {
			t.Errorf("parseAppList(%q) = %v (len %d), want len %d", tt.input, got, len(got), tt.count)
		}
	}
}
