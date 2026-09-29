package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/apiclient"
)

func TestMintSession_AttachesClientCredentialsToken(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"machine-tok","token_type":"Bearer","expires_in":300}`))
	}))
	defer idp.Close()
	want := uuid.New()
	var gotAuth, gotPath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"session_id":"` + want.String() + `","scope_id":"` + uuid.NewString() + `"}`))
	}))
	defer api.Close()

	got, err := mintSession(context.Background(), api.URL, apiclient.ClientCredentialsConfig{TokenURL: idp.URL, ClientID: "id", ClientSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want || gotPath != "/sessions/init" || gotAuth != "Bearer machine-tok" {
		t.Fatalf("got id=%v path=%q auth=%q", got, gotPath, gotAuth)
	}
}

func TestMintSession_RequiresCredentials(t *testing.T) {
	if _, err := mintSession(context.Background(), "http://x", apiclient.ClientCredentialsConfig{}); err == nil {
		t.Fatal("expected error")
	}
}
