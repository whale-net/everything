package writeback

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
)

// TestSignAppJWT_VerifiesWithPublicKey is a table test over the RS256 JWT
// signAppJWT hand-rolls (issue #798: stdlib crypto only, no JWT library).
// Verifies both the claim values and that the signature actually verifies
// against the signing key's public half -- and only that key.
func TestSignAppJWT_VerifiesWithPublicKey(t *testing.T) {
	cases := []struct {
		name  string
		appID string
	}{
		{name: "numeric app id", appID: "123456"},
		{name: "different app id", appID: "9999999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

			tok, err := signAppJWT(tc.appID, key, now)
			require.NoError(t, err)

			parts := strings.Split(tok, ".")
			require.Lenf(t, parts, 3, "expected header.claims.signature, got %q", tok)

			headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
			require.NoError(t, err)
			var header map[string]string
			require.NoError(t, json.Unmarshal(headerJSON, &header))
			require.Equal(t, "RS256", header["alg"])
			require.Equal(t, "JWT", header["typ"])

			claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
			require.NoError(t, err)
			var claims map[string]any
			require.NoError(t, json.Unmarshal(claimsJSON, &claims))
			require.Equal(t, tc.appID, claims["iss"])
			require.Equal(t, float64(now.Add(-60*time.Second).Unix()), claims["iat"])
			require.Equal(t, float64(now.Add(9*time.Minute).Unix()), claims["exp"])

			sig, err := base64.RawURLEncoding.DecodeString(parts[2])
			require.NoError(t, err)
			hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			require.NoError(t, rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], sig),
				"signature must verify against the signing key's own public key")

			otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			require.Error(t, rsa.VerifyPKCS1v15(&otherKey.PublicKey, crypto.SHA256, hash[:], sig),
				"signature must NOT verify against an unrelated key")
		})
	}
}

// TestMintInstallationToken_TokenExchange exercises the GitHub App
// installation-token exchange (issue #798 point 3) against an
// httptest.Server standing in for api.github.com -- no network access.
func TestMintInstallationToken_TokenExchange(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	var gotAuth, gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test123"}`))
	}))
	defer server.Close()

	a, err := NewGitOpsActivities(nil, nil, GitOpsConfig{
		Repo:           "whale-net/argok8s",
		AppID:          "123456",
		InstallationID: "67890",
		PrivateKeyPEM:  encodePKCS1PEMForTest(t, key),
	})
	require.NoError(t, err)
	a.GitHubAPIBaseURL = server.URL
	a.HTTPClient = server.Client()

	token, err := a.mintInstallationToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, "ghs_test123", token)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/app/installations/67890/access_tokens", gotPath)
	require.True(t, strings.HasPrefix(gotAuth, "Bearer "), "Authorization header must be a Bearer JWT, got %q", gotAuth)
}

// TestMintInstallationToken_ErrorStatus proves a non-2xx response from the
// token endpoint is surfaced as an error, not an empty/zero token.
func TestMintInstallationToken_ErrorStatus(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	a, err := NewGitOpsActivities(nil, nil, GitOpsConfig{
		Repo:           "whale-net/argok8s",
		AppID:          "1",
		InstallationID: "1",
		PrivateKeyPEM:  encodePKCS1PEMForTest(t, key),
	})
	require.NoError(t, err)
	a.GitHubAPIBaseURL = server.URL
	a.HTTPClient = server.Client()

	_, err = a.mintInstallationToken(context.Background())
	require.Error(t, err)
}

// TestNewGitOpsActivities_RequiresEveryEnvVar proves construction fails
// fast (never falls back to a baked-in value) when any required field is
// missing -- issue #798's "do not hardcode" requirement.
func TestNewGitOpsActivities_RequiresEveryEnvVar(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemStr := encodePKCS1PEMForTest(t, key)

	full := GitOpsConfig{
		Repo:           "whale-net/argok8s",
		AppID:          "1",
		InstallationID: "1",
		PrivateKeyPEM:  pemStr,
	}

	if _, err := NewGitOpsActivities(nil, nil, full); err != nil {
		t.Fatalf("expected a fully-populated config to succeed, got %v", err)
	}

	cases := []struct {
		name   string
		mutate func(c GitOpsConfig) GitOpsConfig
	}{
		{"missing repo", func(c GitOpsConfig) GitOpsConfig { c.Repo = ""; return c }},
		{"missing app id", func(c GitOpsConfig) GitOpsConfig { c.AppID = ""; return c }},
		{"missing installation id", func(c GitOpsConfig) GitOpsConfig { c.InstallationID = ""; return c }},
		{"missing private key", func(c GitOpsConfig) GitOpsConfig { c.PrivateKeyPEM = ""; return c }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewGitOpsActivities(nil, nil, tc.mutate(full))
			require.Error(t, err)
		})
	}
}

