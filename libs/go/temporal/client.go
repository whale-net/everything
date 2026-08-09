package temporal

import (
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
)

// NewClient dials the Temporal frontend described by cfg. Dial connects
// eagerly, so a non-nil error here means the server was unreachable, not
// merely misconfigured, at call time.
//
// If logger is nil, NewLogger("temporal-client") is used.
func NewClient(cfg Config, logger log.Logger) (client.Client, error) {
	if logger == nil {
		logger = NewLogger("temporal-client")
	}

	c, err := client.Dial(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
		Logger:    logger,
	})
	if err != nil {
		return nil, fmt.Errorf("dial temporal at %s (namespace %s): %w", cfg.HostPort, cfg.Namespace, err)
	}
	return c, nil
}
