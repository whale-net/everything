package argocd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := NewClient(Config{ServerURL: srv.URL, AuthToken: "test-token"}, srv.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

func requireBearerToken(t *testing.T, r *http.Request) {
	t.Helper()
	got := r.Header.Get("Authorization")
	if got != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
	}
}

func TestNewClient_RequiresServerURLAndAuthToken(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"missing both", Config{}},
		{"missing ServerURL", Config{AuthToken: "tok"}},
		{"missing AuthToken", Config{ServerURL: "https://argocd.example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(tc.cfg, nil)
			if err == nil {
				t.Fatalf("NewClient(%+v) = %v, nil; want error", tc.cfg, c)
			}
			if c != nil {
				t.Errorf("NewClient(%+v) returned non-nil client on error", tc.cfg)
			}
		})
	}
}

func TestNewClient_Success(t *testing.T) {
	c, err := NewClient(Config{ServerURL: "https://argocd.example.com", AuthToken: "tok"}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("NewClient returned nil client with nil error")
	}
}

func TestRefresh_Success(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		requireBearerToken(t, r)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	if err := c.Refresh(context.Background(), "my-project", "my-app"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/api/v1/applications/my-app" {
		t.Errorf("path = %q, want /api/v1/applications/my-app", gotPath)
	}
	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("url.ParseQuery: %v", err)
	}
	if q.Get("refresh") != "normal" {
		t.Errorf("refresh query param = %q, want normal", q.Get("refresh"))
	}
	if q.Get("project") != "my-project" {
		t.Errorf("project query param = %q, want my-project", q.Get("project"))
	}
}

func TestRefresh_NonOKStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("boom"))
			})

			err := c.Refresh(context.Background(), "proj", "app")
			if err == nil {
				t.Fatal("Refresh: got nil error, want non-nil")
			}
			if !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Errorf("Refresh error %q does not contain status code %d", err.Error(), status)
			}
		})
	}
}

func TestSync_Success(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody syncRequest
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		requireBearerToken(t, r)
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	if err := c.Sync(context.Background(), "my-project", "my-app"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/applications/my-app/sync" {
		t.Errorf("path = %q, want /api/v1/applications/my-app/sync", gotPath)
	}
	if gotBody.Name != "my-app" {
		t.Errorf("request body Name = %q, want my-app", gotBody.Name)
	}
	if gotBody.Project != "my-project" {
		t.Errorf("request body Project = %q, want my-project", gotBody.Project)
	}
}

func TestSync_NonOKStatus(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("rbac denied"))
	})

	err := c.Sync(context.Background(), "proj", "app")
	if err == nil {
		t.Fatal("Sync: got nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("Sync error %q does not contain status code 403", err.Error())
	}
}

func TestGetStatus_SyncedHealthy(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearerToken(t, r)
		if r.URL.Path != "/api/v1/applications/my-app" {
			t.Errorf("path = %q, want /api/v1/applications/my-app", r.URL.Path)
		}
		if r.URL.Query().Get("project") != "my-project" {
			t.Errorf("project query param = %q, want my-project", r.URL.Query().Get("project"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"status": {
				"sync": {"status": "Synced"},
				"health": {"status": "Healthy"}
			}
		}`))
	})

	syncStatus, health, err := c.GetStatus(context.Background(), "my-project", "my-app")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if syncStatus != "Synced" {
		t.Errorf("syncStatus = %q, want Synced", syncStatus)
	}
	if health != "Healthy" {
		t.Errorf("health = %q, want Healthy", health)
	}
}

func TestGetStatus_DegradedOutOfSync(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"status": {
				"sync": {"status": "OutOfSync"},
				"health": {"status": "Degraded"}
			}
		}`))
	})

	syncStatus, health, err := c.GetStatus(context.Background(), "my-project", "my-app")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if syncStatus != "OutOfSync" {
		t.Errorf("syncStatus = %q, want OutOfSync", syncStatus)
	}
	if health != "Degraded" {
		t.Errorf("health = %q, want Degraded", health)
	}
}

func TestGetStatus_NonOKStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("error body"))
			})

			_, _, err := c.GetStatus(context.Background(), "proj", "app")
			if err == nil {
				t.Fatal("GetStatus: got nil error, want non-nil")
			}
			if !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Errorf("GetStatus error %q does not contain status code %d", err.Error(), status)
			}
		})
	}
}
