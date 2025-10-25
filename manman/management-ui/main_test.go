package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

func TestHealthEndpoint(t *testing.T) {
	// Create a mock config
	config := &Config{
		Host:             "0.0.0.0",
		Port:             "8000",
		AuthMode:         "none",
		SessionSecret:    "test-secret-key",
		ExperienceAPIURL: "http://localhost:8000",
	}

	// Create authenticator in no-auth mode for testing
	ctx := context.Background()
	auth, err := htmxauth.NewAuthenticator(ctx, htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: config.SessionSecret,
	})
	assert.NoError(t, err)

	// Create app
	app := &App{
		config: config,
		auth:   auth,
	}

	// Create request
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	// Call handler
	app.handleHealth(w, req)

	// Assert response
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "healthy")
}
