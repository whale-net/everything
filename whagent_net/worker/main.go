package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// defaultPriceTablePath is WHAGENT_PRICE_TABLE_PATH's default (ENV.md,
// issue #2221): where //whagent_net/worker's BUILD.bazel (pkg_tar +
// additional_tars) bakes the checked-in //whagent_net/config:prices.json
// into the worker image.
const defaultPriceTablePath = "/etc/whagent-net/prices.json"

func main() {
	if err := run(); err != nil {
		logging.Get("main").Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logging.Configure(logging.Config{
		ServiceName:   "whagent-net-worker",
		Domain:        "whagent-net",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck
	logger := logging.Get("main")

	// Postgres, shared with api (ARCHITECTURE.md "Service boundary vs.
	// package boundary": worker imports whagent_net/session directly, no
	// RPC hop).
	databaseURL := getEnv("PG_DATABASE_URL", "")
	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	logger.Info("database connected")

	pub := initializePublisher(ctx, logger)
	store := session.New(pool, pub)

	// OpenRouter client (issue #2112) -- CallModel (activities.go) calls
	// this on every turn. Constructed unconditionally: OPENROUTER_API_KEY
	// is documented required in ENV.md, but startup itself does not
	// validate it -- an empty/invalid key surfaces as a CallModel activity
	// failure on the first turn, not a startup error, matching this
	// worker's other soft-fail-at-startup dependencies (initializePublisher
	// below).
	llmClient := llm.NewClient(os.Getenv("OPENROUTER_API_KEY"), getEnv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"))

	// Price table (LB6, ENV.md's WHAGENT_PRICE_TABLE_PATH) -- CommitTurn's
	// resolveCost (activities.go) falls back to this only when the
	// provider omits cost in its response (uncommon with
	// usage.include=true, but not guaranteed for every model/provider
	// combination on OpenRouter). Defaults to defaultPriceTablePath, which
	// //whagent_net/worker's BUILD.bazel bakes into the image
	// (config/prices.json via pkg_tar/additional_tars, issue #2221) --
	// still overridable to a mounted/ConfigMap path, since LoadPriceTable
	// re-reads it on every call regardless of where it points.
	prices, err := llm.LoadPriceTable(getEnv("WHAGENT_PRICE_TABLE_PATH", defaultPriceTablePath))
	if err != nil {
		return fmt.Errorf("load price table: %w", err)
	}

	// Persona issuer + tool dispatcher (issue #2118/#2121; ARCHITECTURE.md
	// "Identity and auth chaining" § "Issuance mechanism"): worker mints
	// its own persona credentials in-process, from the identical signing-
	// key configuration api reads (ENV.md "Persona claim issuance") --
	// there is no RPC hop to api for this. Fails startup loudly the same
	// way api's own persona.LoadKeySet call does, never falling back to
	// an unsigned mode -- see persona.LoadKeySet's doc comment.
	keySet, err := persona.LoadKeySet(persona.KeySetEnvConfig{
		Issuer:              getEnv("WHAGENT_ISSUER", "whagent-net"),
		ActiveKeyID:         os.Getenv("WHAGENT_SIGNING_KEY_ID"),
		ActivePrivateKeyPEM: os.Getenv("WHAGENT_SIGNING_KEY"),
	})
	if err != nil {
		return fmt.Errorf("persona: %w", err)
	}
	issuer := persona.NewIssuer(keySet.ActiveSigner())
	dispatcher := &tools.Dispatcher{Issuer: issuer, Ledger: store.Idempotency()}

	// Temporal client + worker, via libs/go/temporal (NewWorker bootstrap).
	temporalCfg := temporallib.ConfigFromEnv()
	if temporalCfg.TaskQueue == "" {
		temporalCfg.TaskQueue = TaskQueue
	}
	logger.Info("connecting to temporal", "host_port", temporalCfg.HostPort, "namespace", temporalCfg.Namespace, "task_queue", temporalCfg.TaskQueue)
	temporalClient, err := temporallib.NewClient(temporalCfg, temporallib.NewLogger("whagent-net-worker"))
	if err != nil {
		return fmt.Errorf("connect to temporal: %w", err)
	}
	defer temporalClient.Close()

	w := temporallib.NewWorker(temporalClient, temporalCfg.TaskQueue, worker.Options{})
	w.RegisterWorkflow(SessionWorkflow)

	acts := &Activities{Store: store, LLM: llmClient, Prices: prices, Dispatcher: dispatcher}
	w.RegisterActivityWithOptions(acts.ResolveAgentDefinition, activity.RegisterOptions{Name: ActivityResolveAgentDefinition})
	w.RegisterActivityWithOptions(acts.BuildContext, activity.RegisterOptions{Name: ActivityBuildContext})
	w.RegisterActivityWithOptions(acts.CallModel, activity.RegisterOptions{Name: ActivityCallModel})
	w.RegisterActivityWithOptions(acts.CommitTurn, activity.RegisterOptions{Name: ActivityCommitTurn})
	w.RegisterActivityWithOptions(acts.UpdateSessionStatus, activity.RegisterOptions{Name: ActivityUpdateSessionStatus})
	// SumCost/CommitTerminalEvent (issue #2119): registered now so the
	// worker binary exposes them from Scaffold phase onward, even though
	// processTurn does not call either yet -- Implementation phase wires
	// the ExecuteActivity calls into workflow.go under this task's
	// workflow.GetVersion gate (caps.go/activities.go doc comments).
	w.RegisterActivityWithOptions(acts.SumCost, activity.RegisterOptions{Name: ActivitySumCost})
	w.RegisterActivityWithOptions(acts.CommitTerminalEvent, activity.RegisterOptions{Name: ActivityCommitTerminalEvent})
	// ListToolDefinitions/DispatchTool (issue #2121): same "registered now,
	// wired into processTurn in Implementation phase" shape as SumCost/
	// CommitTerminalEvent above -- see activities.go's doc comments on
	// both.
	w.RegisterActivityWithOptions(acts.ListToolDefinitions, activity.RegisterOptions{Name: ActivityListToolDefinitions})
	w.RegisterActivityWithOptions(acts.DispatchTool, activity.RegisterOptions{Name: ActivityDispatchTool})

	done := make(chan error, 1)
	go func() {
		done <- w.Run(worker.InterruptCh())
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
		logger.Info("shutting down gracefully")
		cancel()
	case err := <-done:
		cancel()
		return err
	}
	return <-done
}

// initializePublisher constructs the events.PublisherInterface
// session.New's Transcript().Append publishes committed events onto (NFR2,
// see whagent_net/session/transcript.go). Construction is non-fatal, same
// as tools/app_registry/worker's initializePublisher: with RABBITMQ_URL
// unset or the broker unreachable at startup, this returns nil and Append
// silently skips the publish step.
func initializePublisher(ctx context.Context, logger *slog.Logger) events.PublisherInterface {
	brokerURL := getEnv("RABBITMQ_URL", "")
	if brokerURL == "" {
		logger.Info("RABBITMQ_URL not set; transcript events will not be published")
		return nil
	}

	conn, err := rmq.NewConnectionFromURL(brokerURL)
	if err != nil {
		logger.Warn("failed to connect to RabbitMQ for event publishing; transcript events will not be published", "error", err)
		return nil
	}

	pub, err := events.NewPublisher(conn)
	if err != nil {
		logger.Warn("failed to create event publisher; transcript events will not be published", "error", err)
		return nil
	}
	return pub
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
