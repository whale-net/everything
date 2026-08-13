package citest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/tools/app_registry/cli/cmd"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/handlers"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

// startServer brings up the real gRPC services on a loopback port, backed by
// the in-memory fake registry, with an interceptor that injects a principal
// holding every role. Auth is not what this package tests -- the command
// lines are -- and the fake enforces the same state machine and the same
// argument validation as postgres does, so a call that works here is a call
// whose shape the server accepts.
func startServer(t *testing.T) string {
	t.Helper()
	repo := fake.New()
	claims := &grpcauth.Claims{Subject: "citest", Roles: auth.AllRoles()}

	srv := grpc.NewServer(grpc.UnaryInterceptor(
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			return h(grpcauth.ContextWithClaims(ctx, claims), req)
		},
	))
	pb.RegisterAppRegistryServer(srv, handlers.NewAppServer(repo))
	pb.RegisterArtifactRegistryServer(srv, handlers.NewArtifactServer(repo))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// runCLI executes one command line through the real CLI, exactly as a CI
// shell step would, and returns its stdout. A non-nil error carries the exit
// code CI would branch on.
func runCLI(t *testing.T, args []string) (string, error) {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	root := cmd.NewRootCmd()
	root.SetArgs(args)
	runErr := root.Execute()

	_ = w.Close()
	os.Stdout = orig
	return <-done, runErr
}

// step is one call in the canonical release ordering. The argv SHAPE comes
// from the CI file named here -- not from a copy maintained in this test --
// so a workflow that adds, removes or renames a flag is exercised by this
// test the moment it changes. Only the VALUES are the test's own.
type step struct {
	name string
	file string   // CI file whose invocation of this command to replay
	path []string // subcommand path, e.g. {"artifacts", "record"}
	vars map[string]string
}

