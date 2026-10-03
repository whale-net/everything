package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewHandlerServesUnauthenticatedMetadataAndChallenge(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ts := httptest.NewServer(NewHandler(next, fakeVerifier{}, "https://kc/realms/r", "https://mcp.example/", "", nil))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata status = %d, want 200", resp.StatusCode)
	}
	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.Resource != "https://mcp.example" || len(meta.AuthorizationServers) != 1 || meta.AuthorizationServers[0] != "https://kc/realms/r" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	resp401, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp401.Body.Close()
	if resp401.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp401.StatusCode)
	}
	want := `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource"`
	if got := resp401.Header.Get("WWW-Authenticate"); got != want {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
	}
}

func TestNewHandlerWithoutPublicURLOmitsMetadata(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	ts := httptest.NewServer(NewHandler(next, fakeVerifier{}, "https://kc/realms/r", "", "", nil))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
