package main

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/forge"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
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
// POST /tasks (the work-axis task-create endpoint, issue #2719, FR1) and
// POST /tasks/{id}/claim (the claim endpoint, issue #2722, FR3/FR5) are
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
	assembler := work.NewAssembler(entities.Tasks(), querier)

	mux.HandleFunc("/healthz", handleHealthz(pool))
	mux.HandleFunc("POST /sessions/init", handlers.InitSessionHandler(sessions))

	mux.Handle("POST /products", gate(handlers.CreateProductHandler(entities.Products())))
	// Product discovery (issue #2941): ungated read, scope_id is a
	// required query parameter since a Product has no parent entity.
	mux.HandleFunc("GET /products", handlers.ListProductsHandler(entities.Products()))
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
	// Task discovery (issue #2941): every task scoped to one milestone_ref
	// row (milepebble or uncut milestone), ungated read.
	mux.HandleFunc("GET /milestones/{id}/tasks", handlers.ListTasksHandler(entities.Tasks()))

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

	// The work-axis task payload document (issue #2721, FR4/FR10): the
	// by-task-id fetch that makes resumption on a different host a
	// re-fetch rather than a dedicated verb. Ungated like every other read
	// endpoint in this package (NFR6's gate is write-only).
	mux.HandleFunc("GET /tasks/{id}", handlers.GetTaskPayloadHandler(entities.Tasks(), assembler))

	// task_claim (issue #2722, FR3/FR5): an Agent with an active session
	// claims a task -- race-safe (a Postgres row lock, not an
	// application-level mutex), mints a lease, and records one attempt.
	// Gated like every other write endpoint (NFR6); the response is the
	// same work.Payload document GET /tasks/{id} returns, enriched with
	// the fresh lease.
	mux.Handle("POST /tasks/{id}/claim", gate(handlers.ClaimTaskHandler(entities.Tasks(), assembler)))

	// task_lease_event (issue #2723, FR6): the current claimant extends its
	// own lease. Gated like every other write endpoint (NFR6); a stale
	// claim id (reclaimed, or already released by complete/abandon) is
	// rejected with a 409, never a silently-accepted no-op.
	mux.Handle("POST /tasks/{id}/heartbeat", gate(handlers.HeartbeatHandler(entities.Tasks())))

	// task_complete (issue #2725, FR8): an Agent holding a task's current
	// claim reports a pass/fail verdict -- krill, not the caller, decides
	// whether the task advances or reverts one lane in its own lane
	// sequence. Gated like every other write endpoint (NFR6); the
	// response is the same work.Payload document GET /tasks/{id} and
	// claim return.
	mux.Handle("POST /tasks/{id}/complete", gate(handlers.CompleteTaskHandler(entities.Tasks(), assembler)))

	// task_abandon (issue #2726, FR9): an Agent holding a task's current
	// claim releases it without reporting a verdict -- current_lane is
	// unchanged, and the abandon counts as an attempt against the same
	// DefaultAttemptCap #2724's reclaim sweep enforces. Gated like every
	// other write endpoint (NFR6); the response is the same work.Payload
	// document GET /tasks/{id} and claim/complete return. Distinct from
	// the pre-existing delivery-axis POST /milestones/{id}/abandon.
	mux.Handle("POST /tasks/{id}/abandon", gate(handlers.AbandonTaskHandler(entities.Tasks(), assembler)))

	// task_reclaim (issue #2724, FR7): an operator/automation-triggered
	// sweep of the caller's own scope for lease-expired tasks -- gated like
	// every other write endpoint (NFR6). No background scheduler/cron
	// exists in this milestone; this is the sweep's only entry point aside
	// from ClaimTask's own expired-lease branch (#2722), which both call
	// the same underlying closure logic rather than duplicating it.
	mux.Handle("POST /tasks/reclaim", gate(handlers.ReclaimExpiredHandler(entities.Tasks())))

	// task_note (issue #2727, FR11/FR12): any Agent, claimant or not, can
	// record a flat, immutable note against a task or a spec-axis entity
	// -- POST gated (NFR6; the only other gate is the session requirement
	// itself, never current_claim_id), GET ungated like every other read
	// endpoint in this package.
	mux.Handle("POST /notes", gate(handlers.RecordNoteHandler(entities.Tasks())))
	mux.HandleFunc("GET /tasks/{id}/notes", handlers.ListTaskNotesHandler(entities.Tasks()))

	// task_note_lifecycle_event (issue #2874, FR11): any persona
	// transitions a note's lifecycle status -- POST gated (NFR6), the same
	// session-only gate POST /notes uses, deliberately never restricted to
	// PersonaSwarmOperator (see task_note_lifecycle.go's own doc comment).
	mux.Handle("POST /notes/{id}/lifecycle", gate(handlers.TransitionNoteLifecycleHandler(entities.Tasks())))

	// M5's console query surface (issue #2869, FR4, NFR6): GET
	// /console/claimed, ungated like every other read endpoint in this
	// package -- unlike every other ungated GET route above, this query
	// has no single path entity to resolve scope_id from, so scope_id is
	// a required query parameter instead (see console.go's own doc
	// comment). GET /console/notes (issue #2874, FR12) is the same shape,
	// over store.TaskStore.ListOpenNotes.
	mux.HandleFunc("GET /console/claimed", handlers.ListClaimedTasksHandler(entities.Tasks()))
	mux.HandleFunc("GET /console/notes", handlers.ListOpenNotesHandler(entities.Tasks()))

	// task_cancel (issue #2873, FR7): a Swarm Operator moves any task --
	// escalated or not -- into a dead-lettered terminal state, distinct
	// from lane Done, that claim never again returns and requeue cannot
	// reopen. Gated like every other write endpoint (NFR6); the response
	// is the same work.Payload document GET /tasks/{id} and claim/
	// complete/abandon return.
	mux.Handle("POST /tasks/{id}/cancel", gate(handlers.CancelTaskHandler(entities.Tasks(), assembler)))

	// FR10's cancelled-task console view (issue #2873): GET
	// /console/cancelled, ungated and scope_id-as-query-parameter like GET
	// /console/claimed above.
	mux.HandleFunc("GET /console/cancelled", handlers.ListCancelledTasksHandler(entities.Tasks()))

	// FR5's escalated-task console view (issue #2875): GET
	// /console/escalated, ungated and scope_id-as-query-parameter like GET
	// /console/claimed above -- the milestone's headline query, over
	// store.TaskStore.ListEscalatedTasks.
	mux.HandleFunc("GET /console/escalated", handlers.ListEscalatedTasksHandler(entities.Tasks()))

	// task_release (issue #2872, FR8): a Swarm Operator force-closes the
	// active lease on a claimed task directly, independent of lease
	// expiry -- counts as an attempt against the same DefaultAttemptCap
	// M4's claim/reclaim/abandon paths enforce. Gated like every other
	// write endpoint (NFR6); the response is the same work.Payload
	// document GET /tasks/{id} and claim/complete/abandon/cancel return.
	mux.Handle("POST /tasks/{id}/release", gate(handlers.ReleaseTaskHandler(entities.Tasks(), assembler)))

	// task_escalate (issue #2872, FR9): a Swarm Operator manually
	// escalates a task at any time, the same reasoned escalation event
	// FR2/FR3 record automatically but with reason 'manual'. Gated like
	// every other write endpoint (NFR6); the response is the same
	// work.Payload document GET /tasks/{id} and release return.
	mux.Handle("POST /tasks/{id}/escalate", gate(handlers.EscalateTaskHandler(entities.Tasks(), assembler)))

	// task_requeue (issue #2876, FR6): a Swarm Operator returns an
	// escalated task to claimable, resetting exactly the counter (thrash
	// or attempt) whose cap triggered the escalation being resolved --
	// the "recover" half of the recover-or-terminate pair cancel is the
	// other half of. Gated like every other write endpoint (NFR6); the
	// response is the same work.Payload document GET /tasks/{id} and
	// claim/complete/abandon/cancel/release/escalate return.
	mux.Handle("POST /tasks/{id}/requeue", gate(handlers.RequeueTaskHandler(entities.Tasks(), assembler)))

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
