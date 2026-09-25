// Command mcp is krill's MCP surface: the only MCP-capable way any harness
// (Claude Code today) reaches the FR5-FR9 scoped-slice query
// (//krill/slice) and, as of issue #2547, the FR1-FR10 design-session
// surface (//krill/mcp/tools' design.go), with no krill-specific harness
// code. See ../ARCHITECTURE.md "The MCP spec surface" and "The
// design-session MCP surface" for the two-front-door design this mirrors
// from audience_score_system/mcp and whagent_net/mcp.
//
// `mcp` mounts four pre-filtered tool surfaces, each on its own
// *mcp.Server and its own mount point (server/transport.go's
// specMountPath, designMountPath, workMountPath, and opsMountPath): the
// FR5-FR8 read-only spec surface at /mcp/spec (unchanged since M1), the
// design-scoped write/read surface at /mcp/design -- design-session
// authoring, milestone status, and delivery -- the work-axis task-
// lifecycle surface at /mcp/work (create_task, declare_task_dependencies,
// claim_task, heartbeat_task, complete_task, record_note,
// transition_note_lifecycle; init_session, get_task, list_tasks, and
// abandon_task also mount here, see below), and, as of M5 (issue #2867),
// the Swarm Operator-only surface at /mcp/ops, mounted but with no tool
// registered yet (the rest of M5 registers onto it). Both front doors
// (auth/human, whagent-net/agent) apply to all four mounts identically.
//
// workMountPath is a deliberate split from designMountPath (originally
// the work axis rode on /mcp/design entirely): the krill-design and
// krill-work Claude Code plugins each need a distinct write surface --
// krill-design's producer/architect/reviewer/stakeholder personas never
// touch a task's lifecycle, and krill-work's planner/worker/validator
// personas have no business authoring design sessions or milestones --
// so each plugin's own .mcp.json now points at spec+design or spec+work
// respectively, instead of both plugins pointing at both. init_session
// (every work-axis write tool needs a krill_session_id, and a
// krill-work-only caller has no other MCP-reachable way to mint one),
// get_task (ungated read, NFR6), list_tasks (ungated discovery), and
// abandon_task (PersonaAgent-gated cleanup) are registered on BOTH
// designMountPath and workMountPath: krill-design keeps ad hoc task
// discovery/inspection/unwinding even though no design persona's
// instructions call any of them today.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/whagent"
)

// config holds `mcp`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// MCPAddr is the address this binary's streamable-HTTP MCP surface
	// listens on.
	MCPAddr string

	// DatabaseURL is PG_DATABASE_URL -- the same pool //krill/slice's
	// Querier reads from (empty defers to libs/go/db.NewPool's own
	// PG_DATABASE_URL fallback).
	DatabaseURL string

	// MCPPublicURL is this instance's own externally reachable URL
	// (KRILL_MCP_PUBLIC_URL) -- passed as
	// server.ResourceMetadataConfig.Resource and as the audience every
	// whagent Claim this instance verifies must carry.
	MCPPublicURL string

	// OAuthIssuer is the auth front door's OAuth2 authorization
	// server's issuer identifier (KRILL_MCP_OAUTH_ISSUER) -- the
	// authorization_servers entry this instance's protected-resource
	// metadata advertises. Left unset skips serving RFC 9728 metadata
	// entirely (server.ResourceMetadataConfig.enabled).
	OAuthIssuer string

	// WhagentJWKSURL and WhagentIssuer (KRILL_MCP_WHAGENT_JWKS_URL /
	// KRILL_MCP_WHAGENT_ISSUER) are whagent-net's own JWKS endpoint and
	// issuer identifier -- both required to enable the agent front door
	// (server.WhagentAuthConfig). Left unset, `mcp` mounts only the
	// auth door (server.NewHTTPHandler), same as
	// audience_score_system/mcp's own pre-FR12(a) fallback.
	WhagentJWKSURL string
	WhagentIssuer  string
}

