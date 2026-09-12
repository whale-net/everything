//go:build integration

// This file only builds under the "integration" build tag so that `bazel
// test //...` (which builds and runs the whole tree, including on
// Docker-less machines) never even compiles it, let alone runs it -- see
// the image_integration_test go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest's postgres_constraints_test.go, which this mirrors.
package imageintegration

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// platform is one of the two architectures krill ships images for -- see
// docs/DOCKER.md "Base Images & Architecture".
type platform struct {
	os, arch string
}

func (p platform) String() string { return p.os + "/" + p.arch }

var platforms = []platform{
	{os: "linux", arch: "amd64"},
	{os: "linux", arch: "arm64"},
}

// image identifies one of krill's three release images by its runfile-
// relative OCI layout (the index.json next to the blobs/ directory
// oci_image_index produces) and the entrypoint path
// tools/bazel/container_image.bzl bakes in for a go_binary:
// "/app/{binary_name}_/{binary_name}".
type image struct {
	name           string
	layoutIndex    string
	entrypointPath string
}

var images = []image{
	{name: "migrate", layoutIndex: "_main/krill/migrate/migrate_image/index.json", entrypointPath: "/app/migrate_/migrate"},
	{name: "api", layoutIndex: "_main/krill/api/api_image/index.json", entrypointPath: "/app/api_/api"},
	{name: "mcp", layoutIndex: "_main/krill/mcp/mcp_image/index.json", entrypointPath: "/app/mcp_/mcp"},
}

// TestNFR2_ImageIntegration runs krill's NFR2 cross-compilation validation
// (issue #2499): for each of linux/amd64 and linux/arm64, it extracts the
// actual migrate/api/mcp binaries baked into their release images straight
// out of the built OCI image index -- the same multiplatform artifact
// //tools:release pushes (see //krill/BUILD.bazel's release_helm_chart and
// docs/DOCKER.md) -- and runs them for real.
//
// A build-only check would not catch what docs/DOCKER.md warns about:
// ARM64 cross-compilation breakage is silent at build time and only fails
// at runtime. So for each platform this asserts:
//   - migrate applies krill's schema against a real, empty Postgres, then
//     a second `up` run against the now-migrated database is a genuine
//     no-op (migration_history gains no new rows).
//   - api's /healthz reports {"status":"ok"} (it pings the database, see
//     krill/api/routes.go's handleHealthz -- a static 200 would not prove
//     anything).
//   - mcp's /healthz is live, and POST /mcp/spec's `initialize` handshake
//     reaches the mcpauth front door and is cleanly rejected (401, "no
//     bearer token") rather than hanging, crashing, or refusing the
//     connection -- proof the MCP transport and its routing are alive
//     under this platform, not just that the process started.
//
// krill's Go binaries are CGO_ENABLED=0, statically linked (see
// krill/*/BUILD.bazel's go_binary targets and container_image.bzl's Go
// entrypoint convention, "/app/{binary}_/{binary}"), so once extracted
// they can be exec'd directly -- no base-image libc needed. For arm64 on
// an amd64 host this relies on binfmt_misc having qemu-aarch64 registered
// (CI's image-integration job runs docker/setup-qemu-action before this
// test; locally: `docker run --rm --privileged multiarch/qemu-user-static
// --reset -p yes`). Postgres, which every binary here needs, still comes
// from //libs/go/dbtest's testcontainers-go container, so this test still
// needs a live Docker daemon -- see BUILD.bazel's no-sandbox/manual tags,
// mirroring //libs/go/dbtest:postgres_constraints_test.
//
// Run it explicitly:
//
//	bazel test //krill/imageintegration:image_integration_test \
//	  --test_tag_filters=-manual --test_output=all
func TestNFR2_ImageIntegration(t *testing.T) {
	byName := map[string]image{}
	for _, img := range images {
		byName[img.name] = img
	}

	for _, plat := range platforms {
		plat := plat
		t.Run(plat.String(), func(t *testing.T) {
			migrateBin := extractBinary(t, byName["migrate"], plat)
			apiBin := extractBinary(t, byName["api"], plat)
			mcpBin := extractBinary(t, byName["mcp"], plat)

			ctx := context.Background()
			pg := dbtest.NewPostgres(ctx, t, dbtest.Options{})

			runMigrate(t, migrateBin, pg.ConnString)
			before := migrationHistoryCount(ctx, t, pg)

			// The NFR2-required no-op assertion: the same binary against
			// the same already-migrated database must succeed without
			// applying anything new.
			runMigrate(t, migrateBin, pg.ConnString)
			after := migrationHistoryCount(ctx, t, pg)
			if after != before {
				t.Errorf("expected the second `migrate up` run to be a no-op, but migration_history went from %d to %d rows", before, after)
			}

			assertAPIHealthz(t, apiBin, pg.ConnString)
			assertMCPHandshake(t, mcpBin, pg.ConnString)
		})
	}
}

