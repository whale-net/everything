package temporal

import (
	"go.temporal.io/sdk/log"

	"github.com/whale-net/everything/libs/go/logging"
)

// NewLogger returns a Temporal SDK log.Logger backed by this repo's
// libs/go/logging *slog.Logger, so Temporal client/worker logs go through
// the same structured pipeline (console/JSON output, OTLP export) as the
// rest of the service instead of a second logging stack.
//
// name identifies the logger the same way logging.Get(name) does (e.g. the
// package or component name); it is attached as the "logger" attribute on
// every log line.
func NewLogger(name string) log.Logger {
	return log.NewStructuredLogger(logging.Get(name))
}
