package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

type fakeArtifactRecorder struct {
	recorded []*pb.RecordArtifactRequest
	err      error
}

func (f *fakeArtifactRecorder) RecordArtifact(_ context.Context, req *pb.RecordArtifactRequest) (*pb.RecordArtifactResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.recorded = append(f.recorded, req)
	return &pb.RecordArtifactResponse{
		Artifact: &pb.Artifact{
			ArtifactId: "fake-artifact-123",
			Kind:       req.Kind,
			Version:    req.Version,
			Digest:     req.Digest,
		},
	}, nil
}

func TestPackageAppAssets_CLI(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "package-cli-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	wsRoot := filepath.Join(tmpDir, "ws")
	binDir := filepath.Join(wsRoot, "bazel-bin", "tools", "release_helper_go", "release_helper_go_")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	fakeBinPath := filepath.Join(binDir, "release_helper_go")
	if err := os.WriteFile(fakeBinPath, []byte("fake-cli-binary-content"), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	outDir := filepath.Join(tmpDir, "out")

	app := AppMetadata{
		AppManifest: &appmetapb.AppManifest{
			Name:         "release_helper_go",
			Domain:       "tools",
			AppType:      "cli",
			BinaryTarget: "//tools/release_helper_go:release_helper_go",
		},
		BazelTarget: "//tools/release_helper_go:release_helper_go_metadata",
	}

	bazelCalls := []fakeBazelCall{
		{argsContain: []string{"build", "--platforms=//tools:linux_x86_64"}, output: ""},
		{argsContain: []string{"build", "--platforms=//tools:linux_arm64"}, output: ""},
		{argsContain: []string{"build", "--platforms=//tools:darwin_arm64"}, output: ""},
	}
	fakeBazel := newFakeBazel(bazelCalls...)

	assets, err := PackageAppAssets(fakeBazel, defaultFS, wsRoot, app, outDir, nil)
	if err != nil {
		t.Fatalf("PackageAppAssets failed: %v", err)
	}

	if len(assets) != 3 {
		t.Fatalf("Expected 3 assets, got %d", len(assets))
	}

	expectedNames := []string{
		"release_helper_go-darwin-arm64",
		"release_helper_go-linux-amd64",
		"release_helper_go-linux-arm64",
	}

	for i, name := range expectedNames {
		if assets[i].Name != name {
			t.Errorf("Asset %d name = %s, want %s", i, assets[i].Name, name)
		}
		if assets[i].SHA256 == "" {
			t.Errorf("Asset %d has empty SHA256", i)
		}
	}

	// Verify checksum files
	for _, sumFile := range []string{"checksums.txt", "SHA256SUMS"} {
		sumPath := filepath.Join(outDir, sumFile)
		data, err := os.ReadFile(sumPath)
		if err != nil {
			t.Fatalf("Failed to read %s: %v", sumFile, err)
		}
		content := string(data)
		for _, name := range expectedNames {
			if !strings.Contains(content, name) {
				t.Errorf("%s missing entry for %s", sumFile, name)
			}
		}
	}
}

func TestPackageAppAssets_Firmware(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "package-firmware-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	wsRoot := filepath.Join(tmpDir, "ws")
	binDir := filepath.Join(wsRoot, "bazel-bin", "demo", "blink")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	fakeBinPath := filepath.Join(binDir, "blink_merged.bin")
	if err := os.WriteFile(fakeBinPath, []byte("fake-firmware-bin-data"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	outDir := filepath.Join(tmpDir, "out")

	app := AppMetadata{
		AppManifest: &appmetapb.AppManifest{
			Name:         "blink",
			Domain:       "demo",
			AppType:      "firmware",
			BinaryTarget: "//demo/blink:blink",
		},
		BazelTarget: "//demo/blink:blink_metadata",
	}

	bazelCalls := []fakeBazelCall{
		{argsContain: []string{"build", "--config=esp32", "//demo/blink:blink_merged_bin"}, output: ""},
	}
	fakeBazel := newFakeBazel(bazelCalls...)

	assets, err := PackageAppAssets(fakeBazel, defaultFS, wsRoot, app, outDir, nil)
	if err != nil {
		t.Fatalf("PackageAppAssets failed: %v", err)
	}

	if len(assets) != 1 {
		t.Fatalf("Expected 1 asset, got %d", len(assets))
	}

	if assets[0].Name != "blink-esp32-merged.bin" {
		t.Errorf("Expected blink-esp32-merged.bin, got %s", assets[0].Name)
	}

	// Verify checksum files
	for _, sumFile := range []string{"checksums.txt", "SHA256SUMS"} {
		sumPath := filepath.Join(outDir, sumFile)
		data, err := os.ReadFile(sumPath)
		if err != nil {
			t.Fatalf("Failed to read %s: %v", sumFile, err)
		}
		if !strings.Contains(string(data), "blink-esp32-merged.bin") {
			t.Errorf("%s missing entry for blink-esp32-merged.bin", sumFile)
		}
	}
}

func TestRecordPublishedArtifact_BinaryAndFirmware(t *testing.T) {
	fakeRecorder := &fakeArtifactRecorder{}

	cliApp := AppMetadata{
		AppManifest: &appmetapb.AppManifest{
			Name:    "release_helper_go",
			Domain:  "tools",
			AppType: "cli",
		},
	}

	fwApp := AppMetadata{
		AppManifest: &appmetapb.AppManifest{
			Name:    "blink",
			Domain:  "demo",
			AppType: "firmware",
		},
	}

	warnMsgs := []string{}
	warn := func(msg string) { warnMsgs = append(warnMsgs, msg) }

	withEnv(map[string]string{
		"APP_REGISTRY_CICD_OPT_IN": "true",
	}, func() {
		withArtifactRecorder(fakeRecorder, func() {
			// Record CLI binary
			err := recordPublishedArtifact(context.Background(), warn, cliApp, "v0.1.0", "abcdef1234567890", "whale-net", "everything", "build-1")
			if err != nil {
				t.Fatalf("recordPublishedArtifact CLI failed: %v", err)
			}

			// Record firmware
			err = recordPublishedArtifact(context.Background(), warn, fwApp, "v0.2.0", "sha256:112233445566", "whale-net", "everything", "build-2")
			if err != nil {
				t.Fatalf("recordPublishedArtifact Firmware failed: %v", err)
			}
		})
	})

	if len(fakeRecorder.recorded) != 2 {
		t.Fatalf("Expected 2 recorded artifacts, got %d", len(fakeRecorder.recorded))
	}

	recCLI := fakeRecorder.recorded[0]
	if recCLI.Kind != pb.ArtifactKind_ARTIFACT_KIND_BINARY {
		t.Errorf("Expected ARTIFACT_KIND_BINARY, got %v", recCLI.Kind)
	}
	if recCLI.OwnerFullName != "tools-release_helper_go" {
		t.Errorf("Expected tools-release_helper_go, got %s", recCLI.OwnerFullName)
	}
	if recCLI.Digest != "sha256:abcdef1234567890" {
		t.Errorf("Expected sha256:abcdef1234567890, got %s", recCLI.Digest)
	}

	recFW := fakeRecorder.recorded[1]
	if recFW.Kind != pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE {
		t.Errorf("Expected ARTIFACT_KIND_FIRMWARE, got %v", recFW.Kind)
	}
	if recFW.OwnerFullName != "demo-blink" {
		t.Errorf("Expected demo-blink, got %s", recFW.OwnerFullName)
	}
	if recFW.Digest != "sha256:112233445566" {
		t.Errorf("Expected sha256:112233445566, got %s", recFW.Digest)
	}
}

func TestPackageAssetsCmd_Execution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cmd-package-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	wsRoot := filepath.Join(tmpDir, "ws")
	binDir := filepath.Join(wsRoot, "bazel-bin", "tools", "release_helper_go", "release_helper_go_")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "release_helper_go"), []byte("bin-content"), 0755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	appJSON := []byte(`{"name":"release_helper_go","domain":"tools","language":"go","app_type":"cli","binary_target":"//tools/release_helper_go:release_helper_go","version":"latest"}`)
	apps := []fakeApp{
		{pkg: "tools/release_helper_go", targetSuffix: "release_helper_go_metadata", name: "release_helper_go", domain: "tools", customJSON: appJSON},
	}

	fs, bazel := buildFakeInfra(apps)
	bazel.calls = append(bazel.calls,
		fakeBazelCall{argsContain: []string{"build", "--platforms=//tools:linux_x86_64"}, output: ""},
		fakeBazelCall{argsContain: []string{"build", "--platforms=//tools:linux_arm64"}, output: ""},
		fakeBazelCall{argsContain: []string{"build", "--platforms=//tools:darwin_arm64"}, output: ""},
	)

	outDir := filepath.Join(tmpDir, "assets-out")

	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(wsRoot, func() {
				stdout, stderr, err := runTest([]string{
					"package-assets",
					"tools-release_helper_go",
					"--version", "v0.1.0",
					"--output-dir", outDir,
				})
				if err != nil {
					t.Fatalf("package-assets failed: %v (stderr: %s)", err, stderr)
				}
				if !strings.Contains(stdout, "Successfully packaged") {
					t.Errorf("Expected success output, got: %s", stdout)
				}
				if !strings.Contains(stdout, "release_helper_go-linux-amd64") {
					t.Errorf("Missing linux-amd64 in output: %s", stdout)
				}
			})
		})
	})
}
