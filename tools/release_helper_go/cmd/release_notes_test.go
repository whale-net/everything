package cmd

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseTagInfo(t *testing.T) {
	tests := []struct {
		tag        string
		wantDomain string
		wantApp    string
		wantVer    string
		wantErr    bool
	}{
		// rsplit on last '-': matches Python's domain_app.rsplit('-', 1) behavior
		{"demo-hello-go.v1.2.3", "demo-hello", "go", "v1.2.3", false},
		{"friendly-computing-machine-bot.v0.1.0", "friendly-computing-machine", "bot", "v0.1.0", false},
		{"manmanv2-control-api.v1.0.0", "manmanv2-control", "api", "v1.0.0", false},
		{"invalid", "", "", "", true},
		{"nodash.v1.0.0", "", "", "", true},
	}
	for _, tt := range tests {
		d, a, v, err := parseTagInfo(tt.tag)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseTagInfo(%q): expected error", tt.tag)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTagInfo(%q): unexpected error: %v", tt.tag, err)
			continue
		}
		if d != tt.wantDomain || a != tt.wantApp || v != tt.wantVer {
			t.Errorf("parseTagInfo(%q) = (%q, %q, %q), want (%q, %q, %q)", tt.tag, d, a, v, tt.wantDomain, tt.wantApp, tt.wantVer)
		}
	}
}

func TestFilterCommitsByApp(t *testing.T) {
	commits := []releaseCommit{
		{SHA: "abc", Message: "fix thing", FilesChanged: []string{"demo/hello_go/main.go"}},
		{SHA: "def", Message: "update docs", FilesChanged: []string{"docs/README.md"}},
		{SHA: "ghi", Message: "ci change", FilesChanged: []string{".github/workflows/ci.yml"}},
	}

	// Filter for demo/hello_go
	got := filterCommitsByApp(commits, "demo/hello_go")
	// "abc" matches app path, "ghi" matches .github infra prefix
	if len(got) != 2 {
		t.Errorf("expected 2 commits (app + infra), got %d: %v", len(got), got)
	}
}

func TestFilterCommitsByAppEmptyPath(t *testing.T) {
	commits := []releaseCommit{
		{SHA: "abc", Message: "test", FilesChanged: []string{"anything/main.go"}},
	}
	got := filterCommitsByApp(commits, "")
	if len(got) != 1 {
		t.Errorf("empty appPath should return all commits, got %d", len(got))
	}
}

func TestFormatMarkdown(t *testing.T) {
	d := appReleaseData{
		AppName:     "demo-hello-go",
		CurrentTag:  "HEAD",
		PreviousTag: "demo-hello-go.v1.0.0",
		ReleasedAt:  "2026-01-01 00:00:00 UTC",
		Commits: []releaseCommit{
			{SHA: "abc12345", Message: "add feature", Author: "dev", Date: "2026-01-01", FilesChanged: []string{"main.go"}},
		},
	}
	out := formatMarkdown(d)
	if !strings.Contains(out, "## Changes") {
		t.Error("expected '## Changes' heading")
	}
	if !strings.Contains(out, "add feature") {
		t.Error("expected commit message in output")
	}
}

func TestFormatMarkdownNoCommits(t *testing.T) {
	d := appReleaseData{
		AppName:     "demo-hello-go",
		CurrentTag:  "HEAD",
		PreviousTag: "v1.0.0",
		ReleasedAt:  "2026-01-01 00:00:00 UTC",
		Commits:     nil,
	}
	out := formatMarkdown(d)
	if !strings.Contains(out, "No changes") {
		t.Error("expected 'No changes' message for empty commits")
	}
}

func TestFormatPlain(t *testing.T) {
	d := appReleaseData{
		AppName:     "demo-hello-go",
		CurrentTag:  "demo-hello-go.v1.1.0",
		PreviousTag: "demo-hello-go.v1.0.0",
		ReleasedAt:  "2026-01-01 00:00:00 UTC",
		Commits: []releaseCommit{
			{SHA: "abc", Message: "fix bug", Author: "dev", Date: "2026-01-01"},
		},
	}
	out := formatPlain(d)
	// Title parsed from tag: "demo hello-go v1.1.0"
	if !strings.Contains(out, "hello-go") {
		t.Error("expected app name in plain output")
	}
	if !strings.Contains(out, "fix bug") {
		t.Error("expected commit message in plain output")
	}
}

func TestFormatJSON(t *testing.T) {
	d := appReleaseData{
		AppName:     "demo-hello-go",
		CurrentTag:  "HEAD",
		PreviousTag: "v1.0.0",
		ReleasedAt:  "2026-01-01",
		Commits:     nil,
	}
	out, err := formatJSON(d)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"app"`) {
		t.Error("expected 'app' field in JSON output")
	}
	if !strings.Contains(out, `"changes"`) {
		t.Error("expected 'changes' field in JSON output")
	}
}

func TestGetCommitsBetweenRefsNoBaseRef(t *testing.T) {
	// When base ref doesn't exist, falls back to last 5 commits
	git := newFakeGit(
		fakeGitCall{argsContain: []string{"rev-parse", "--verify"}, err: fmt.Errorf("not found")},
		fakeGitCall{argsContain: []string{"log", "-n", "5"}, output: "abc12345|add feature|dev|2026-01-01 00:00:00 +0000"},
		fakeGitCall{argsContain: []string{"diff-tree"}, output: "main.go"},
	)
	commits, err := getCommitsBetweenRefs("nonexistent-tag", "HEAD", git)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 1 {
		t.Errorf("expected 1 commit, got %d", len(commits))
	}
}