// TestCanonicalReleaseSequence replays a whole release's registry write path
// against a live server, in order, using the command lines CI actually runs.
//
// This is the test that would have caught the chart begin-publish outage.
// Each call is individually well-formed -- the contract test above passes on
// all of them -- and the defect only appears when they run in sequence
// against a server that knows what a chart is. It also covers #575's shape
// (two calls sharing an idempotency key, where the second silently replayed
// the first's response) by asserting each step's own effect, not just that
// the process exited 0.
func TestCanonicalReleaseSequence(t *testing.T) {
	addr := startServer(t)
	t.Setenv("APP_REGISTRY_ADDRESS", addr)
	t.Setenv("GRPC_AUTH_MODE", "none")
	t.Setenv("GRPC_USE_TLS", "false")

	dir := t.TempDir()
	writeManifestSet(t, dir) // read by `apps assert --from-plan $RUNNER_TEMP/manifest-set.json`
	contains := writeContainsFile(t, dir)

	const (
		imageOwner = "demo-hello"
		chartOwner = "demo-bundle"
		imageRepo  = "ghcr.io/whale-net/demo-hello"
		chartRepo  = "https://charts.example.test/demo-bundle"
		version    = "v1.2.3"
		imageSHA   = "sha256:aaaa"
		chartSHA   = "sha256:bbbb"
		prefix     = "run7-1"
	)

	byKey := invocationsByKey(t)

	// 1-2 establish identity and the build; 3-6 are the two publish pairs.
	// The idempotency keys mirror release.yml's own scheme, including the
	// distinct "-begin" / "-record" suffixes that issue #575 introduced --
	// if those ever collapse back to one key per target, the assertions
	// below on state and digest catch the silent replay.
	steps := []step{{
		name: "assert app/chart identity",
		file: ".github/actions/app-registry-assert/action.yml",
		path: []string{"apps", "assert"},
		vars: map[string]string{
			"RUNNER_TEMP":     dir,
			"IDEMPOTENCY_KEY": prefix + "-assert",
		},
	}, {
		name: "record build",
		file: ".github/actions/app-registry-record-build/action.yml",
		path: []string{"builds", "record"},
		vars: map[string]string{
			"GIT_SHA": "f8793681", "GIT_REF": "refs/heads/main",
			"WORKFLOW_RUN_ID": "31564279457", "WORKFLOW_ATTEMPT": "1",
			"ACTOR": "citest", "IDEMPOTENCY_KEY": prefix,
		},
	}, {
		name: "begin publish (image)",
		file: ".github/actions/app-registry-begin-publish/action.yml",
		path: []string{"artifacts", "begin-publish"},
		vars: map[string]string{
			"KIND": "image", "OWNER": imageOwner, "VERSION": version,
			"IDEMPOTENCY_KEY": prefix + "-" + imageOwner + "-image-begin",
		},
	}, {
		name: "record image artifact",
		file: ".github/actions/app-registry-record-image/action.yml",
		path: []string{"artifacts", "record"},
		vars: map[string]string{
			"OWNER": imageOwner, "REPOSITORY": imageRepo, "VERSION": version,
			"DIGEST":          imageSHA,
			"IDEMPOTENCY_KEY": prefix + "-" + imageOwner + "-image-record",
		},
	}, {
		name: "begin publish (chart)",
		file: ".github/workflows/release.yml",
		path: []string{"artifacts", "begin-publish"},
		vars: map[string]string{
			"CHART_REPO_URL": "https://charts.example.test",
			"PUBLISHED_NAME": chartOwner, "CHART_VERSION": version,
			"IDEMPOTENCY_PREFIX": prefix,
		},
	}, {
		name: "record chart artifact",
		file: ".github/workflows/release.yml",
		path: []string{"artifacts", "record"},
		vars: map[string]string{
			"CHART_REPO_URL": "https://charts.example.test",
			"PUBLISHED_NAME": chartOwner, "CHART_VERSION": version,
			"CHART_DIGEST": chartSHA, "CONTAINS_FILE": contains,
			"IDEMPOTENCY_PREFIX": prefix,
		},
	}}

	var buildID string
	for _, s := range steps {
		inv, ok := byKey[key(s.file, s.path)]
		if !ok {
			t.Fatalf("step %q: no `app-registry %s` invocation found in %s -- the workflow changed and this test's step list did not", s.name, strings.Join(s.path, " "), s.file)
		}
		if buildID != "" {
			s.vars["BUILD_ID"] = buildID
		}
		args := expand(inv.Args, s.vars)
		out, err := runCLI(t, args)
		if err != nil {
			t.Fatalf("step %q failed (CI would exit %d)\n  %s\n  args: %s\n  error: %v",
				s.name, cmd.ExitCodeFor(err), inv, strings.Join(args, " "), err)
		}
		if s.path[0] == "builds" {
			buildID = buildIDFrom(t, out)
		}
	}
	if buildID == "" {
		t.Fatal("no build id was ever captured -- the record-build step's JSON shape changed, and .github/actions/app-registry-record-build's `jq -r '.build.buildId'` would now yield null")
	}

	// The sequence exiting 0 is not the assertion -- issue #575 was a case
	// where every step exited 0 and the work silently did not happen. Assert
	// the end state each pair was supposed to produce.
	for _, want := range []struct{ kind, owner, digest, repo string }{
		{"image", imageOwner, imageSHA, imageRepo},
		{"chart", chartOwner, chartSHA, chartRepo},
	} {
		out, err := runCLI(t, []string{"artifacts", "get", want.owner, "--kind", want.kind, "--version", version})
		if err != nil {
			t.Fatalf("get %s %s: %v", want.kind, want.owner, err)
		}
		var got struct {
			Artifact struct {
				State, Digest, Repository string
			}
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("parse artifacts get output: %v\n%s", err, out)
		}
		if got.Artifact.State != "ARTIFACT_STATE_PUBLISHED" {
			t.Errorf("%s %s ended in state %q, want ARTIFACT_STATE_PUBLISHED -- the begin/record pair did not complete", want.kind, want.owner, got.Artifact.State)
		}
		if got.Artifact.Digest != want.digest {
			t.Errorf("%s %s digest = %q, want %q -- RecordArtifact did not execute (a replayed BeginPublish response would look like this)", want.kind, want.owner, got.Artifact.Digest, want.digest)
		}
		if got.Artifact.Repository != want.repo {
			t.Errorf("%s %s repository = %q, want %q", want.kind, want.owner, got.Artifact.Repository, want.repo)
		}
	}
}

