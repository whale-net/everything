// Package pushconfig_test exercises leaflab/scripts/push-config.sh end to
// end (FR81 contract half): the migration onto a device-flow bearer
// credential and the published FileDescriptorSet artifact instead of
// unauthenticated calls against server reflection.
//
// This needs real external tooling on PATH (grpcurl, jq -- push-config.sh's
// own documented dependencies) and opens a real TCP listener, so it is not
// hermetic in the way a plain go_test is expected to be. Tagged "manual"
// and "external", same idiom as
// //leaflab/api:response_contract_integration_test (see that target's
// BUILD.bazel comment) -- run explicitly:
//
//	bazel test //leaflab/scripts/pushconfig_test:pushconfig_test --test_output=all
package pushconfig_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// rlocation resolves a runfile path relative to this repo's main module
// ("_main" under Bzlmod), failing the test with a clear message if the
// corresponding `data` dependency is missing from BUILD.bazel.
func rlocation(t *testing.T, repoRelPath string) string {
	t.Helper()
	rf, err := runfiles.New()
	if err != nil {
		t.Fatalf("runfiles.New: %v", err)
	}
	path, err := rf.Rlocation("_main/" + repoRelPath)
	if err != nil {
		t.Fatalf("Rlocation(%s): %v (check the `data` attribute on this go_test)", repoRelPath, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("runfile %s resolved to %s, but it does not exist: %v", repoRelPath, path, statErr)
	}
	return path
}

// requireOnPath skips the test (rather than failing it) when a dependency
// push-config.sh itself documents as required -- grpcurl, jq -- is not
// installed. Bazel's build sandbox does not vendor either; they are host
// tooling, exactly like the Docker dependency
// response_contract_integration_test documents.
func requireOnPath(t *testing.T, cmd string) {
	t.Helper()
	if _, err := exec.LookPath(cmd); err != nil {
		t.Skipf("%s not found on PATH -- install it to run this test (push-config.sh's own documented dependency)", cmd)
	}
}

// TestPushConfig_NoCredential_ActionableExitOne asserts FR81's Testing
// criterion "push-config.sh with no credential produces the actionable
// message and a non-zero exit": with a working OIDC discovery endpoint but
// no cached device-flow token, the real authtoken binary fails
// non-interactively (it never launches the interactive prompt on its own),
// and push-config.sh must turn that into a plain, actionable
// "run authtoken login" hint and exit(1) -- not hang, and not a bare stack
// trace.
func TestPushConfig_NoCredential_ActionableExitOne(t *testing.T) {
	requireOnPath(t, "grpcurl")
	requireOnPath(t, "jq")

	scriptPath := rlocation(t, "leaflab/scripts/push-config.sh")
	rlocation(t, "leaflab/scripts/scenarios/single-light.json") // scenario fixture must be staged alongside the script
	authtokenBin := rlocation(t, "leaflab/scripts/authtoken/authtoken_/authtoken")

	// A minimal OIDC discovery double: enough for newDeviceFlowCreds'
	// unconditional discovery step to succeed. The device_authorization and
	// token endpoints are deliberately never hit -- there is no cached
	// credential, and DeviceFlowAccessToken(interactive=false) must fail
	// before ever reaching them.
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"device_authorization_endpoint": "http://unused.invalid/device_authorization",
			"token_endpoint": "http://unused.invalid/token"
		}`))
	}))
	defer oidc.Close()

	tmpCache := filepath.Join(t.TempDir(), "no-such-cached-token.json")

	cmd := exec.Command("bash", scriptPath, "leaflab-test-device", "single-light")
	cmd.Env = append(os.Environ(),
		"LEAFLAB_AUTHTOKEN_BIN="+authtokenBin,
		"LEAFLAB_API_OIDC_ISSUER="+oidc.URL,
		"LEAFLAB_DEVICE_FLOW_CLIENT_ID=test-client",
		"LEAFLAB_DEVICE_FLOW_TOKEN_CACHE="+tmpCache,
	)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected push-config.sh to exit non-zero with no cached credential; output:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !isExitError(err, &exitErr) {
		t.Fatalf("expected an *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1; output:\n%s", exitErr.ExitCode(), out)
	}

	const hint = "bazel run //leaflab/scripts/authtoken:authtoken -- login"
	if !strings.Contains(string(out), hint) {
		t.Errorf("output does not contain the actionable authtoken-login hint %q; output:\n%s", hint, out)
	}
	if !strings.Contains(string(out), "Run this once to authenticate interactively") {
		t.Errorf("output does not contain the actionable-message lead-in; output:\n%s", out)
	}
}

func isExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// fakeLeafLabAPI implements pb.LeafLabAPIServer with a single working RPC
// (PushDeviceConfig) so push-config.sh has something to call. Everything
// else is the zero-value pb.UnimplementedLeafLabAPIServer behavior
// (Unimplemented), which is fine -- push-config.sh only ever calls
// PushDeviceConfig.
type fakeLeafLabAPI struct {
	pb.UnimplementedLeafLabAPIServer
}

func (f *fakeLeafLabAPI) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if req.GetDeviceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "device_id is required")
	}
	return &pb.PushDeviceConfigResponse{Version: 7}, nil
}

const wantBearerToken = "fake-device-flow-access-token-for-pushconfig-test"

// requireBearerToken is a bare unary interceptor enforcing the same shape
// of check leaflab/api's real auth interceptor does (see auth.go): a
// missing or wrong "authorization: Bearer <token>" is Unauthenticated. It
// exists so this test proves push-config.sh actually attaches the
// credential faketoken prints -- not merely that an unauthenticated call
// happens to succeed.
func requireBearerToken(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) != 1 || vals[0] != "Bearer "+wantBearerToken {
		return nil, status.Error(codes.Unauthenticated, "missing or invalid bearer token")
	}
	return handler(ctx, req)
}

// TestPushConfig_AuthenticatedServer_ReflectionOff_Succeeds asserts FR81's
// Testing criterion "push-config.sh runs green against an authenticated
// server with reflection off": a real TCP gRPC server -- reflection
// deliberately never registered, enforcing a bearer token -- must be
// reachable via push-config.sh's grpcurl -protoset call against the
// published descriptor set artifact, with the credential coming from
// LEAFLAB_AUTHTOKEN_BIN (faketoken, standing in for a real device-flow
// login). This is Phase 1 exit criterion 4 end to end.
func TestPushConfig_AuthenticatedServer_ReflectionOff_Succeeds(t *testing.T) {
	requireOnPath(t, "grpcurl")
	requireOnPath(t, "jq")

	scriptPath := rlocation(t, "leaflab/scripts/push-config.sh")
	rlocation(t, "leaflab/scripts/scenarios/single-light.json")
	faketokenBin := rlocation(t, "leaflab/scripts/pushconfig_test/faketoken/faketoken_/faketoken")
	descriptorSet := rlocation(t, "leaflab/api/proto/leaflabapi_descriptor_set.pb")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	// Deliberately no reflection.Register(grpcServer) call anywhere in this
	// test -- the whole point is that push-config.sh must work without it
	// (FR11.1).
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(requireBearerToken))
	pb.RegisterLeafLabAPIServer(grpcServer, &fakeLeafLabAPI{})
	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.GracefulStop)

	cmd := exec.Command("bash", scriptPath, "leaflab-test-device", "single-light")
	cmd.Env = append(os.Environ(),
		"LEAFLAB_AUTHTOKEN_BIN="+faketokenBin,
		"FAKETOKEN_VALUE="+wantBearerToken,
		"LEAFLAB_DESCRIPTOR_SET="+descriptorSet,
		"LEAFLAB_API_HOST="+lis.Addr().String(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("push-config.sh failed: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(string(out), "Pushed — assigned version 7") {
		t.Errorf("output does not contain the expected success line; output:\n%s", out)
	}
}
