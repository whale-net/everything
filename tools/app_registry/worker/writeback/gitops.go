// gitops.go implements Writeback for real: RenderEnvironmentState renders a
// domain-scoped `targetRevision: <version>` YAML document (whale-net/argok8s#68),
// and Publish commits it to the gitops repo (whale-net/argok8s) over HTTPS
// using a GitHub App installation token.
//
// See the package doc comment in workflow.go ("Swapping the stub for a real
// implementation") for how this fits alongside StubActivities, which stays
// the no-config dev/test fallback -- see ../main.go for the selection
// logic (GitOpsActivities is used only when WRITEBACK_GITOPS_REPO is set).
package writeback

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"gopkg.in/yaml.v3"
)

// GitOpsConfig is GitOpsActivities' configuration, sourced from env vars by
// ../main.go and documented (var names, defaults) in worker/ENV.md. Every
// field here that has no default is REQUIRED with no baked-in fallback --
// see NewGitOpsActivities, which fails fast at construction if any is
// empty. In particular AppID/InstallationID/Repo/PrivateKeyPEM must never
// be hardcoded anywhere in this repo (they are deployment-specific,
// argok8s-owned values) -- see issue #798.
type GitOpsConfig struct {
	// Repo is the gitops repo to write to -- normally "whale-net/argok8s"
	// (WRITEBACK_GITOPS_REPO). Also accepts a full git remote URL or a
	// local filesystem path (a `git init --bare` directory), which is what
	// lets tests exercise Publish's real clone/commit/push logic against a
	// local repo with no network access -- see remoteURL.
	Repo string
	// Branch is the target branch to clone/push. Defaults to "main"
	// (WRITEBACK_GITOPS_BRANCH).
	Branch string
	// AppID is the GitHub App's numeric id, used as the JWT "iss" claim
	// (WRITEBACK_GITHUB_APP_ID). Required, no default.
	AppID string
	// InstallationID is the GitHub App installation id the access-token
	// request is scoped to (WRITEBACK_GITHUB_APP_INSTALLATION_ID).
	// Required, no default.
	InstallationID string
	// PrivateKeyPEM is the GitHub App's RSA private key, PEM-encoded,
	// PKCS#1 or PKCS#8 (WRITEBACK_GITHUB_APP_PRIVATE_KEY). Required, no
	// default. Never logged; see mintInstallationToken and runGit's
	// redaction of the derived installation token.
	PrivateKeyPEM string
	// AuthorName/AuthorEmail are the git commit author identity. Default to
	// "app-registry-writeback[bot]" and
	// "app-registry-writeback[bot]@users.noreply.github.com"
	// (WRITEBACK_GIT_AUTHOR_NAME / WRITEBACK_GIT_AUTHOR_EMAIL).
	AuthorName  string
	AuthorEmail string
}

// pushMechanism is the seam between Publish's render/diff/commit logic and
// how a committed branch actually reaches the remote. directPush (below) is
// the only implementation today; argok8s#55 point 5 (direct push vs.
// PR-based writes) is still undecided upstream per issue #798, so this
// interface exists to let a PR-based implementation swap in later without
// touching anything above it.
type pushMechanism interface {
	push(ctx context.Context, repoDir, branch, token string) error
}

// directPush pushes the current branch tip straight to origin/branch.
type directPush struct{}

func (directPush) push(ctx context.Context, repoDir, branch, token string) error {
	return runGit(ctx, repoDir, token, "push", "origin", "HEAD:"+branch)
}

// GitOpsActivities is the real Writeback implementation. See the package
// doc comment above and ../ARCHITECTURE.md "Writeback: outbox -> Temporal".
type GitOpsActivities struct {
	// Client reads current promotion state, same role as
	// StubActivities.Client.
	Client pb.PromotionRegistryClient
	Config GitOpsConfig

	// HTTPClient issues the installation-token exchange request. Defaults
	// to http.DefaultClient; overridable in tests to point at an
	// httptest.Server.
	HTTPClient *http.Client
	// GitHubAPIBaseURL defaults to "https://api.github.com"; overridable in
	// tests for the same reason as HTTPClient.
	GitHubAPIBaseURL string

	privateKey *rsa.PrivateKey
	push       pushMechanism
}

