package main

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// setupRoutes registers krill's HTTP surface. /healthz, `init` (FR3, issue
// #2489), the four scoped-slice query endpoints (FR5-FR9, issue #2491),
// and the history endpoints below (FR11, issue #2493) are ungated; every
// entity create/attach endpoint (issue #2490, FR1/FR2/FR4) and the two
// amend endpoints below (FR12, issue #2493) are wrapped with
// handlers.RequireSession (gate.go) -- no write path is reachable without
// a session minted by `init`. Read paths never require a session (root
// plan issue #2485). Import (FR16) and pointer-issue create (FR20) are
// later tasks' routes, not added here.
func setupRoutes(mux *http.ServeMux, pool *pgxpool.Pool) {
	sessions := store.NewSessionStore(pool)
	entities := store.New(pool)
	gate := handlers.RequireSession(sessions)

	mux.HandleFunc("/healthz", handleHealthz(pool))
	mux.HandleFunc("POST /sessions/init", handlers.InitSessionHandler(sessions))

	mux.Handle("POST /products", gate(handlers.CreateProductHandler(entities.Products())))
	mux.Handle("POST /feature-sets", gate(handlers.CreateFeatureSetHandler(entities.FeatureSets())))
	mux.Handle("POST /features", gate(handlers.CreateFeatureHandler(entities.Features())))
	mux.Handle("POST /requirements", gate(handlers.CreateRequirementHandler(entities.Requirements())))
	mux.Handle("POST /load-bearing-decisions", gate(handlers.AttachLoadBearingDecisionHandler(entities.Decisions())))

	mux.Handle("POST /requirements/{id}/amend", gate(handlers.AmendRequirementHandler(entities.Amend())))
	mux.Handle("POST /load-bearing-decisions/{id}/amend", gate(handlers.AmendLoadBearingDecisionHandler(entities.Amend())))

	mux.HandleFunc("GET /requirements/{id}/as-of", handlers.GetRequirementAsOfHandler(entities.History()))
	mux.HandleFunc("GET /requirements/{id}/versions", handlers.ListRequirementVersionsHandler(entities.History()))
	mux.HandleFunc("GET /load-bearing-decisions/{id}/as-of", handlers.GetLoadBearingDecisionAsOfHandler(entities.History()))
	mux.HandleFunc("GET /load-bearing-decisions/{id}/versions", handlers.ListLoadBearingDecisionVersionsHandler(entities.History()))

	querier := slice.NewQuerier(entities)
	handlers.NewSlice(querier).Register(mux)
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
