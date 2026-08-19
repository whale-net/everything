package cmd

import (
	"context"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
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

func TestBuildCompareURL(t *testing.T) {
	tests := []struct {
		name    string
		owner   string
		repo    string
		prevRef string
		currRef string
		want    string
	}{
		{
			name:    "valid refs",
			owner:   "whale-net",
			repo:    "everything",
			prevRef: "abc1234",
			currRef: "def5678",
			want:    "https://github.com/whale-net/everything/compare/abc1234...def5678",
		},
		{
			name:    "empty prevRef",
			owner:   "whale-net",
			repo:    "everything",
			prevRef: "",
			currRef: "def5678",
			want:    "",
		},
		{
			name:    "same refs",
			owner:   "whale-net",
			repo:    "everything",
			prevRef: "abc1234",
			currRef: "abc1234",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCompareURL(tt.owner, tt.repo, tt.prevRef, tt.currRef)
			if got != tt.want {
				t.Errorf("buildCompareURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatAppMarkdown(t *testing.T) {
	d := AppReleaseNotesData{
		AppName:         "demo-hello-go",
		Version:         "v1.2.0",
		PreviousVersion: "v1.1.0",
		ReleasedAt:      "2026-08-19 00:00:00 UTC",
		DiffURL:         "https://github.com/whale-net/everything/compare/v1.1.0...v1.2.0",
	}
	out := formatAppMarkdown(d)
	if !strings.Contains(out, "## Release `demo-hello-go` `v1.2.0`") {
		t.Errorf("expected release heading in output: %s", out)
	}
	if !strings.Contains(out, "(Previous: `v1.1.0`)") {
		t.Errorf("expected previous version in output: %s", out)
	}
	if !strings.Contains(out, "https://github.com/whale-net/everything/compare/v1.1.0...v1.2.0") {
		t.Errorf("expected diff url in output: %s", out)
	}
}

func TestFormatAppMarkdownInitialRelease(t *testing.T) {
	d := AppReleaseNotesData{
		AppName:    "demo-hello-go",
		Version:    "v1.0.0",
		ReleasedAt: "2026-08-19 00:00:00 UTC",
	}
	out := formatAppMarkdown(d)
	if !strings.Contains(out, "(Initial release)") {
		t.Errorf("expected Initial release text in output: %s", out)
	}
}

func TestFormatChartMarkdown(t *testing.T) {
	d := ChartReleaseNotesData{
		ChartName:       "helm-demo-hello-fastapi",
		Version:         "v0.2.0",
		PreviousVersion: "v0.1.0",
		ReleasedAt:      "2026-08-19 00:00:00 UTC",
		DiffURL:         "https://github.com/whale-net/everything/compare/prev...curr",
		Images: []ContainedImageNote{
			{
				AppFullName:     "demo-hello-fastapi",
				Version:         "v1.1.0",
				PreviousVersion: "v1.0.0",
				Digest:          "sha256:1234567890abcdef123456",
				DiffURL:         "https://github.com/whale-net/everything/compare/appprev...appcurr",
			},
		},
	}
	out := formatChartMarkdown(d)
	if !strings.Contains(out, "## Release `helm-demo-hello-fastapi` `v0.2.0`") {
		t.Errorf("expected chart heading in output: %s", out)
	}
	if !strings.Contains(out, "### Contained Images") {
		t.Errorf("expected Contained Images heading in output: %s", out)
	}
	if !strings.Contains(out, "| `demo-hello-fastapi` | `v1.1.0` *(from v1.0.0)* | `sha256:1234567890ab...` | [Compare](https://github.com/whale-net/everything/compare/appprev...appcurr) |") {
		t.Errorf("expected contained image table row in output: %s", out)
	}
}

// fakeNotesRegistryClient implements pb.ArtifactRegistryClient for testing resolvePreviousRef
type fakeNotesRegistryClient struct {
	pb.ArtifactRegistryClient
	getArtifactResp *pb.GetArtifactResponse
	getArtifactErr  error
	enforced        bool
}

func (f *fakeNotesRegistryClient) GetArtifact(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
	return f.getArtifactResp, f.getArtifactErr
}

func (f *fakeNotesRegistryClient) CheckChartHermeticity(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
	return &pb.CheckChartHermeticityResponse{Enforced: f.enforced}, nil
}

func TestResolvePreviousRefAppRegistry(t *testing.T) {
	t.Setenv("APP_REGISTRY_CICD_OPT_IN", "true")
	fakeClient := &fakeNotesRegistryClient{
		enforced: true,
		getArtifactResp: &pb.GetArtifactResponse{
			Artifact: &pb.Artifact{
				Version: "v1.2.0",
			},
			Build: &pb.Build{
				GitSha: "deadbeef12345678",
			},
		},
	}

	prevVer, prevSHA, prevTag := resolvePreviousRef(
		context.Background(),
		"demo-hello-go",
		"demo",
		pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		"v1.3.0",
		"demo-hello-go.v1.3.0",
		nil,
		fakeClient,
	)

	if prevVer != "v1.2.0" {
		t.Errorf("resolvePreviousRef prevVer = %q, want v1.2.0", prevVer)
	}
	if prevSHA != "deadbeef12345678" {
		t.Errorf("resolvePreviousRef prevSHA = %q, want deadbeef12345678", prevSHA)
	}
	if prevTag != "demo-hello-go.v1.2.0" {
		t.Errorf("resolvePreviousRef prevTag = %q, want demo-hello-go.v1.2.0", prevTag)
	}
}

func TestResolvePreviousRefGitTagFallback(t *testing.T) {
	git := newFakeGit(
		fakeGitCall{
			argsContain: []string{"tag", "--sort=-version:refname"},
			output:      "demo-hello-go.v1.2.0\ndemo-hello-go.v1.1.0\ndemo-hello-go.v1.0.0",
		},
		fakeGitCall{
			argsContain: []string{"rev-parse", "demo-hello-go.v1.1.0"},
			output:      "1111222233334444\n",
		},
	)

	prevVer, prevSHA, prevTag := resolvePreviousRef(
		context.Background(),
		"demo-hello-go",
		"demo",
		pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		"v1.2.0",
		"demo-hello-go.v1.2.0",
		git,
		nil,
	)

	if prevVer != "v1.1.0" {
		t.Errorf("resolvePreviousRef prevVer = %q, want v1.1.0", prevVer)
	}
	if prevSHA != "1111222233334444" {
		t.Errorf("resolvePreviousRef prevSHA = %q, want 1111222233334444", prevSHA)
	}
	if prevTag != "demo-hello-go.v1.1.0" {
		t.Errorf("resolvePreviousRef prevTag = %q, want demo-hello-go.v1.1.0", prevTag)
	}
}

