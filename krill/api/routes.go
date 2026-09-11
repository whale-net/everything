package main

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// setupRoutes registers krill's HTTP surface. /healthz and `init` (FR3,
// issue #2489) are the only routes as of this task -- no spec entity write
// endpoints exist yet (those land in #2490/#2492/#2493/#2496, each of
// which wraps its handler with handlers.RequireSession).
func setupRoutes(mux *http.ServeMux, pool *pgxpool.Pool) {
	sessions := store.NewSessionStore(pool)

	mux.HandleFunc("/healthz", handleHealthz(pool))
	mux.HandleFunc("POST /sessions/init", handlers.InitSessionHandler(sessions))
}

// handleHealthz reports ok only if a live Postgres ping succeeds -- a
// static 200 would not exercise the one dependency this binary has
// (issue #2487: "/healthz and DB connectivity check only").
func handleHealthz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if err := pool.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  err.Error(),
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
