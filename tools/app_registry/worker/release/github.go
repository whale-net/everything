// github.go implements DispatchBuild/PollBuild's real GitHub Actions
// integration: dispatch release.yml (or another configured workflow file)
// via workflow_dispatch, using a GitHub App installation token (the exact
// pattern worker/writeback/gitops.go uses for gitops pushes -- see
// writeback.MintInstallationToken's doc comment and issue #889's Summary
// "reuse ... rather than inventing a second auth mechanism"), then poll the
// resulting run to a terminal state.
//
// # DispatchBuild's version-uniformity limitation
//
// release.yml's workflow_dispatch accepts a single top-level `version`
// input applied to every selected app/chart -- there is no per-target
// version input. ResolvedPlan.Versions (this package's workflow.go) is a
// map, since FR7/FR8 resolve independently-versioned targets in general.
// Reconciling that with release.yml's single-version input by changing
// release.yml's input schema is out of this issue's scope (see issue
// #889's "File paths / targets": release.yml is not listed). Instead,
// DispatchBuild requires every target in the resolved plan to share the
// exact same version string and passes that single version to release.yml
// as an explicit --version-equivalent input, which -- per
// tools/release_helper_go/cmd/plan.go's buildPlanResult -- makes
// release.yml's own plan-release job apply it literally to every selected
// app/chart rather than re-deriving anything, satisfying "resolved once,
// reused everywhere" for the uniform-version case. A batch with
// genuinely heterogeneous per-target versions is rejected with a clear
// error rather than silently picking one -- see uniformVersion. Lifting
// this limitation (a release.yml input-schema change carrying a full
// per-target version map) is documented follow-up work, matching this
// file's other "interim implementation" precedent (see plan.go's
// ResolvePlan doc comment).
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/worker/writeback"
)

// GitHubDispatcherConfig configures GitHubDispatcher. Every field with no
// default below is REQUIRED -- NewGitHubDispatcher fails fast if any is
// empty, mirroring writeback.NewGitOpsActivities' "do not hardcode, do not
// silently fall back" discipline (issue #798).
type GitHubDispatcherConfig struct {
	// App is the GitHub App credential set used to mint an installation
	// token -- see writeback.GitHubAppConfig's doc comment. Required.
	App writeback.GitHubAppConfig
	// Owner/Repo identify the repository release.yml lives in (normally
	// "whale-net"/"everything"). Required.
	Owner string
	Repo  string
	// WorkflowFile is the workflow file name DispatchBuild/PollBuild
	// operate on, e.g. "release.yml". Defaults to "release.yml".
	WorkflowFile string
	// Ref is the git ref (branch or tag) release.yml is dispatched
	// against. Defaults to "main".
	Ref string
}

// GitHubDispatcher implements the GitHub Actions half of DispatchBuild/
// PollBuild: mint an installation token, POST a workflow_dispatch event,
// locate the run it created, and poll that run to a terminal state.
type GitHubDispatcher struct {
	Config GitHubDispatcherConfig

	// HTTPClient issues every GitHub API request. Defaults to
	// http.DefaultClient; overridable in tests to point at an
	// httptest.Server, mirroring writeback.GitOpsActivities.HTTPClient.
	HTTPClient *http.Client
	// GitHubAPIBaseURL defaults to "https://api.github.com"; overridable in
	// tests for the same reason as HTTPClient.
	GitHubAPIBaseURL string
	// PollInterval is how long PollBuild sleeps between run-status checks.
	// Defaults to 15s. Overridden to a much smaller value in tests.
	PollInterval time.Duration
	// DispatchPollInterval/DispatchPollAttempts govern how DispatchBuild
	// looks for the run the dispatch call created -- see dispatch's doc
	// comment. Default to 2s / 15 attempts (30s total).
	DispatchPollInterval time.Duration
	DispatchPollAttempts int
	// Now defaults to time.Now; overridable in tests for determinism.
	Now func() time.Time
}

