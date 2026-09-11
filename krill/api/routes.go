package main

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// setupRoutes registers krill's HTTP surface. No spec endpoints exist yet
// (see main.go's doc comment) -- /healthz is the only route.
func setupRoutes(mux *http.ServeMux, pool *pgxpool.Pool) {
	mux.HandleFunc("/healthz", handleHealthz(pool))
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
