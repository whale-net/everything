package changes

import (
	"testing"

	"github.com/whale-net/everything/tools/release/pkg/metadata"
)

func TestDetectChangedAppsSimple(t *testing.T) {
	allApps := []metadata.AppInfo{
		{
			BazelTarget: "//demo/hello_python:hello_python_metadata",
			Name:        "hello_python",
			Domain:      "demo",
		},
		{
			BazelTarget: "//demo/hello_go:hello_go_metadata",
			Name:        "hello_go",
			Domain:      "demo",
		},
		{
			BazelTarget: "//api/auth:auth_metadata",
			Name:        "auth",
			Domain:      "api",
		},
	}

	tests := []struct {
		name         string
		changedFiles []string
		want         []string // app names
	}{
		{
			name: "python app changed",
			changedFiles: []string{
				"demo/hello_python/main.py",
				"demo/hello_python/BUILD.bazel",
			},
			want: []string{"hello_python"},
		},
		{
			name: "multiple apps changed",
			changedFiles: []string{
				"demo/hello_python/main.py",
				"demo/hello_go/main.go",
			},
			want: []string{"hello_python", "hello_go"},
		},
		{
			name: "no app files changed",
			changedFiles: []string{
				"README.md",
				"docs/guide.md",
			},
			want: []string{},
		},
		{
			name: "api app changed",
			changedFiles: []string{
				"api/auth/handler.go",
			},
			want: []string{"auth"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectChangedAppsSimple(tt.changedFiles, allApps)
			if err != nil {
				t.Errorf("detectChangedAppsSimple() error = %v", err)
				return
			}

			gotNames := make([]string, len(got))
			for i, app := range got {
				gotNames[i] = app.Name
			}

			if len(gotNames) != len(tt.want) {
				t.Errorf("detectChangedAppsSimple() returned %d apps, want %d", len(gotNames), len(tt.want))
				return
			}

			// Check that all expected apps are in the result
			wantMap := make(map[string]bool)
			for _, name := range tt.want {
				wantMap[name] = true
			}

			for _, name := range gotNames {
				if !wantMap[name] {
					t.Errorf("detectChangedAppsSimple() returned unexpected app: %s", name)
				}
			}
		})
	}
}

func TestDetectChangedAppsWithBazelQuery(t *testing.T) {
	allApps := []metadata.AppInfo{
		{
			BazelTarget: "//demo/hello_python:hello_python_metadata",
			Name:        "hello_python",
			Domain:      "demo",
		},
	}

	changedFiles := []string{"demo/hello_python/main.py"}

	// This test just verifies the function runs without error
	// In a real test, we'd mock the Bazel query calls
	_, err := detectChangedAppsWithBazelQuery(changedFiles, allApps)
	if err != nil {
		t.Errorf("detectChangedAppsWithBazelQuery() error = %v", err)
	}
}