// NewGitOpsActivities constructs a GitOpsActivities, validating every
// required field of cfg and parsing its private key once. Returns an error
// (never silently falls back to a baked-in value) if any required field is
// empty or the key doesn't parse -- see GitOpsConfig's doc comment and
// issue #798's "do not hardcode" requirement.
func NewGitOpsActivities(client pb.PromotionRegistryClient, cfg GitOpsConfig) (*GitOpsActivities, error) {
	var missing []string
	if cfg.Repo == "" {
		missing = append(missing, "WRITEBACK_GITOPS_REPO")
	}
	if cfg.AppID == "" {
		missing = append(missing, "WRITEBACK_GITHUB_APP_ID")
	}
	if cfg.InstallationID == "" {
		missing = append(missing, "WRITEBACK_GITHUB_APP_INSTALLATION_ID")
	}
	if cfg.PrivateKeyPEM == "" {
		missing = append(missing, "WRITEBACK_GITHUB_APP_PRIVATE_KEY")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("gitops writeback activities: missing required env var(s): %s", strings.Join(missing, ", "))
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.AuthorName == "" {
		cfg.AuthorName = "app-registry-writeback[bot]"
	}
	if cfg.AuthorEmail == "" {
		cfg.AuthorEmail = "app-registry-writeback[bot]@users.noreply.github.com"
	}
	key, err := parsePrivateKeyPEM(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("gitops writeback activities: %w", err)
	}
	return &GitOpsActivities{
		Client:           client,
		Config:           cfg,
		HTTPClient:       http.DefaultClient,
		GitHubAPIBaseURL: "https://api.github.com",
		privateKey:       key,
		push:             directPush{},
	}, nil
}

// RenderEnvironmentState implements Writeback. Renders top-level
// `targetRevision: <version>` per argok8s#68 -- matching the chart version
// promoted to in.EnvironmentKey in in.Domain.
func (a *GitOpsActivities) RenderEnvironmentState(ctx context.Context, in WritebackInput) (RenderedState, error) {
	resp, err := a.Client.GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{
		EnvironmentKey: in.EnvironmentKey,
		Domain:         in.Domain,
	})
	if err != nil {
		return RenderedState{}, fmt.Errorf("render environment state for %s/%s (promotion %s): %w", in.Domain, in.EnvironmentKey, in.PromotionID, err)
	}

	var targetRevision string
	for _, entry := range resp.Entries {
		if entry.Artifact != nil && entry.Artifact.Kind == pb.ArtifactKind_ARTIFACT_KIND_CHART {
			targetRevision = entry.Artifact.Version
			break
		}
	}
	if targetRevision == "" {
		return RenderedState{}, fmt.Errorf("render environment state for %s/%s: no chart artifact found in environment state", in.Domain, in.EnvironmentKey)
	}

	doc, err := renderTargetRevisionDocument(targetRevision)
	if err != nil {
		return RenderedState{}, fmt.Errorf("render environment state for %s/%s: marshal: %w", in.Domain, in.EnvironmentKey, err)
	}

	return RenderedState{
		EnvironmentKey: in.EnvironmentKey,
		Domain:         in.Domain,
		StateHash:      resp.StateHash,
		RenderedAt:     time.Now().UTC(),
		Document:       doc,
	}, nil
}