// TestGitOpsActivities_Publish_FirstWriteThenNoOp exercises Publish end to
// end against a local `git init --bare` repo (no network): the first
// publish writes and commits/pushes `<domain>/<chart-name>/versions/<env>.yaml`; a
// second publish of byte-identical content is a no-op (Skipped=true),
// checked against the git-committed file itself, not a sidecar hash file
// (stub.go's mechanism) -- see Publish's doc comment.
func TestGitOpsActivities_Publish_FirstWriteThenNoOp(t *testing.T) {
	requireGit(t)
	bareDir := t.TempDir()
	runTestGit(t, "", "init", "--bare", "--initial-branch=main", bareDir)

	a := newTestGitOpsActivities(t, bareDir)
	state := RenderedState{
		EnvironmentKey: "dev",
		Domain:         "app-registry",
		ChartName:      "app-registry-app-registry",
		Document:       []byte("targetRevision: v0.0.1\n"),
	}

	first, err := a.Publish(context.Background(), state)
	require.NoError(t, err)
	require.False(t, first.Skipped)
	require.Equal(t, filepath.Join("app-registry", "app-registry-app-registry", "versions", "dev.yaml"), first.Location)

	second, err := a.Publish(context.Background(), state)
	require.NoError(t, err)
	require.True(t, second.Skipped, "identical content republished must be a no-op")

	checkDir := t.TempDir()
	runTestGit(t, "", "clone", bareDir, checkDir)
	got, err := os.ReadFile(filepath.Join(checkDir, "app-registry", "app-registry-app-registry", "versions", "dev.yaml"))
	require.NoError(t, err)
	require.Equal(t, state.Document, got)
}

// TestGitOpsActivities_Publish_ConflictRetry simulates a concurrent writer
// pushing to the same branch between this Publish's clone and its first
// push attempt -- the first push is rejected (non-fast-forward), and
// Publish must fetch, re-check no-op against the fresh remote content, and
// retry exactly once, succeeding without losing the concurrent writer's
// commit.
func TestGitOpsActivities_Publish_ConflictRetry(t *testing.T) {
	requireGit(t)
	bareDir := t.TempDir()
	runTestGit(t, "", "init", "--bare", "--initial-branch=main", bareDir)

	// Seed the bare repo so origin/main exists before either clone below --
	// mirrors a real, already-populated gitops repo.
	seedDir := t.TempDir()
	runTestGit(t, "", "clone", bareDir, seedDir)
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("seed\n"), 0o644))
	runTestGit(t, seedDir, "add", "README.md")
	runTestGit(t, seedDir, "-c", "user.name=seed", "-c", "user.email=seed@example.com", "commit", "-m", "seed")
	runTestGit(t, seedDir, "push", "origin", "HEAD:main")

	a := newTestGitOpsActivities(t, bareDir)

	// This clone becomes "stale" the moment the concurrent writer below
	// pushes -- cloneRepo is called directly (rather than through Publish)
	// so the test can push a competing commit in between.
	staleDir := t.TempDir()
	require.NoError(t, cloneRepo(context.Background(), bareDir, "main", staleDir, ""))

	otherDir := t.TempDir()
	runTestGit(t, "", "clone", bareDir, otherDir)
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "other.txt"), []byte("other\n"), 0o644))
	runTestGit(t, otherDir, "add", "other.txt")
	runTestGit(t, otherDir, "-c", "user.name=other", "-c", "user.email=other@example.com", "commit", "-m", "concurrent write")
	runTestGit(t, otherDir, "push", "origin", "HEAD:main")

	doc := []byte("targetRevision: v0.0.2\n")
	relPath := filepath.Join("app-registry", "app-registry-app-registry", "versions", "dev.yaml")
	result, err := a.publishToClone(context.Background(), staleDir, "main", relPath, doc, "")
	require.NoError(t, err)
	require.False(t, result.Skipped)

	checkDir := t.TempDir()
	runTestGit(t, "", "clone", bareDir, checkDir)
	_, err = os.Stat(filepath.Join(checkDir, "other.txt"))
	require.NoError(t, err, "the concurrent writer's commit must survive the retry")
	got, err := os.ReadFile(filepath.Join(checkDir, relPath))
	require.NoError(t, err)
	require.Equal(t, doc, got)
}

type fakePromotionClient struct {
	pb.PromotionRegistryClient
	getEnvState func(ctx context.Context, in *pb.GetEnvironmentStateRequest) (*pb.GetEnvironmentStateResponse, error)
}

