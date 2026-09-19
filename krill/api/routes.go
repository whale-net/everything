package main

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/forge"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// setupRoutes registers krill's HTTP surface. /healthz, `init` (FR3, issue
// #2489), the four M1 scoped-slice query endpoints (FR5-FR9, issue #2491),
// the fifth, session-scoped granularity below (FR5, M2, issue #2544), the
// history endpoints below (FR11, issue #2493), GET /design-sessions/{id}
// (issue #2543), GET /design-sessions/{id}/open-questions (issue #2545,
// FR6), and GET /milestones/{id} (issue #2683, FR1/FR2) are ungated; every
// entity
// create/attach endpoint (issue #2490, FR1/FR2/FR4), the two amend
// endpoints below (FR12, issue #2493), the pointer-artifact create
// endpoint (issue #2496, FR20), POST /design-sessions and POST
// /design-sessions/{id}/revision-events (issue #2543, FR1-FR4/FR8), POST
// /design-sessions/{id}/propose (issue #2546, FR9/FR10/NFR2), the five
// POST /milestones* endpoints below (issue #2683, FR1/FR2, LB4), and the
// three POST /milestones/{id}/milepebbles, /milepebbles/{id}/delivers,
// and /milepebbles/{id}/discovered-scope endpoints (issue #2684, FR3/FR4),
// POST /milestones/{id}/status (issue #2685, FR8/FR9/FR12), POST
// /milestones/{id}/shipped (issue #2686, FR10), POST /delivery/move
// (issue #2687, FR5), and POST /milestones/{id}/abandon (issue #2688,
// FR6) are wrapped with handlers.RequireSession (gate.go) --
// no write path is reachable without a session minted by `init`. Read
// paths never require a session (root plan issue #2485) -- this includes
// GET /milestones/{id}/status, GET /milestones/{id}/status/history, GET
// /milestones/{id}/delivery, GET /products/{id}/backlog, and GET
// /products/{id}/delivery (issue #2689, FR11).
// Import (FR16) is a later task's route, not added here.
//
// POST /tasks (the work-axis task-create endpoint, issue #2719, FR1) is
// gated the same way every other write endpoint above is -- scope_id and
// both subjects come from the session RequireSession resolves, never the
// request body (NFR6).
//
// githubToken is KRILL_GITHUB_TOKEN (see main.go's config/../ENV.md) --
// threaded through to //krill/forge.GitHubClient, the one dependency
// POST /pointer-artifacts has that no other route in this binary does.
func setupRoutes(mux *http.ServeMux, pool *pgxpool.Pool, githubToken string) {
	sessions := store.NewSessionStore(pool)
	entities := store.New(pool)
	gate := handlers.RequireSession(sessions)
	forgeClient := &forge.GitHubClient{Token: githubToken}
	querier := slice.NewQuerier(entities)

	mux.HandleFunc("/healthz", handleHealthz(pool))
	mux.HandleFunc("POST /sessions/init", handlers.InitSessionHandler(sessions))

	mux.Handle("POST /products", gate(handlers.CreateProductHandler(entities.Products())))
	mux.Handle("POST /feature-sets", gate(handlers.CreateFeatureSetHandler(entities.FeatureSets())))
	mux.Handle("POST /features", gate(handlers.CreateFeatureHandler(entities.Features())))
	mux.Handle("POST /requirements", gate(handlers.CreateRequirementHandler(entities.Requirements())))
	mux.Handle("POST /load-bearing-decisions", gate(handlers.AttachLoadBearingDecisionHandler(entities.Decisions())))
	mux.Handle("POST /pointer-artifacts", gate(handlers.CreatePointerArtifactHandler(entities.Products(), entities.Scopes(), entities.PointerArtifacts(), forgeClient)))

	mux.Handle("POST /milestones", gate(handlers.CreateMilestoneHandler(entities.MilestoneAuthoring())))
	mux.Handle("POST /milestones/{id}/fr-budget", gate(handlers.SetFRBudgetHandler(entities.MilestoneAuthoring())))
	mux.Handle("POST /milestones/{id}/delivers", gate(handlers.AddDeliversHandler(entities.MilestoneAuthoring())))
	mux.Handle("POST /milestones/{id}/must-not-foreclose", gate(handlers.AddMustNotForecloseHandler(entities.MilestoneAuthoring())))
	mux.Handle("POST /milestones/{id}/deferrals", gate(handlers.AddDeferralHandler(entities.MilestoneAuthoring())))
	mux.HandleFunc("GET /milestones/{id}", handlers.GetMilestoneHandler(entities.MilestoneAuthoring()))

	mux.Handle("POST /milestones/{id}/milepebbles", gate(handlers.CreateMilepebbleHandler(entities.MilestoneAuthoring())))
	mux.Handle("POST /milepebbles/{id}/delivers", gate(handlers.AddMilepebbleDeliversHandler(entities.MilestoneAuthoring())))
	mux.Handle("POST /milepebbles/{id}/discovered-scope", gate(handlers.AddDiscoveredScopeHandler(entities.MilestoneAuthoring())))
	mux.HandleFunc("GET /milepebbles/{id}", handlers.GetMilepebbleHandler(entities.MilestoneAuthoring()))
	mux.HandleFunc("GET /milestones/{id}/milepebbles", handlers.ListMilepebblesHandler(entities.MilestoneAuthoring()))

	// milestone_status_event (issue #2685, FR8/FR9/FR12) serves both a
	// MilestoneKindMilestone and a MilestoneKindMilepebble row -- both are
	// `milestone_ref` rows, so one route pair covers both without a
	// milepebble-specific alias.
	mux.Handle("POST /milestones/{id}/status", gate(handlers.SetMilestoneStatusHandler(entities.MilestoneStatus())))
	mux.HandleFunc("GET /milestones/{id}/status", handlers.GetMilestoneStatusHandler(entities.MilestoneStatus()))
	mux.HandleFunc("GET /milestones/{id}/status/history", handlers.GetMilestoneStatusHistoryHandler(entities.MilestoneStatus()))

	// delivery_shipment (issue #2686, FR10) also serves both a
	// MilestoneKindMilestone and a MilestoneKindMilepebble row, same
	// posture as the status routes just above.
	mux.Handle("POST /milestones/{id}/shipped", gate(handlers.MarkShippedHandler(entities.DeliveryShipments())))
	mux.HandleFunc("GET /milestones/{id}/delivery", handlers.GetDeliveryBreakdownHandler(entities.MilestoneStatus(), querier))

	// The delivery-axis re-cut surface (issue #2687, FR5): moving
	// not-yet-shipped scope to a different milestone, milepebble, or the
	// backlog bucket, plus reading the bucket's current contents.
	mux.Handle("POST /delivery/move", gate(handlers.MoveScopeHandler(entities.Recut())))
	mux.HandleFunc("GET /products/{id}/backlog", handlers.GetBacklogHandler(querier))

	// The abandon verb (issue #2688, FR6): composes #2685's status
	// transitions, #2686's not-yet-shipped definition, and #2687's
	// backlog bucket/move primitive into one atomic call. Serves both a
	// MilestoneKindMilestone and a MilestoneKindMilepebble row, same
	// posture as the status/shipment routes above.
	mux.Handle("POST /milestones/{id}/abandon", gate(handlers.AbandonHandler(entities.Abandon())))

	// GET /products/{id}/delivery (issue #2689, FR11, C28) is a whole
	// product's delivery-axis listing, filterable by `status` -- ungated
	// like every other read endpoint in this package.
	mux.HandleFunc("GET /products/{id}/delivery", handlers.GetProductDeliveryHandler(entities.Products(), querier))

	// The work axis (M4, issue #2719, FR1): a task scoped to exactly one
	// milepebble, or to a milestone directly when that milestone has no
	// milepebble cut.
	mux.Handle("POST /tasks", gate(handlers.CreateTaskHandler(entities.Tasks())))

	// task_dependency (issue #2720, FR2): a Swarm Operator declares that
	// one task depends on one or more others -- POST gated (NFR6), GET
	// ungated like every other read endpoint in this package.
	mux.Handle("POST /tasks/{id}/dependencies", gate(handlers.DeclareTaskDependenciesHandler(entities.Tasks())))
	mux.HandleFunc("GET /tasks/{id}/dependencies", handlers.ListTaskDependenciesHandler(entities.Tasks()))

	mux.Handle("POST /design-sessions", gate(handlers.OpenDesignSessionHandler(entities.DesignSessions())))
	mux.HandleFunc("GET /design-sessions/{id}", handlers.GetDesignSessionHandler(entities.DesignSessions(), entities.RevisionEvents()))
	mux.Handle("POST /design-sessions/{id}/revision-events", gate(handlers.AppendRevisionEventHandler(entities.RevisionEvents())))
	mux.HandleFunc("GET /design-sessions/{id}/open-questions", handlers.ListOpenQuestionsHandler(entities.DesignSessions(), entities.RevisionEvents()))
	mux.Handle("POST /design-sessions/{id}/propose", gate(handlers.ProposeEntitiesHandler(entities.MediatedWrites())))

	mux.Handle("POST /requirements/{id}/amend", gate(handlers.AmendRequirementHandler(entities.Amend())))
	mux.Handle("POST /load-bearing-decisions/{id}/amend", gate(handlers.AmendLoadBearingDecisionHandler(entities.Amend())))

	mux.HandleFunc("GET /requirements/{id}/as-of", handlers.GetRequirementAsOfHandler(entities.History()))
	mux.HandleFunc("GET /requirements/{id}/versions", handlers.ListRequirementVersionsHandler(entities.History()))
	mux.HandleFunc("GET /load-bearing-decisions/{id}/as-of", handlers.GetLoadBearingDecisionAsOfHandler(entities.History()))
	mux.HandleFunc("GET /load-bearing-decisions/{id}/versions", handlers.ListLoadBearingDecisionVersionsHandler(entities.History()))

	handlers.NewSlice(querier).Register(mux)
	handlers.NewSessionSlice(entities.DesignSessions(), entities.RevisionEvents(), querier).Register(mux)
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