func loadConfig() config {
	return config{
		MCPAddr:        getEnv("KRILL_MCP_ADDR", ":8080"),
		DatabaseURL:    os.Getenv("PG_DATABASE_URL"),
		MCPPublicURL:   os.Getenv("KRILL_MCP_PUBLIC_URL"),
		OAuthIssuer:    os.Getenv("KRILL_MCP_OAUTH_ISSUER"),
		WhagentJWKSURL: os.Getenv("KRILL_MCP_WHAGENT_JWKS_URL"),
		WhagentIssuer:  os.Getenv("KRILL_MCP_WHAGENT_ISSUER"),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := loadConfig()

	logging.Configure(logging.Config{
		ServiceName:   "krill-mcp",
		Domain:        "krill",
		Level:         slog.LevelInfo,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	entities := store.New(pool)
	sessions := store.NewSessionStore(pool)
	querier := slice.NewQuerier(entities)
	assembler := work.NewAssembler(entities.Tasks(), querier)

	// Four *mcp.Server instances, one per mount (server/transport.go's
	// specMountPath, designMountPath, workMountPath, and opsMountPath) --
	// registering a tool is a per-server operation (mcp.AddTool), so the
	// only way to guarantee a write tool can never end up reachable from
	// specMountPath is to never register it on the same *mcp.Server that
	// backs it. tools.RegisterAll (FR5-FR8, read-only) is unchanged;
	// tools.RegisterInitSession (the krill_session_id minting tool, issue
	// #2827 -- MUST register before every other designReg call below,
	// since every one of them requires a session id this tool is the only
	// MCP-reachable way to obtain), tools.RegisterEntityCreateAll
	// (create_product/create_feature_set/create_load_bearing_decision/
	// create_feature/create_requirement, the non-mediated spec entity
	// creation tools that previously existed only over HTTP -- plus
	// create_persona/create_non_goal), tools.RegisterAmendAll,
	// tools.RegisterListProducts, tools.RegisterListPersonas/
	// RegisterListNonGoals, tools.RegisterDesignAll (issue #2547),
	// tools.RegisterMilestoneAll (milestone authoring, issue #2683),
	// tools.RegisterMilestoneStatusAll (status history, issue #2685),
	// tools.RegisterDeliveryShipmentAll (per-item shipment, issue #2686),
	// tools.RegisterRecutAll (delivery-axis re-cut plus the backlog
	// bucket, issue #2687), and tools.RegisterAbandonAll (the composed
	// abandon verb, issue #2688) all mount on designReg only --
	// create_milestone/set_fr_budget/add_delivers/add_must_not_foreclose/
	// add_deferral/set_milestone_status/mark_delivered_item_shipped/
	// move_delivery_scope/abandon_milestone all need the same
	// krill-session-derived LB4 subject pair every write tool on that
	// mount already resolves.
	//
	// The work axis's own task-lifecycle tools mount on workReg instead,
	// not designReg (see main.go's package doc comment for why):
	// tools.RegisterGetTaskPayload (the work-axis by-task-id fetch tool,
	// issue #2721, FR4/FR10 -- ungated read, NFR6), tools.RegisterListTasks
	// (ungated per-milestone/milepebble task discovery, feeding get_task
	// its ids), and tools.RegisterAbandonTask (the work-axis abandon tool,
	// issue #2726, FR9 -- distinct from tools.RegisterAbandonAll's
	// delivery-axis abandon_milestone above) also mount on designReg, so a
	// krill-design caller retains ad hoc task discovery/read/cleanup
	// access.
	specSrv := server.New()
	specReg := server.NewRegistry(specSrv)
	tools.RegisterAll(specReg, querier)

	designSrv := server.New()
	designReg := server.NewRegistry(designSrv)
	tools.RegisterInitSession(designReg, sessions, entities.Scopes())
	tools.RegisterEntityCreateAll(designReg, sessions, entities.Products(), entities.FeatureSets(), entities.Decisions(), entities.Personas(), entities.NonGoals(), entities.Features(), entities.Requirements())
	// amend_requirement/amend_load_bearing_decision: SCD2 corrections, the MCP twin of POST /{requirements,load-bearing-decisions}/{id}/amend.
	tools.RegisterAmendAll(designReg, sessions, entities.Amend())
	// list_products: ungated Product discovery, the entry point for every get_*_slice product_id.
	tools.RegisterListProducts(designReg, entities.Products())
	// list_personas/list_non_goals: ungated discovery for the two entity kinds create_persona/create_non_goal mint, with no other MCP-reachable read path (not part of the slice.Document either).
	tools.RegisterListPersonas(designReg, entities.Personas())
	tools.RegisterListNonGoals(designReg, entities.NonGoals())
	tools.RegisterDesignAll(designReg, entities, sessions, querier)
	tools.RegisterMilestoneAll(designReg, sessions, entities.MilestoneAuthoring(), entities.Products(), querier)
	tools.RegisterMilestoneStatusAll(designReg, sessions, entities.MilestoneStatus())
	tools.RegisterDeliveryShipmentAll(designReg, sessions, entities.DeliveryShipments(), entities.MilestoneStatus(), querier)
	tools.RegisterRecutAll(designReg, sessions, entities.Recut(), querier)
	tools.RegisterAbandonAll(designReg, sessions, entities.Abandon())
	tools.RegisterGetTaskPayload(designReg, entities.Tasks(), assembler)
	// list_tasks: ungated per-milestone/milepebble task discovery, feeding get_task its ids -- also registered on workReg below, the same shared-mount exception get_task/abandon_task get.
	tools.RegisterListTasks(designReg, entities.Tasks())
	tools.RegisterAbandonTask(designReg, sessions, entities.Tasks(), assembler)
	// list_entity_notes: ungated read-back of record_note's spec-axis entity notes.
	tools.RegisterListEntityNotes(designReg, handlers.NoteEntityScopes{
		Products:     entities.Products(),
		FeatureSets:  entities.FeatureSets(),
		Features:     entities.Features(),
		Requirements: entities.Requirements(),
		Decisions:    entities.Decisions(),
	}, entities.Tasks())

	// workReg is the work axis's own mount (server/transport.go's
	// workMountPath, /mcp/work): create_task (issue #2719, FR1),
	// declare_task_dependencies (issue #2720, FR2), claim_task (issue
	// #2722, FR3/FR5), heartbeat_task (issue #2723, FR6), complete_task
	// (issue #2725, FR8), record_note and transition_note_lifecycle
	// (issue #2727/#2874, FR11/FR12) live ONLY here -- the krill-work
	// plugin's manifest points at this mount instead of designMountPath,
	// so it never gains the design-session/milestone/delivery write
	// surface above. get_task, list_tasks, and abandon_task are
	// registered here too (see designReg above), the deliberate exception
	// to "a tool lives on exactly one mount." tools.RegisterInitSession
	// also registers here (as well as designReg): every work-axis write
	// tool requires a krill_session_id, and a krill-work-only caller (its
	// manifest no longer includes designMountPath) would otherwise have no
	// MCP-reachable way to mint one at all. For the same reason the six
	// ungated milestone/product discovery reads dual-mount here (issue
	// #3028): a krill-work persona holding a milestone or task id still
	// needs to resolve the product and read the milestone it belongs to,
	// and with only the task-lifecycle verbs above it could not do either.
	// These are reads -- no work-axis write is reachable from this mount
	// that designReg does not already carry.
	workSrv := server.New()
	workReg := server.NewRegistry(workSrv)
	// RegisterWorkAll (mcp/tools/work_mount.go) is the single call site for
	// this mount's tool set, so work_mount_registration_test.go can pin the
	// exact set rather than re-deriving it here.
	tools.RegisterWorkAll(workReg, entities, sessions, assembler, querier)

	// opsSrv/opsReg is M5's operator surface (issue #2867, /mcp/ops):
	// its own *mcp.Server so an operator verb or console query
	// (registered by later M5 tasks, via server.RegisterOpsRead/
	// RegisterOpsWrite -- registry.go) can never end up reachable from
	// specMountPath, designMountPath, or workMountPath, the same isolation
	// specSrv/designSrv/workSrv give each other above.
	//
	// tools.RegisterListClaimedTasks (issue #2869, FR4) was the first
	// tool registered here: list_claimed_tasks, PersonaSwarmOperator only
	// (enforced by RegisterOpsRead/the mount itself, not a per-tool
	// allow-list). tools.RegisterCancelTask (issue #2873, FR7) and
	// tools.RegisterListCancelledTasks (issue #2873, FR10) are the next
	// two: cancel_task (write) and list_cancelled_tasks (read), the same
	// PersonaSwarmOperator-only posture. tools.RegisterListOpenNotes (issue
	// #2874, FR12) mounts the same way, just below. tools.RegisterReleaseTask/
	// RegisterEscalateTask (issue #2872, FR8/FR9) are the next two:
	// release_task and escalate_task (both write), the same
	// PersonaSwarmOperator-only posture.
	opsSrv := server.New()
	opsReg := server.NewRegistry(opsSrv)
	tools.RegisterListClaimedTasks(opsReg, entities.Tasks())
	tools.RegisterCancelTask(opsReg, sessions, entities.Tasks(), assembler)
	tools.RegisterListCancelledTasks(opsReg, entities.Tasks())

	// tools.RegisterListOpenNotes (issue #2874, FR12): list_open_notes,
	// PersonaSwarmOperator only (RegisterOpsRead/the mount itself).
	tools.RegisterListOpenNotes(opsReg, entities.Tasks())

	// tools.RegisterReleaseTask/RegisterEscalateTask (issue #2872, FR8/FR9):
	// release_task and escalate_task, PersonaSwarmOperator only
	// (RegisterOpsWrite/the mount itself).
	tools.RegisterReleaseTask(opsReg, sessions, entities.Tasks(), assembler)
	tools.RegisterEscalateTask(opsReg, sessions, entities.Tasks(), assembler)

	// tools.RegisterListEscalatedTasks (issue #2875, FR5): list_escalated_tasks,
	// this milestone's headline console query, PersonaSwarmOperator only
	// (RegisterOpsRead/the mount itself).
	tools.RegisterListEscalatedTasks(opsReg, entities.Tasks())

	// tools.RegisterRequeueTask (issue #2876, FR6): requeue_task, the
	// recover half of the recover-or-terminate pair cancel_task is the
	// other half of, PersonaSwarmOperator only (RegisterOpsWrite/the
	// mount itself).
	tools.RegisterRequeueTask(opsReg, sessions, entities.Tasks(), assembler)

	// The auth (human) front door's CredentialStore preflights the
	// consuming domain's credential table at boot -- exactly like
	// audience_score_system/mcp/main.go's own NewCredentialStore call.
	// krill has not yet shipped that migration (no later M1 task numbers
	// one in ../ARCHITECTURE.md's migration table as of this task) --
	// until it does, this degrades to an always-reject store rather than
	// a fatal boot error, mirroring whagent_net/mcp/main.go's
	// initializeAuthDeps degrade-and-log convention for every other
	// optional dependency: the agent front door below never depends on
	// this succeeding.
	credentials, err := auth.NewCredentialStore(ctx, auth.StoreConfig{Pool: pool})
	if err != nil {
		logger.Warn("auth credential store unavailable; the auth (human) front door will reject every call until its migration is applied", "error", err)
		credentials = rejectingCredentialStore{}
	}

	resourceMeta := server.ResourceMetadataConfig{
		Resource:            cfg.MCPPublicURL,
		AuthorizationServer: cfg.OAuthIssuer,
		ResourceName:        "krill MCP",
	}

	// Mount the agent (whagent-net) front door ALONGSIDE the auth one
	// -- never in place of it -- whenever it's configured (NFR1: "both
	// front doors at the same mount point, each env-gated"). Both
	// KRILL_MCP_WHAGENT_JWKS_URL and KRILL_MCP_WHAGENT_ISSUER unset falls
	// back to the auth-only handler, exactly mirroring
	// audience_score_system/mcp/main.go's own FR12(a) fallback.
	var handler http.Handler
	if cfg.WhagentJWKSURL != "" && cfg.WhagentIssuer != "" {
		whagentVerifier, err := whagent.NewVerifier(ctx, cfg.WhagentJWKSURL, cfg.WhagentIssuer)
		if err != nil {
			return fmt.Errorf("whagent verifier: %w", err)
		}
		specSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
		designSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
		workSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
		opsSrv.AddReceivingMiddleware(server.WhagentPersonaMiddleware())
		handler = server.NewDualAuthHTTPHandler(specSrv, designSrv, workSrv, opsSrv, credentials, server.WhagentAuthConfig{
			Verifier: whagentVerifier,
			Audience: cfg.MCPPublicURL,
		}, resourceMeta)
	} else {
		handler = server.NewHTTPHandler(specSrv, designSrv, workSrv, opsSrv, credentials, resourceMeta)
	}

	httpServer := newHTTPServer(cfg.MCPAddr, handler)

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.MCPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed { //nolint:errorlint // net/http documents this exact sentinel
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown signal received, draining in-flight requests")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(drainCtx); err != nil {
		logger.Warn("graceful shutdown did not complete cleanly", "error", err)
	}
	return nil
}

// newHTTPServer builds the http.Server `mcp` listens on, wrapped in the
// otelhttp handler that gives every streamable-HTTP request a server span.
//
// WriteTimeout is deliberately left unset. net/http applies it as an
// absolute deadline measured from when the request header was read, not
// as a per-write budget, so any non-zero value silently breaks the
// transport's long-lived `GET <mount>` listening stream (MCP §2.1.3):
// every server->client notification written after that deadline fails
// with i/o timeout while the handler keeps hanging, so the client sees a
// stream that accepts connections and then goes deaf. Stream lifetime
// belongs to the transport, not to this server.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:        addr,
		Handler:     otelhttp.NewHandler(handler, "krill-mcp"),
		ReadTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second,
	}
}

// rejectingCredentialStore is a auth.CredentialStore of last resort:
// every call fails with the same opaque "invalid or revoked credential"
// auth.TokenVerifier already produces for any other Verify failure, so
// a caller presenting an auth-shaped credential against a
// not-yet-migrated krill deployment gets a clean 401 instead of `mcp`
// panicking on a nil CredentialStore interface value.
type rejectingCredentialStore struct{}

func (rejectingCredentialStore) Mint(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, fmt.Errorf("auth: credential store not configured")
}

func (rejectingCredentialStore) Verify(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, fmt.Errorf("auth: credential store not configured")
}

func (rejectingCredentialStore) Revoke(context.Context, uuid.UUID, string) error {
	return fmt.Errorf("auth: credential store not configured")
}

func (rejectingCredentialStore) List(context.Context, string) ([]auth.Credential, error) {
	return nil, fmt.Errorf("auth: credential store not configured")
}

var _ auth.CredentialStore = rejectingCredentialStore{}