func (f *fakePromotionClient) GetEnvironmentState(ctx context.Context, in *pb.GetEnvironmentStateRequest, opts ...grpc.CallOption) (*pb.GetEnvironmentStateResponse, error) {
	if f.getEnvState != nil {
		return f.getEnvState(ctx, in)
	}
	return nil, errors.New("unimplemented")
}

type fakeAppClient struct {
	pb.AppRegistryClient
	listCharts func(ctx context.Context, in *pb.ListChartsRequest) (*pb.ListChartsResponse, error)
}

func (f *fakeAppClient) ListCharts(ctx context.Context, in *pb.ListChartsRequest, opts ...grpc.CallOption) (*pb.ListChartsResponse, error) {
	if f.listCharts != nil {
		return f.listCharts(ctx, in)
	}
	return nil, errors.New("unimplemented")
}

func TestGitOpsActivities_RenderEnvironmentState(t *testing.T) {
	fakeClient := &fakePromotionClient{
		getEnvState: func(ctx context.Context, in *pb.GetEnvironmentStateRequest) (*pb.GetEnvironmentStateResponse, error) {
			require.Equal(t, "dev", in.EnvironmentKey)
			require.Equal(t, "app-registry", in.Domain)
			return &pb.GetEnvironmentStateResponse{
				StateHash: "hash-123",
				Entries: []*pb.EnvironmentStateEntry{
					{
						Artifact: &pb.Artifact{
							Kind:    pb.ArtifactKind_ARTIFACT_KIND_CHART,
							ChartId: "chart-123",
							Version: "v0.0.39",
						},
					},
				},
			}, nil
		},
	}
	fakeApp := &fakeAppClient{
		listCharts: func(ctx context.Context, in *pb.ListChartsRequest) (*pb.ListChartsResponse, error) {
			require.Equal(t, "app-registry", in.Domain)
			return &pb.ListChartsResponse{
				Charts: []*pb.Chart{
					{
						ChartId:  "chart-123",
						Domain:   "app-registry",
						Name:     "app-registry",
						FullName: "app-registry-app-registry",
					},
				},
			}, nil
		},
	}

	a := &GitOpsActivities{Client: fakeClient, AppClient: fakeApp}
	rendered, err := a.RenderEnvironmentState(context.Background(), WritebackInput{
		PromotionID:    "promo-1",
		EnvironmentKey: "dev",
		Domain:         "app-registry",
	})
	require.NoError(t, err)
	require.Equal(t, "dev", rendered.EnvironmentKey)
	require.Equal(t, "app-registry", rendered.Domain)
	require.Equal(t, "app-registry-app-registry", rendered.ChartName)
	require.Equal(t, "hash-123", rendered.StateHash)
	require.Equal(t, "targetRevision: v0.0.39\n", string(rendered.Document))
}

func TestGitOpsActivities_RenderEnvironmentState_MissingChartError(t *testing.T) {
	fakeClient := &fakePromotionClient{
		getEnvState: func(ctx context.Context, in *pb.GetEnvironmentStateRequest) (*pb.GetEnvironmentStateResponse, error) {
			return &pb.GetEnvironmentStateResponse{
				StateHash: "hash-123",
				Entries:   []*pb.EnvironmentStateEntry{},
			}, nil
		},
	}

	a := &GitOpsActivities{Client: fakeClient, AppClient: &fakeAppClient{}}
	_, err := a.RenderEnvironmentState(context.Background(), WritebackInput{
		PromotionID:    "promo-1",
		EnvironmentKey: "dev",
		Domain:         "app-registry",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no chart artifact found")
}

// --- test helpers ---

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
}

func encodePKCS1PEMForTest(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

// newTestGitOpsActivities builds a GitOpsActivities whose token exchange
// hits a local httptest.Server (so Publish's mintInstallationToken step
// never touches the network) and whose Repo is a local bare repo path (so
// remoteURL uses it as-is -- see that method's doc comment).
func newTestGitOpsActivities(t *testing.T, bareRepoDir string) *GitOpsActivities {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"test-token"}`))
	}))
	t.Cleanup(server.Close)

	a, err := NewGitOpsActivities(nil, nil, GitOpsConfig{
		Repo:           bareRepoDir,
		Branch:         "main",
		AppID:          "1",
		InstallationID: "1",
		PrivateKeyPEM:  encodePKCS1PEMForTest(t, key),
	})
	require.NoError(t, err)
	a.GitHubAPIBaseURL = server.URL
	a.HTTPClient = server.Client()
	return a
}
