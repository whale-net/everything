package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// ── root builder ─────────────────────────────────────────────────────────────

// newTestRoot builds a fresh cobra root so tests are isolated from global state.
func newTestRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "release_helper",
		Short:         "Release helper for Everything monorepo",
		Long:          "Release helper for Everything monorepo — plan, build, and publish app releases.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newPlanCmd(),
		newPlanOpenapiBuildsCmd(),
		newSummaryCmd(),
		newReleaseNotesCmd(),
		newReleaseNotesAllCmd(),
		newPlanHelmReleaseCmd(),
		newBuildHelmChartCmd(),
		newCleanupReleasesCmd(),
		newUnpublishHelmChartCmd(),
		newListAppsCmd(),
		newListCmd(),
		newChangesCmd(),
	)
	return root
}

// runTest executes args against a fresh root, capturing stdout, stderr, and any error.
func runTest(args []string) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	root := newTestRoot()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// ── fake FS ──────────────────────────────────────────────────────────────────

// fakeFS is an in-memory FileSystem.
type fakeFS struct {
	files    map[string][]byte // path → content
	existing map[string]bool   // extra paths that exist but have no content
}

func newFakeFS() *fakeFS { return &fakeFS{files: make(map[string][]byte), existing: make(map[string]bool)} }

func (f *fakeFS) add(path string, content []byte) *fakeFS {
	f.files[path] = content
	return f
}

func (f *fakeFS) addExist(path string) *fakeFS {
	f.existing[path] = true
	return f
}

func (f *fakeFS) Stat(path string) (os.FileInfo, error) {
	if _, ok := f.files[path]; ok {
		return fakeFileInfo{}, nil
	}
	if f.existing[path] {
		return fakeFileInfo{}, nil
	}
	return nil, &os.PathError{Op: "stat", Path: path, Err: syscall.ENOENT}
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	if data, ok := f.files[path]; ok {
		return data, nil
	}
	return nil, &os.PathError{Op: "open", Path: path, Err: syscall.ENOENT}
}

func (f *fakeFS) WriteFile(path string, data []byte, _ os.FileMode) error {
	f.files[path] = data
	return nil
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

// ── fake Bazel runner ────────────────────────────────────────────────────────

type fakeBazelCall struct {
	argsContain    []string // all must appear in joined args
	argsNotContain []string // none must appear in joined args
	output         string
	err            error
}

type fakeBazelRunner struct {
	calls    []fakeBazelCall
	recorded [][]string
}

func newFakeBazel(calls ...fakeBazelCall) *fakeBazelRunner {
	return &fakeBazelRunner{calls: calls}
}

func (f *fakeBazelRunner) Run(args ...string) (string, error) {
	f.recorded = append(f.recorded, args)
	for _, call := range f.calls {
		if bazelCallMatches(args, call) {
			return call.output, call.err
		}
	}
	return "", fmt.Errorf("fakeBazelRunner: no match for args %v", args)
}

func bazelCallMatches(args []string, call fakeBazelCall) bool {
	joined := strings.Join(args, " ")
	for _, r := range call.argsContain {
		if !strings.Contains(joined, r) {
			return false
		}
	}
	for _, r := range call.argsNotContain {
		if strings.Contains(joined, r) {
			return false
		}
	}
	return true
}

func argsMatch(args, required []string) bool {
	joined := strings.Join(args, " ")
	for _, r := range required {
		if !strings.Contains(joined, r) {
			return false
		}
	}
	return true
}

// ── fake Git runner ──────────────────────────────────────────────────────────

type fakeGitCall struct {
	argsContain []string
	output      string
	err         error
}

type fakeGitRunner struct {
	calls    []fakeGitCall
	recorded [][]string
}

func newFakeGit(calls ...fakeGitCall) *fakeGitRunner {
	return &fakeGitRunner{calls: calls}
}

func (f *fakeGitRunner) Run(args ...string) (string, error) {
	f.recorded = append(f.recorded, args)
	for _, call := range f.calls {
		if argsMatch(args, call.argsContain) {
			return call.output, call.err
		}
	}
	return "", fmt.Errorf("fakeGitRunner: no match for args %v", args)
}

// ── injection helpers ────────────────────────────────────────────────────────

func withFS(fs FileSystem, fn func()) {
	old := defaultFS
	defaultFS = fs
	defer func() { defaultFS = old }()
	fn()
}

func withEnv(env map[string]string, fn func()) {
	old := defaultEnv
	defaultEnv = func(key string) string { return env[key] }
	defer func() { defaultEnv = old }()
	fn()
}

func withBazel(br BazelRunner, fn func()) {
	old := defaultBazel
	defaultBazel = br
	defer func() { defaultBazel = old }()
	fn()
}

func withGit(gr GitRunner, fn func()) {
	old := defaultGit
	defaultGit = gr
	defer func() { defaultGit = old }()
	fn()
}

func withWorkspace(root string, fn func()) {
	old := defaultWorkspaceRoot
	defaultWorkspaceRoot = func() (string, error) { return root, nil }
	defer func() { defaultWorkspaceRoot = old }()
	fn()
}

// ── canned metadata helpers ──────────────────────────────────────────────────

const fakeWorkspaceRoot = "/fake/workspace"

// sampleMetaJSON returns a metadata JSON blob for use in fakeFS.
func sampleMetaJSON(name, domain string) []byte {
	return []byte(fmt.Sprintf(
		`{"name":%q,"domain":%q,"language":"go","registry":"ghcr.io","organization":"whale-net","repo_name":%q,"image_target":"@@//%s/%s:%s_image","binary_target":"@@//%s/%s:%s","version":"latest"}`,
		name, domain, domain+"-"+name,
		domain, name, name,
		domain, name, name,
	))
}

// metaPath returns the expected file path for a metadata target in fakeFS.
func metaPath(pkg, targetName string) string {
	return fakeWorkspaceRoot + "/bazel-bin/" + pkg + "/" + targetName + "_metadata.json"
}

// standardFakeInfra returns a fakeFS and fakeBazelRunner for a set of apps.
// Each app is identified as "domain/name" (e.g., "demo/hello_go").
// labelSuffix is the suffix appended to the app name to form the target name
// (e.g., if the target is //demo/hello_go:hello-go_metadata, suffix = "hello-go_metadata").
type fakeApp struct {
	pkg        string // e.g., "demo/hello_go"
	targetSuffix string // e.g., "hello-go_metadata"
	name       string // e.g., "hello-go"
	domain     string // e.g., "demo"
}

func buildFakeInfra(apps []fakeApp) (*fakeFS, *fakeBazelRunner) {
	fs := newFakeFS()

	// Build query response (all metadata labels)
	labels := make([]string, len(apps))
	for i, app := range apps {
		labels[i] = "//" + app.pkg + ":" + app.targetSuffix
	}
	queryOut := strings.Join(labels, "\n")

	bazelCalls := []fakeBazelCall{
		{argsContain: []string{"kind(app_metadata"}, output: queryOut},
	}

	for _, app := range apps {
		target := "//" + app.pkg + ":" + app.targetSuffix
		// build succeeds
		bazelCalls = append(bazelCalls, fakeBazelCall{
			argsContain: []string{"build", target},
		})
		// add file to FS
		path := metaPath(app.pkg, app.targetSuffix)
		fs.add(path, sampleMetaJSON(app.name, app.domain))
	}

	return fs, newFakeBazel(bazelCalls...)
}
