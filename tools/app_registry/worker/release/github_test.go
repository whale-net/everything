package release

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/tools/app_registry/worker/writeback"
)

func encodeTestPEM(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func newTestDispatcher(t *testing.T, handler http.Handler) *GitHubDispatcher {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	d, err := NewGitHubDispatcher(GitHubDispatcherConfig{
		App: writeback.GitHubAppConfig{
			AppID:          "1",
			InstallationID: "1",
			PrivateKeyPEM:  encodeTestPEM(t, key),
		},
		Owner: "whale-net",
		Repo:  "everything",
	})
	require.NoError(t, err)
	d.GitHubAPIBaseURL = server.URL
	d.HTTPClient = server.Client()
	d.PollInterval = time.Millisecond
	d.DispatchPollInterval = time.Millisecond
	d.DispatchPollAttempts = 5
	return d
}

// TestGitHubDispatcher_Dispatch_FindsCreatedRun exercises Dispatch end to
// end against a stub GitHub API: the token exchange, the workflow_dispatch
// POST, and the runs-list poll that locates the run it created (see
// Dispatch's doc comment on why the API needs a poll at all).
func TestGitHubDispatcher_Dispatch_FindsCreatedRun(t *testing.T) {
	var sawDispatchBody map[string]any
	pollCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&sawDispatchBody))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		if pollCount < 2 {
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":42,"html_url":"https://github.com/whale-net/everything/actions/runs/42","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	d := newTestDispatcher(t, mux)
	ref, err := d.Dispatch(context.Background(), "release-run-1", map[string]string{"version": "v1.2.3", "apps": "demo-widget"}, "")
	require.NoError(t, err)
	require.Equal(t, "42", ref.RunID)
	require.Equal(t, "release-run-1", ref.ReleaseRunID)
	require.Equal(t, "https://github.com/whale-net/everything/actions/runs/42", ref.RunURL)
	require.Equal(t, "main", sawDispatchBody["ref"])
	inputs, ok := sawDispatchBody["inputs"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "v1.2.3", inputs["version"])
}

func TestGitHubDispatcher_Dispatch_NoRunFound_Errors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
	})

	d := newTestDispatcher(t, mux)
	_, err := d.Dispatch(context.Background(), "release-run-1", map[string]string{"version": "v1.2.3"}, "")
	require.Error(t, err)
}

// TestGitHubDispatcher_PollRun_TerminalStates covers both a successful and
// a failed run conclusion.
func TestGitHubDispatcher_PollRun_TerminalStates(t *testing.T) {
	cases := []struct {
		name       string
		conclusion string
		wantOK     bool
	}{
		{"success", "success", true},
		{"failure", "failure", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
			})
			mux.HandleFunc("/repos/whale-net/everything/actions/runs/42", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"status":"completed","conclusion":"` + tc.conclusion + `"}`))
			})

			d := newTestDispatcher(t, mux)
			status, err := d.PollRun(context.Background(), BuildRef{RunID: "42"})
			require.NoError(t, err)
			require.Equal(t, tc.wantOK, status.Succeeded)
		})
	}
}

// TestGitHubDispatcher_PollRun_WaitsForTerminal proves PollRun keeps
// polling (rather than returning early) while the run is still in_progress.
func TestGitHubDispatcher_PollRun_WaitsForTerminal(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			_, _ = w.Write([]byte(`{"status":"in_progress","conclusion":""}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","conclusion":"success"}`))
	})

	d := newTestDispatcher(t, mux)
	status, err := d.PollRun(context.Background(), BuildRef{RunID: "42"})
	require.NoError(t, err)
	require.True(t, status.Succeeded)
	require.GreaterOrEqual(t, calls, 3)
}

func TestUniformVersion(t *testing.T) {
	v, err := uniformVersion(map[string]string{"image:a": "v1.0.0", "chart:b": "v1.0.0"})
	require.NoError(t, err)
	require.Equal(t, "v1.0.0", v)

	_, err = uniformVersion(map[string]string{"image:a": "v1.0.0", "chart:b": "v1.0.1"})
	require.Error(t, err)

	_, err = uniformVersion(nil)
	require.Error(t, err)
}

func TestSplitPlanTargets(t *testing.T) {
	apps, charts, err := splitPlanTargets(map[string]string{
		"image:demo-widget": "v1.0.0",
		"chart:demo-achart": "v1.0.0",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"demo-widget"}, apps)
	require.Equal(t, []string{"demo-achart"}, charts)

	_, _, err = splitPlanTargets(map[string]string{"binary:demo-tool": "v1.0.0"})
	require.Error(t, err)

	_, _, err = splitPlanTargets(map[string]string{"malformed": "v1.0.0"})
	require.Error(t, err)
}
