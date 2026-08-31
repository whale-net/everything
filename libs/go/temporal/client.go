package temporal

import (
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"
)

// NewClient dials the Temporal frontend described by cfg. Dial connects
// eagerly, so a non-nil error here means the server was unreachable, not
// merely misconfigured, at call time.
//
// If logger is nil, NewLogger("temporal-client") is used.
//
// Workflow and activity execution is traced via the OTel contrib tracing
// interceptor against the global tracer provider, so it's a no-op unless
// the process has configured tracing (e.g. via libs/go/logging's
// logging.Configure with EnableTracing: true).
func NewClient(cfg Config, logger log.Logger) (client.Client, error) {
	if logger == nil {
		logger = NewLogger("temporal-client")
	}

	tracingInterceptor, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{})
	if err != nil {
		return nil, fmt.Errorf("build temporal tracing interceptor: %w", err)
	}

	c, err := client.Dial(client.Options{
		HostPort:     cfg.HostPort,
		Namespace:    cfg.Namespace,
		Logger:       logger,
		Interceptors: []interceptor.ClientInterceptor{tracingInterceptor},
	})
	if err != nil {
		return nil, fmt.Errorf("dial temporal at %s (namespace %s): %w", cfg.HostPort, cfg.Namespace, err)
	}
	return c, nil
}