// extractBinary pulls img.entrypointPath out of img's OCI image index for
// plat, writes it to a fresh temp file with exec permission, and returns
// that file's path.
func extractBinary(t *testing.T, img image, plat platform) string {
	t.Helper()

	indexJSON, err := runfiles.Rlocation(img.layoutIndex)
	if err != nil {
		t.Fatalf("runfiles.Rlocation(%s): %v (is //krill/%s:%s_image still a data dep of this test target?)",
			img.layoutIndex, err, img.name, img.name)
	}
	layoutDir := filepath.Dir(indexJSON)

	rootIdx, err := layout.ImageIndexFromPath(layoutDir)
	if err != nil {
		t.Fatalf("open OCI layout %s: %v", layoutDir, err)
	}
	rootManifest, err := rootIdx.IndexManifest()
	if err != nil {
		t.Fatalf("read %s root index manifest: %v", img.name, err)
	}
	// multiplatform_image (tools/bazel/container_image.bzl) documents this
	// as a deliberate nested index: the outer index has exactly one entry
	// pointing at an inner index that actually lists the per-platform
	// manifests.
	if len(rootManifest.Manifests) != 1 {
		t.Fatalf("expected exactly 1 top-level manifest (the nested platform index) for %s, found %d",
			img.name, len(rootManifest.Manifests))
	}

	innerIdx, err := rootIdx.ImageIndex(rootManifest.Manifests[0].Digest)
	if err != nil {
		t.Fatalf("descend into %s's nested platform index: %v", img.name, err)
	}
	innerManifest, err := innerIdx.IndexManifest()
	if err != nil {
		t.Fatalf("read %s's nested index manifest: %v", img.name, err)
	}

	var target *v1.Hash
	for _, m := range innerManifest.Manifests {
		if m.Platform != nil && m.Platform.OS == plat.os && m.Platform.Architecture == plat.arch {
			d := m.Digest
			target = &d
			break
		}
	}
	if target == nil {
		t.Fatalf("no %s manifest found in %s's image index", plat, img.name)
	}

	platImage, err := innerIdx.Image(*target)
	if err != nil {
		t.Fatalf("load %s's %s image: %v", img.name, plat, err)
	}
	layers, err := platImage.Layers()
	if err != nil {
		t.Fatalf("read %s's %s layers: %v", img.name, plat, err)
	}

	want := strings.TrimPrefix(path.Clean(img.entrypointPath), "/")
	// The binary's own layer is added last by container_image.bzl, so
	// search from the top down.
	for i := len(layers) - 1; i >= 0; i-- {
		data, err := findInLayer(layers[i], want)
		if err != nil {
			t.Fatalf("scan layer %d of %s's %s image: %v", i, img.name, plat, err)
		}
		if data == nil {
			continue
		}
		out, err := os.CreateTemp(t.TempDir(), img.name+"-"+plat.arch+"-*")
		if err != nil {
			t.Fatalf("create temp file for %s: %v", img.name, err)
		}
		if _, err := out.Write(data); err != nil {
			t.Fatalf("write extracted %s binary: %v", img.name, err)
		}
		if err := out.Close(); err != nil {
			t.Fatalf("close extracted %s binary: %v", img.name, err)
		}
		if err := os.Chmod(out.Name(), 0o755); err != nil {
			t.Fatalf("chmod extracted %s binary: %v", img.name, err)
		}
		return out.Name()
	}
	t.Fatalf("%s not found in any layer of %s's %s image", img.entrypointPath, img.name, plat)
	return ""
}