// renderTargetRevisionDocument builds the `targetRevision: <version>` YAML
// document for the domain's versions/<env>.yaml file in argok8s (see
// argok8s#68).
func renderTargetRevisionDocument(targetRevision string) ([]byte, error) {
	root := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "targetRevision"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: targetRevision},
		},
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Publish implements Writeback: mint a GitHub App installation token,
// clone the gitops repo, and commit+push the rendered document to
// `<domain>/versions/<environment>.yaml` if it differs from what's already
// there -- see argok8s#68 for the path convention and
// ../../architecture/12-writeback-outbox-temporal.md for why
// Environment.gitops_path is not used.
func (a *GitOpsActivities) Publish(ctx context.Context, state RenderedState) (PublishResult, error) {
	if state.Domain == "" {
		return PublishResult{}, fmt.Errorf("publish %s: RenderedState has no domain -- GitOpsActivities requires domain-scoped rendering (see WritebackInput.Domain)", state.EnvironmentKey)
	}

	token, err := a.mintInstallationToken(ctx)
	if err != nil {
		return PublishResult{}, fmt.Errorf("publish %s/%s: %w", state.Domain, state.EnvironmentKey, err)
	}

	dir, err := os.MkdirTemp("", "app-registry-writeback-")
	if err != nil {
		return PublishResult{}, fmt.Errorf("publish %s/%s: create temp clone dir: %w", state.Domain, state.EnvironmentKey, err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck

	branch := a.Config.Branch
	remote := a.remoteURL(token)
	if err := cloneRepo(ctx, remote, branch, dir, token); err != nil {
		return PublishResult{}, fmt.Errorf("publish %s/%s: %w", state.Domain, state.EnvironmentKey, err)
	}

	relPath := filepath.Join(state.Domain, "versions", state.EnvironmentKey+".yaml")
	result, err := a.publishToClone(ctx, dir, branch, relPath, state.Document, token)
	if err != nil {
		return PublishResult{}, fmt.Errorf("publish %s/%s: %w", state.Domain, state.EnvironmentKey, err)
	}
	return result, nil
}

// publishToClone applies the no-op check, write/commit/push, and one
// push-conflict retry against an already-cloned repoDir checked out to
// branch -- see Publish's doc comment. On a push rejection (almost always
// non-fast-forward, i.e. someone else published to the same file since
// this clone was made), it re-fetches, re-checks no-op against the fresh
// remote content (another writer may have already written exactly this
// document), and re-applies the write/commit/push exactly once more. A
// second failure is returned as an error -- WritebackWorkflow's own
// activity RetryPolicy (5 attempts, see workflow.go) covers further
// retries from a fresh clone.
func (a *GitOpsActivities) publishToClone(ctx context.Context, repoDir, branch, relPath string, doc []byte, token string) (PublishResult, error) {
	if skip, err := isNoOp(repoDir, relPath, doc); err != nil {
		return PublishResult{}, err
	} else if skip {
		return PublishResult{Location: relPath, Skipped: true}, nil
	}
	if err := writeAndCommit(ctx, repoDir, relPath, doc, a.Config.AuthorName, a.Config.AuthorEmail, token); err != nil {
		return PublishResult{}, err
	}
	if err := a.push.push(ctx, repoDir, branch, token); err == nil {
		return PublishResult{Location: relPath}, nil
	}

	// Push was rejected -- re-fetch the branch's current tip and re-check
	// no-op against what's actually there now, per this method's doc
	// comment.
	if err := runGit(ctx, repoDir, token, "fetch", "origin", branch); err != nil {
		return PublishResult{}, fmt.Errorf("push conflict retry: fetch: %w", err)
	}
	if err := runGit(ctx, repoDir, token, "checkout", "-B", branch, "origin/"+branch); err != nil {
		return PublishResult{}, fmt.Errorf("push conflict retry: checkout origin/%s: %w", branch, err)
	}
	if skip, err := isNoOp(repoDir, relPath, doc); err != nil {
		return PublishResult{}, err
	} else if skip {
		return PublishResult{Location: relPath, Skipped: true}, nil
	}
	if err := writeAndCommit(ctx, repoDir, relPath, doc, a.Config.AuthorName, a.Config.AuthorEmail, token); err != nil {
		return PublishResult{}, err
	}
	if err := a.push.push(ctx, repoDir, branch, token); err != nil {
		return PublishResult{}, fmt.Errorf("push conflict retry: push: %w", err)
	}
	return PublishResult{Location: relPath}, nil
}

// isNoOp reports whether relPath already contains exactly doc inside
// repoDir -- the no-op detection contract Writeback.Publish's doc comment
// commits every implementation to, applied here against the git-committed
// file itself rather than a local sidecar hash file (stub.go's mechanism).
// A missing file is not a no-op (nothing to compare against).
func isNoOp(repoDir, relPath string, doc []byte) (bool, error) {
	existing, err := os.ReadFile(filepath.Join(repoDir, relPath))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", relPath, err)
	}
	return bytes.Equal(existing, doc), nil
}

// writeAndCommit writes doc to relPath inside repoDir and commits it.
// Callers must have already ruled out the no-op case (isNoOp) -- git commit
// with a genuinely empty diff is not expected to happen here and would
// surface as an error if it did.
func writeAndCommit(ctx context.Context, repoDir, relPath string, doc []byte, authorName, authorEmail, token string) error {
	fullPath := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(relPath), err)
	}
	if err := os.WriteFile(fullPath, doc, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	if err := runGit(ctx, repoDir, token, "add", relPath); err != nil {
		return fmt.Errorf("git add %s: %w", relPath, err)
	}
	if err := runGit(ctx, repoDir, token,
		"-c", "user.name="+authorName,
		"-c", "user.email="+authorEmail,
		"commit", "-m", "app-registry writeback: "+relPath,
	); err != nil {
		return fmt.Errorf("git commit %s: %w", relPath, err)
	}
	return nil
}