// NewGitHubDispatcher validates cfg and constructs a GitHubDispatcher.
// Returns an error (never silently falls back) if any required field is
// empty -- see GitHubDispatcherConfig's doc comment.
func NewGitHubDispatcher(cfg GitHubDispatcherConfig) (*GitHubDispatcher, error) {
	var missing []string
	if cfg.App.AppID == "" {
		missing = append(missing, "AppID")
	}
	if cfg.App.InstallationID == "" {
		missing = append(missing, "InstallationID")
	}
	if cfg.App.PrivateKeyPEM == "" {
		missing = append(missing, "PrivateKeyPEM")
	}
	if cfg.Owner == "" {
		missing = append(missing, "Owner")
	}
	if cfg.Repo == "" {
		missing = append(missing, "Repo")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("github dispatcher: missing required config: %s", strings.Join(missing, ", "))
	}
	if cfg.WorkflowFile == "" {
		cfg.WorkflowFile = "release.yml"
	}
	if cfg.Ref == "" {
		cfg.Ref = "main"
	}
	return &GitHubDispatcher{
		Config:               cfg,
		HTTPClient:           http.DefaultClient,
		GitHubAPIBaseURL:     "https://api.github.com",
		PollInterval:         15 * time.Second,
		DispatchPollInterval: 2 * time.Second,
		DispatchPollAttempts: 15,
		Now:                  time.Now,
	}, nil
}

func (d *GitHubDispatcher) httpClient() *http.Client {
	if d.HTTPClient != nil {
		return d.HTTPClient
	}
	return http.DefaultClient
}

func (d *GitHubDispatcher) baseURL() string {
	if d.GitHubAPIBaseURL != "" {
		return d.GitHubAPIBaseURL
	}
	return "https://api.github.com"
}

func (d *GitHubDispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *GitHubDispatcher) token(ctx context.Context) (string, error) {
	return writeback.MintInstallationToken(ctx, d.Config.App, d.httpClient(), d.baseURL())
}

// Dispatch implements DispatchBuild's GitHub Actions half: POST a
// workflow_dispatch event carrying inputs, then poll the workflow's runs
// list for the run it created. The workflow_dispatch API itself does not
// return a run id (issue #798-adjacent GitHub API limitation) -- the
// standard workaround, used here, is to record the time just before
// dispatching and poll `GET .../runs?event=workflow_dispatch` until a run
// created after that time appears, up to DispatchPollAttempts tries. This
// has a narrow, accepted race: if some *other* workflow_dispatch of the
// same workflow file lands in the same poll window, Dispatch could pick up
// the wrong run. Acceptable at this repo's release cadence (this plan's
// #889 does not attempt to remove it); a future improvement could pass a
// caller-chosen marker input to disambiguate, once release.yml grows one.
func (d *GitHubDispatcher) Dispatch(ctx context.Context, releaseRunID string, inputs map[string]string) (BuildRef, error) {
	token, err := d.token(ctx)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build for release run %s: %w", releaseRunID, err)
	}

	since := d.now().Add(-2 * time.Second) // small buffer against clock skew between this process and GitHub's

	body, err := json.Marshal(map[string]any{
		"ref":    d.Config.Ref,
		"inputs": inputs,
	})
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build for release run %s: marshal request: %w", releaseRunID, err)
	}
	url := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/dispatches", d.baseURL(), d.Config.Owner, d.Config.Repo, d.Config.WorkflowFile)
	if err := d.doRequest(ctx, http.MethodPost, url, token, body, http.StatusNoContent); err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build for release run %s: %w", releaseRunID, err)
	}

	ref, err := d.findDispatchedRun(ctx, token, since)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build for release run %s: %w", releaseRunID, err)
	}
	ref.ReleaseRunID = releaseRunID
	return ref, nil
}

// findDispatchedRun polls the workflow's runs list until a run created at
// or after since appears -- see Dispatch's doc comment.
func (d *GitHubDispatcher) findDispatchedRun(ctx context.Context, token string, since time.Time) (BuildRef, error) {
	interval := d.DispatchPollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	attempts := d.DispatchPollAttempts
	if attempts <= 0 {
		attempts = 15
	}

	url := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/runs?event=workflow_dispatch&per_page=10",
		d.baseURL(), d.Config.Owner, d.Config.Repo, d.Config.WorkflowFile)

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, interval); err != nil {
				return BuildRef{}, err
			}
		}
		var listResp struct {
			WorkflowRuns []struct {
				ID        int64     `json:"id"`
				HTMLURL   string    `json:"html_url"`
				CreatedAt time.Time `json:"created_at"`
			} `json:"workflow_runs"`
		}
		if err := d.doJSON(ctx, http.MethodGet, url, token, nil, &listResp); err != nil {
			return BuildRef{}, err
		}
		for _, run := range listResp.WorkflowRuns {
			if !run.CreatedAt.Before(since) {
				return BuildRef{RunID: fmt.Sprintf("%d", run.ID), RunURL: run.HTMLURL}, nil
			}
		}
	}
	return BuildRef{}, fmt.Errorf("no %s run observed within %d attempts after dispatch", d.Config.WorkflowFile, attempts)
}

