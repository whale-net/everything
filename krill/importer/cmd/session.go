package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/apiclient"
)

// mintSession calls POST <apiURL>/sessions/init with a client_credentials
// bearer token and returns the minted session id.
func mintSession(ctx context.Context, apiURL string, cc apiclient.ClientCredentialsConfig) (uuid.UUID, error) {
	ts, err := apiclient.NewClientCredentialsSource(ctx, cc)
	if err != nil {
		return uuid.Nil, err
	}
	client := &http.Client{Transport: &apiclient.Transport{Machine: ts}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(apiURL, "/")+"/sessions/init", nil)
	if err != nil {
		return uuid.Nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("POST /sessions/init: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return uuid.Nil, fmt.Errorf("POST /sessions/init: unexpected status %d", resp.StatusCode)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return uuid.Nil, fmt.Errorf("decode /sessions/init response: %w", err)
	}
	id, err := uuid.Parse(out.SessionID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("/sessions/init returned invalid session_id: %w", err)
	}
	return id, nil
}