// findInLayer returns the contents of wantPath (a slash-separated path
// with no leading slash) inside layer's tar stream, or nil if it is not
// present in this layer.
func findInLayer(layer v1.Layer, wantPath string) ([]byte, error) {
	rc, err := layer.Uncompressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(path.Clean("/"+hdr.Name), "/")
		if name != wantPath {
			continue
		}
		return io.ReadAll(tr)
	}
}

// runMigrate execs the extracted migrate binary's `up` subcommand against
// connString, failing the test with its combined output on any error.
func runMigrate(t *testing.T, bin, connString string) {
	t.Helper()
	cmd := exec.Command(bin, "up")
	cmd.Env = append(os.Environ(), "PG_DATABASE_URL="+connString)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s up failed: %v\n%s", bin, err, out)
	}
}

func migrationHistoryCount(ctx context.Context, t *testing.T, pg *dbtest.Postgres) int {
	t.Helper()
	var n int
	if err := pg.Pool.QueryRow(ctx, "SELECT count(*) FROM migration_history").Scan(&n); err != nil {
		t.Fatalf("count migration_history: %v", err)
	}
	return n
}

// startProcess starts bin with env, capturing its combined output into the
// returned buffer, and registers a cleanup that kills it when the test
// ends.
func startProcess(t *testing.T, bin string, env []string) *bytes.Buffer {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return &buf
}

// freePort reserves and immediately releases a loopback TCP port for a
// subprocess to bind to.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitForHTTP polls url until it returns a response or timeout elapses --
// QEMU user-mode emulation for arm64 can take longer than a native process
// to come up.
func waitForHTTP(t *testing.T, url string, timeout time.Duration) *http.Response {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %v", url, lastErr)
	return nil
}

func assertAPIHealthz(t *testing.T, bin, connString string) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	env := append(os.Environ(), "PG_DATABASE_URL="+connString, "KRILL_API_ADDR="+addr)
	out := startProcess(t, bin, env)

	resp := waitForHTTP(t, "http://"+addr+"/healthz", 30*time.Second)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api /healthz = %d: %s\nprocess output:\n%s", resp.StatusCode, body, out)
	}
	if !bytes.Contains(body, []byte(`"status":"ok"`)) {
		t.Fatalf("api /healthz unexpected body: %s\nprocess output:\n%s", body, out)
	}
}

func assertMCPHandshake(t *testing.T, bin, connString string) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	env := append(os.Environ(), "PG_DATABASE_URL="+connString, "KRILL_MCP_ADDR="+addr)
	out := startProcess(t, bin, env)

	healthResp := waitForHTTP(t, "http://"+addr+"/healthz", 30*time.Second)
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(healthResp.Body)
		t.Fatalf("mcp /healthz = %d: %s\nprocess output:\n%s", healthResp.StatusCode, b, out)
	}

	// The MCP handshake surface itself: POST /mcp/spec with an
	// `initialize` request. With no mcpauth credential configured, the
	// mcpauth front door correctly rejects it -- proving the MCP
	// transport, its routing, and its auth gate are all alive under this
	// platform's emulation. A crash, hang, or wrong-architecture exec
	// would show up here as a connection failure or 5xx, not a clean 401
	// -- exactly the failure mode docs/DOCKER.md warns is silent at build
	// time.
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"image-integration-smoke","version":"0"}}}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/mcp/spec", body)
	if err != nil {
		t.Fatalf("build initialize request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp/spec: %v\nprocess output:\n%s", err, out)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 (no mcpauth credential configured) from the MCP handshake, got %d: %s\nprocess output:\n%s",
			resp.StatusCode, respBody, out)
	}
}