// PollRun polls the GitHub Actions run identified by ref until it reaches
// status "completed", sleeping PollInterval between checks.
func (d *GitHubDispatcher) PollRun(ctx context.Context, ref BuildRef) (BuildStatus, error) {
	token, err := d.token(ctx)
	if err != nil {
		return BuildStatus{}, fmt.Errorf("poll build %s: %w", ref.RunID, err)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%s", d.baseURL(), d.Config.Owner, d.Config.Repo, ref.RunID)
	interval := d.PollInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}

	for {
		var run struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		}
		if err := d.doJSON(ctx, http.MethodGet, url, token, nil, &run); err != nil {
			return BuildStatus{}, fmt.Errorf("poll build %s: %w", ref.RunID, err)
		}
		if run.Status == "completed" {
			return BuildStatus{
				Succeeded: run.Conclusion == "success",
				Detail:    fmt.Sprintf("run %s conclusion=%s", ref.RunID, run.Conclusion),
			}, nil
		}
		if err := sleepCtx(ctx, interval); err != nil {
			return BuildStatus{}, fmt.Errorf("poll build %s: %w", ref.RunID, err)
		}
	}
}

// doRequest issues a GitHub API request and requires the response status
// to equal wantStatus, discarding the response body.
func (d *GitHubDispatcher) doRequest(ctx context.Context, method, url, token string, body []byte, wantStatus int) error {
	req, err := d.newRequest(ctx, method, url, token, body)
	if err != nil {
		return err
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != wantStatus {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d from %s %s: %s", resp.StatusCode, method, url, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// doJSON issues a GitHub API request and decodes a 2xx JSON response into
// out.
func (d *GitHubDispatcher) doJSON(ctx context.Context, method, url, token string, body []byte, out any) error {
	req, err := d.newRequest(ctx, method, url, token, body)
	if err != nil {
		return err
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d from %s %s: %s", resp.StatusCode, method, url, strings.TrimSpace(string(respBody)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *GitHubDispatcher) newRequest(ctx context.Context, method, url, token string, body []byte) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// sleepCtx sleeps for d, returning ctx.Err() early if ctx is cancelled
// first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// uniformVersion returns the single version string shared by every entry
// in versions, or an error if versions is empty or contains more than one
// distinct value -- see this file's package doc comment
// ("DispatchBuild's version-uniformity limitation").
func uniformVersion(versions map[string]string) (string, error) {
	var v string
	for _, val := range versions {
		if v == "" {
			v = val
			continue
		}
		if val != v {
			return "", fmt.Errorf("resolved plan has heterogeneous versions (%q vs %q): DispatchBuild requires a single uniform version across the batch until release.yml gains a per-target version input -- see github.go's package doc comment", v, val)
		}
	}
	if v == "" {
		return "", fmt.Errorf("resolved plan has no versions")
	}
	return v, nil
}

// splitPlanTargets separates a ResolvedPlan.Versions map (keyed by
// repository.TargetKey format, "<kind>:<owner>") into sorted app and chart
// owner-name lists, the shape release.yml's `apps`/`helm_charts`
// workflow_dispatch inputs expect (comma-joined) -- see
// tools/release_helper_go/cmd/plan.go's resolveApps, which matches
// requested names against AppMetadata.FullName(), the exact format
// OwnerFullName already is.
func splitPlanTargets(versions map[string]string) (apps, charts []string, err error) {
	for key := range versions {
		kind, owner, ok := parseTargetKey(key)
		if !ok {
			return nil, nil, fmt.Errorf("malformed resolved-plan target key %q", key)
		}
		switch kind {
		case repository.ArtifactKindImage:
			apps = append(apps, owner)
		case repository.ArtifactKindChart:
			charts = append(charts, owner)
		default:
			return nil, nil, fmt.Errorf("resolved-plan target key %q has unsupported kind %q", key, kind)
		}
	}
	sort.Strings(apps)
	sort.Strings(charts)
	return apps, charts, nil
}

// parseTargetKey reverses repository.TargetKey's "<kind>:<owner>" format.
func parseTargetKey(key string) (repository.ArtifactKind, string, bool) {
	idx := strings.Index(key, ":")
	if idx < 0 {
		return "", "", false
	}
	return repository.ArtifactKind(key[:idx]), key[idx+1:], true
}
