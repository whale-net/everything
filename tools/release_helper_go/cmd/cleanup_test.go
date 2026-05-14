package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestCleanupRequiresGithubToken(t *testing.T) {
	withEnv(map[string]string{}, func() {
		_, stderr, err := runTest([]string{"cleanup-releases", "--dry-run"})
		if err == nil {
			t.Fatal("expected error when GITHUB_TOKEN is missing")
		}
		if !strings.Contains(stderr, "GITHUB_TOKEN environment variable not set") {
			t.Errorf("expected GITHUB_TOKEN error in stderr, got: %q", stderr)
		}
	})
}

// ── identifyTagsToPrune ───────────────────────────────────────────────────────

func TestIdentifyTagsToPruneNoTags(t *testing.T) {
	git := newFakeGit()
	del, keep := identifyTagsToPrune(nil, 2, 14, git)
	if len(del) != 0 || len(keep) != 0 {
		t.Errorf("expected empty slices, got del=%v keep=%v", del, keep)
	}
}

func TestIdentifyTagsToPruneKeepRecent(t *testing.T) {
	recentDate := time.Now().Format("2006-01-02 15:04:05")
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"--format=%ai", "demo-hello-go.v1.0.0"}, output: recentDate},
		fakeGitCall{argsContain: []string{"--format=%ai", "demo-hello-go.v1.1.0"}, output: recentDate},
		fakeGitCall{argsContain: []string{"--format=%ai", "demo-hello-go.v1.2.0"}, output: recentDate},
	)
	tags := []string{
		"demo-hello-go.v1.0.0",
		"demo-hello-go.v1.1.0",
		"demo-hello-go.v1.2.0",
	}
	del, keep := identifyTagsToPrune(tags, 2, 14, git)
	if len(del) != 0 {
		t.Errorf("no tags should be deleted when all are recent, got: %v", del)
	}
	if len(keep) != 3 {
		t.Errorf("all 3 tags should be kept, got: %v", keep)
	}
}

func TestIdentifyTagsToPruneOldPatchDeleted(t *testing.T) {
	oldDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02 15:04:05")
	recentDate := time.Now().Format("2006-01-02 15:04:05")
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"--format=%ai", "demo-hello-go.v1.0.0"}, output: oldDate},
		fakeGitCall{argsContain: []string{"--format=%ai", "demo-hello-go.v1.0.1"}, output: recentDate},
	)
	tags := []string{"demo-hello-go.v1.0.0", "demo-hello-go.v1.0.1"}
	del, _ := identifyTagsToPrune(tags, 2, 14, git)
	if len(del) != 1 || del[0] != "demo-hello-go.v1.0.0" {
		t.Errorf("expected v1.0.0 to be deleted (old patch), del=%v", del)
	}
}

// ── parseSemver ───────────────────────────────────────────────────────────────

func TestParseSemver(t *testing.T) {
	tests := []struct {
		input   string
		want    semver
		wantErr bool
	}{
		{"v1.2.3", semver{1, 2, 3}, false},
		{"1.2.3", semver{1, 2, 3}, false},
		{"v1.2.3-beta1", semver{1, 2, 3}, false},
		{"invalid", semver{}, true},
		{"v1.2", semver{}, true},
	}
	for _, tt := range tests {
		got, err := parseSemver(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseSemver(%q): expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSemver(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseSemver(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestTagToPackageName(t *testing.T) {
	tests := []struct{ tag, want string }{
		{"demo-hello-go.v1.0.0", "demo-hello-go"},
		{"helm-manmanv2-control-services.v2.1.0", "helm-manmanv2-control-services"},
		{"invalid", ""},
	}
	for _, tt := range tests {
		if got := tagToPackageName(tt.tag); got != tt.want {
			t.Errorf("tagToPackageName(%q) = %q, want %q", tt.tag, got, tt.want)
		}
	}
}

func TestTagToVersion(t *testing.T) {
	if got := tagToVersion("demo-hello-go.v1.2.3"); got != "v1.2.3" {
		t.Errorf("tagToVersion = %q, want %q", got, "v1.2.3")
	}
	if got := tagToVersion("invalid"); got != "" {
		t.Errorf("tagToVersion(invalid) = %q, want %q", got, "")
	}
}
