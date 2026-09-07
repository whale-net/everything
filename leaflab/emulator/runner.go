// runner.go owns per-board goroutine lifecycle: connecting to the broker,
// publishing the online status + retained manifest, and driving the
// per-sensor reading loop on a single ticker (one goroutine per board, not
// per sensor -- see NFR5). Full connect-sequence, reconnect, and
// reading-loop logic lands in the Implementation phase; see leaflab/MQTT.md
// and this issue's Implementation section.
package main

// Runner drives one simulated board's MQTT lifecycle: connect, publish
// status/manifest, run the reading loop, and clean shutdown. One Runner
// per Board; at most one goroutine per Runner.
type Runner struct {
	board     Board
	transport Transport
}

// NewRunner constructs a Runner for board, publishing through transport.
func NewRunner(board Board, transport Transport) *Runner {
	return &Runner{board: board, transport: transport}
}

// Start begins the board's connect sequence and reading loop. Stub -- full
// implementation lands in the Implementation phase.
func (r *Runner) Start() {}

// Stop performs a clean shutdown: publish "offline" retained, disconnect,
// and stop the reading loop. Stub -- full implementation lands in the
// Implementation phase.
func (r *Runner) Stop() {}