func key(file string, path []string) string { return file + "|" + strings.Join(path, " ") }

// invocationsByKey indexes extracted invocations by (file, subcommand path).
// Each CI file calls a given subcommand at most once today; a second one
// would silently shadow the first, so that is checked rather than assumed.
func invocationsByKey(t *testing.T) map[string]Invocation {
	t.Helper()
	out := map[string]Invocation{}
	for _, inv := range allInvocations(t) {
		if inv.Dynamic || len(inv.Args) < 2 {
			continue
		}
		k := key(inv.File, inv.Args[:2])
		if prev, dup := out[k]; dup {
			t.Fatalf("two `app-registry %s` invocations in %s (lines %d and %d) -- this index assumes one, so a step would silently replay the wrong command line", strings.Join(inv.Args[:2], " "), inv.File, prev.Line, inv.Line)
		}
		out[k] = inv
	}
	return out
}

// expand substitutes $VAR and ${VAR} from vars. An unresolved reference is
// left as-is rather than blanked, so it shows up in the failing command line
// instead of turning into a confusing empty argument.
func expand(args []string, vars map[string]string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "${"+k+"}", v)
			a = strings.ReplaceAll(a, "$"+k, v)
		}
		out[i] = a
	}
	return out
}

func buildIDFrom(t *testing.T, out string) string {
	t.Helper()
	// Same path .github/actions/app-registry-record-build reads with
	// `jq -r '.build.buildId'`.
	var resp struct {
		Build struct {
			BuildID string `json:"buildId"`
		} `json:"build"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("parse builds record output: %v\n%s", err, out)
	}
	return resp.Build.BuildID
}

// writeManifestSet writes the AppManifestSet `apps assert --from-plan`
// reads, at the path the composite action names ($RUNNER_TEMP/...).
func writeManifestSet(t *testing.T, dir string) string {
	t.Helper()
	set := &appmetapb.AppManifestSet{
		GitSha: "f8793681", SourceCommittedAt: 1, DiscoveredAt: 2,
		Apps: []*appmetapb.AppManifest{{
			Domain: "demo", Name: "hello", Description: "d", Language: "go", AppType: "worker",
			DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
			Registry:   "ghcr.io", Organization: "whale-net", RepoName: "demo-hello",
		}},
		// Named the way release_helm_chart's Bazel macro names charts, so
		// the owner the release steps below pass ("demo-bundle") is only
		// resolvable if the server normalizes the prefix off.
		Charts: []*appmetapb.ChartManifest{{
			Domain: "demo", Name: "helm-demo-bundle", AppRefs: []string{"demo/hello"},
		}},
	}
	b, err := protojson.Marshal(set)
	if err != nil {
		t.Fatalf("marshal manifest set: %v", err)
	}
	p := filepath.Join(dir, "manifest-set.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write manifest set: %v", err)
	}
	return p
}

// writeContainsFile writes the resolved-image-digest file the chart record
// step passes as --contains.
func writeContainsFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "contains.json")
	body := fmt.Sprintf(`[{"app_full_name":%q,"repository":%q,"version":%q,"digest":%q}]`,
		"demo-hello", "ghcr.io/whale-net/demo-hello", "v1.2.3", "sha256:aaaa")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write contains file: %v", err)
	}
	return p
}
