package temporal

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// NewWorker builds a Temporal worker.Worker bound to the given client and
// task queue. It does not register any workflows or activities and does not
// start the worker — callers register their own workflows/activities and
// call Run or Start.
//
// The worker SDK has no per-worker Logger option; it uses the Logger set on
// the client (see NewClient), so worker logs already route through
// libs/go/logging as long as the client was built with this package.
func NewWorker(c client.Client, taskQueue string, opts worker.Options) worker.Worker {
	return worker.New(c, taskQueue, opts)
}