// cloneRepo shallow-clones remote into dir and checks out branch. Branch
// may not exist yet (a brand-new repo, or the first writeback for a
// domain/environment pair) -- in that case it's created fresh off whatever
// HEAD the clone left us on, rather than treated as an error, since
// argok8s's convention (issue #798) is that this file's own directory
// starts out absent until the first Publish. --no-single-branch keeps
// origin/<branch> available as a remote-tracking ref despite --depth 1, so
// the "does branch already exist" check below works off a real fetch, not
// a guess.
func cloneRepo(ctx context.Context, remote, branch, dir, token string) error {
	if err := runGit(ctx, "", token, "clone", "--depth", "1", "--no-single-branch", remote, dir); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	if err := runGit(ctx, dir, token, "rev-parse", "--verify", "origin/"+branch); err == nil {
		if err := runGit(ctx, dir, token, "checkout", "-B", branch, "origin/"+branch); err != nil {
			return fmt.Errorf("checkout %s: %w", branch, err)
		}
		return nil
	}
	if err := runGit(ctx, dir, token, "checkout", "-B", branch); err != nil {
		return fmt.Errorf("checkout -B %s (new branch): %w", branch, err)
	}
	return nil
}

// remoteURL builds the git remote to clone/push. Config.Repo is normally
// "whale-net/argok8s" (owner/repo), turned into an HTTPS URL authenticated
// with the freshly minted installation token -- "x-access-token:<token>"
// is the credential shape a GitHub App's installation token uses over
// HTTPS (issue #798 point 3). If Config.Repo is already a full remote (a
// URL, or an `owner@host:path` SSH spec) or an existing local filesystem
// path, it's used as-is, unmodified -- the local-path case is what lets
// tests exercise this against a `git init --bare` directory with no
// network access.
func (a *GitOpsActivities) remoteURL(token string) string {
	repo := a.Config.Repo
	if strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@") {
		return repo
	}
	if _, err := os.Stat(repo); err == nil {
		return repo
	}
	return "https://x-access-token:" + token + "@github.com/" + repo + ".git"
}

// runGit shells out to the git CLI (os/exec -- no go-git dependency, see
// issue #798's constraints). token, if non-empty, is redacted from both the
// command line and combined output in any returned error so an
// installation token is never logged -- see GitOpsConfig.PrivateKeyPEM's
// doc comment on the same requirement for the key itself.
func runGit(ctx context.Context, dir, token string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()

	argStr := strings.Join(args, " ")
	outStr := out.String()
	if token != "" {
		argStr = strings.ReplaceAll(argStr, token, "REDACTED")
		outStr = strings.ReplaceAll(outStr, token, "REDACTED")
	}
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", argStr, err, strings.TrimSpace(outStr))
	}
	return nil
}

// mintInstallationToken builds and signs a GitHub App JWT, then exchanges
// it for a short-lived installation access token -- the standard GitHub App
// git-auth flow (issue #798 point 3). The returned token is never logged.
func (a *GitOpsActivities) mintInstallationToken(ctx context.Context) (string, error) {
	jwtStr, err := signAppJWT(a.Config.AppID, a.privateKey, time.Now())
	if err != nil {
		return "", fmt.Errorf("mint installation token: sign app jwt: %w", err)
	}

	url := a.baseURL() + "/app/installations/" + a.Config.InstallationID + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("mint installation token: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("mint installation token: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint installation token: unexpected status %d", resp.StatusCode)
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("mint installation token: decode response: %w", err)
	}
	if body.Token == "" {
		return "", fmt.Errorf("mint installation token: response had no token")
	}
	return body.Token, nil
}

func (a *GitOpsActivities) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

func (a *GitOpsActivities) baseURL() string {
	if a.GitHubAPIBaseURL != "" {
		return a.GitHubAPIBaseURL
	}
	return "https://api.github.com"
}

// signAppJWT hand-rolls the two-claim GitHub App JWT (RS256) GitHub's
// installation-token endpoint requires: header {"alg":"RS256","typ":"JWT"},
// claims {iat, exp, iss}. A ~20-line, well-defined format -- issue #798
// explicitly calls for stdlib crypto here instead of a JWT dependency (this
// repo has none today). iat is backdated 60s to tolerate clock drift
// against GitHub's clock; exp is capped at 9 minutes, under GitHub's
// 10-minute maximum.
func signAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]int64{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	// iss is a string (the App ID, e.g. "123456"), so it can't share the
	// int64-valued claims map above without losing type-safety on the
	// numeric claims; encoded into the same JSON object by hand instead.
	claimsWithIss := map[string]any{
		"iat": claims["iat"],
		"exp": claims["exp"],
		"iss": appID,
	}
	claimsJSON, err := json.Marshal(claimsWithIss)
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parsePrivateKeyPEM parses a GitHub App private key, accepting either
// PKCS#1 ("BEGIN RSA PRIVATE KEY", GitHub's own download format) or PKCS#8
// ("BEGIN PRIVATE KEY").
func parsePrivateKeyPEM(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("parse private key: no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parse private key: not an RSA key")
	}
	return rsaKey, nil
}
