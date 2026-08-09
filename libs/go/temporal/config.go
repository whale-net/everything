// Package temporal provides shared Temporal SDK plumbing for Go services in
// the everything monorepo: env-driven client configuration, a client
// constructor, a worker bootstrap helper, and a logging bridge from
// libs/go/logging to the SDK's log.Logger interface.
//
// # Quick start
//
//	cfg := temporal.ConfigFromEnv()
//	c, err := temporal.NewClient(cfg, temporal.NewLogger("app-registry"))
//	if err != nil { ... }
//	defer c.Close()
//
//	w := temporal.NewWorker(c, cfg.TaskQueue, worker.Options{})
//	w.RegisterWorkflow(MyWorkflow)
//	w.RegisterActivity(MyActivity)
//	if err := w.Run(worker.InterruptCh()); err != nil { ... }
//
// # Environment variables
//
//   - TEMPORAL_HOST — host:port of the Temporal frontend service. Default
//     "localhost:7233". Named to match friendly_computing_machine's existing
//     TEMPORAL_HOST convention (see friendly_computing_machine/README.md),
//     even though the Python SDK there is configured independently.
//   - TEMPORAL_NAMESPACE — Temporal namespace. Default "default".
//   - TEMPORAL_TASK_QUEUE — default task queue name for workers built with
//     NewWorkerFromEnv. No default — callers of ConfigFromEnv that don't use
//     that helper are free to ignore it.
package temporal

import "os"

// Config holds Temporal client/worker configuration. Zero-value fields are
// filled with defaults by ConfigFromEnv; constructing a Config by hand is
// also supported for tests.
type Config struct {
	// HostPort is the Temporal frontend service address, e.g. "localhost:7233"
	// or "temporal-dev.app-registry-local-dev.svc.cluster.local:7233".
	HostPort string
	// Namespace is the Temporal namespace to operate in.
	Namespace string
	// TaskQueue is the default task queue for workers. Not used by NewClient;
	// consumers that build a worker read it from Config themselves.
	TaskQueue string
}

const (
	// DefaultHostPort is used when TEMPORAL_HOST is unset.
	DefaultHostPort = "localhost:7233"
	// DefaultNamespace is used when TEMPORAL_NAMESPACE is unset.
	DefaultNamespace = "default"
)

// ConfigFromEnv builds a Config from TEMPORAL_HOST, TEMPORAL_NAMESPACE, and
// TEMPORAL_TASK_QUEUE, applying defaults for the first two. TaskQueue has no
// default — an empty value means the caller must supply one explicitly
// wherever a task queue name is required.
func ConfigFromEnv() Config {
	return Config{
		HostPort:  envOr("TEMPORAL_HOST", DefaultHostPort),
		Namespace: envOr("TEMPORAL_NAMESPACE", DefaultNamespace),
		TaskQueue: os.Getenv("TEMPORAL_TASK_QUEUE"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
