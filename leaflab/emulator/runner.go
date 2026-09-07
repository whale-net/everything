// runner.go owns per-board goroutine lifecycle: connecting to the broker,
// publishing the online status + retained manifest, and driving the
// per-sensor reading loop on a single ticker (one goroutine per board, not
// per sensor -- see NFR5). See leaflab/MQTT.md for the wire shape this must
// match exactly.
package main

import (
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
)

// tickGranularity is the reading loop's single ticker period. Per-sensor
// due times are checked against it rather than spawning one ticker per
// sensor (NFR5): fine enough that a 100ms-resolution poll interval is
// still honored reasonably, coarse enough not to busy-loop.
const tickGranularity = 100 * time.Millisecond

// clockTicker abstracts time.Ticker so tests can drive the reading loop
// deterministically instead of sleeping in real time.
type clockTicker interface {
	C() <-chan time.Time
	Stop()
}

// runnerClock abstracts time.Now/time.NewTicker for the same reason.
type runnerClock interface {
	Now() time.Time
	NewTicker(d time.Duration) clockTicker
}

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()               { r.t.Stop() }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTicker(d time.Duration) clockTicker {
	return &realTicker{t: time.NewTicker(d)}
}

// RunnerDeps bundles the per-board dependencies NewRunner needs beyond the
// Board and Transport, so the constructor's argument list doesn't grow
// every time the runner needs one more knob.
type RunnerDeps struct {
	// DefaultInterval is the PUBLISH_INTERVAL fallback used for any sensor
	// without its own PollIntervalMS override.
	DefaultInterval time.Duration
	// Rand seeds this board's synthetic value walkers (valuegen.go). Only
	// ever touched by this Runner's own reading-loop goroutine, so sharing
	// one *rand.Rand across a board's sensors is safe even though
	// valuegen.go warns against sharing one across goroutines.
	Rand *rand.Rand
	// Logger receives lifecycle/connect/publish-failure logs. Defaults to
	// slog.Default() if nil.
	Logger *slog.Logger
}

// Runner drives one simulated board's MQTT lifecycle: connect, publish
// status/manifest, run the reading loop, and clean shutdown. One Runner
// per Board; at most one goroutine per Runner.
type Runner struct {
	board     Board
	transport Transport
	client    mqtt.Client // set only by NewPahoRunner; nil when transport is a test double
	deps      RunnerDeps
	clock     runnerClock

	mu          sync.Mutex
	loopStarted bool
	stopped     bool
	stopCh      chan struct{}
	doneCh      chan struct{}
	boardStart  time.Time
	walkers     map[string]*Walker
	nextDue     map[string]time.Time
	// configVersion is the last DeviceConfig version successfully applied
	// (config_apply.go, FR18). Starts at 0 -- a fresh board has never had a
	// config applied, and since versions are monotonic and 1-based, version
	// 0 is never itself a legal accepted push. In-memory only: the
	// emulator's equivalent of "survives reboot" is a fresh device_id on
	// `tilt down`/`tilt up` (see deriveDeviceID), not NVS persistence.
	configVersion uint64
}

// NewRunner constructs a Runner for board, publishing through transport.
// transport is typically a fakeTransport in tests, or a pahoTransport built
// by NewPahoRunner for a real broker connection.
func NewRunner(board Board, transport Transport, deps RunnerDeps) *Runner {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Rand == nil {
		deps.Rand = rand.New(rand.NewSource(seedFor(0, board.DeviceID)))
	}
	if deps.DefaultInterval <= 0 {
		deps.DefaultInterval = 5 * time.Second
	}
	return &Runner{
		board:     board,
		transport: transport,
		deps:      deps,
		clock:     realClock{},
	}
}

// NewPahoRunner constructs a Runner wired to a real, dedicated MQTT
// connection for board: ClientID = device_id, LWT publishing "offline"
// retained on unexpected disconnect (the broker does this, never the
// emulator itself -- FR16), auto-reconnect, and an OnConnect handler that
// re-runs the full connect sequence on every connect, including
// reconnects. Call Start (or Connect) to actually dial the broker.
func NewPahoRunner(board Board, cfg *Config, deps RunnerDeps) *Runner {
	r := NewRunner(board, nil, deps)

	statusTopic := statusTopic(board.DeviceID)
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBrokerURL).
		SetUsername(cfg.MQTTUsername).
		SetPassword(cfg.MQTTPassword).
		SetClientID(board.DeviceID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetWill(statusTopic, "offline", 1, true).
		SetOnConnectHandler(func(_ mqtt.Client) {
			r.onConnect()
		})

	client := mqtt.NewClient(opts)
	r.client = client
	r.transport = newPahoTransport(client)
	return r
}

// seedFor derives a per-board RNG seed from a base seed (0 means seed from
// wall-clock time, matching RANDOM_SEED=0's documented meaning) and the
// board's device_id, so different boards under the same non-zero base seed
// still get distinct-but-deterministic walks.
func seedFor(base int64, deviceID string) int64 {
	if base == 0 {
		return time.Now().UnixNano()
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(deviceID))
	return base ^ int64(h.Sum64())
}

// intervalFor resolves the effective per-sensor publish interval: the
// sensor's own PollIntervalMS override when non-zero, otherwise
// defaultInterval (the board default from PUBLISH_INTERVAL).
func intervalFor(defaultInterval time.Duration, s Sensor) time.Duration {
	if s.PollIntervalMS != 0 {
		return time.Duration(s.PollIntervalMS) * time.Millisecond
	}
	return defaultInterval
}

func statusTopic(deviceID string) string   { return fmt.Sprintf("leaflab/%s/status", deviceID) }
func manifestTopic(deviceID string) string { return fmt.Sprintf("leaflab/%s/manifest", deviceID) }
func sensorTopic(deviceID, name string) string {
	return fmt.Sprintf("leaflab/%s/sensor/%s", deviceID, name)
}

// Start begins the board's connect sequence and reading loop. For a Runner
// built by NewPahoRunner this dials the broker; the connect sequence itself
// then runs via the paho OnConnect callback (on this call and on every
// later reconnect). For a Runner built directly with NewRunner (no paho
// client -- the test-double path) it runs the connect sequence immediately,
// once, synchronously.
func (r *Runner) Start() error {
	if r.client != nil {
		token := r.client.Connect()
		token.Wait()
		if err := token.Error(); err != nil {
			r.deps.Logger.Error("board terminal connect failure", "device_id", r.board.DeviceID, "error", err)
			return err
		}
		return nil
	}
	r.onConnect()
	return nil
}

// onConnect runs the connect sequence (leaflab/MQTT.md, this issue's
// Implementation section): publish "online" retained, publish the retained
// DeviceManifest, then start (first connect) or leave running (reconnect)
// the reading loop. Invoked directly by Start for the test-double path, and
// by the paho OnConnect handler -- on every connect, including
// reconnects -- for the real broker path.
func (r *Runner) onConnect() {
	// Snapshot board under the lock -- config_apply.go's handleConfig can
	// mutate r.board.Sensors concurrently on paho's message-handler
	// goroutine, so this read (like every other read/write of sensor
	// state) must not touch r.board directly outside r.mu.
	r.mu.Lock()
	reconnect := r.loopStarted
	board := r.board
	r.mu.Unlock()

	if err := r.transport.Publish(statusTopic(board.DeviceID), 1, true, []byte("online")); err != nil {
		r.deps.Logger.Warn("retried online status publish", "device_id", board.DeviceID, "error", err)
	}

	manifest := r.publishManifest(board)

	// Re-subscribe on every connect, including reconnects -- FR18's
	// reconnect-convergence requirement -- after the manifest publish, per
	// this issue's Implementation section.
	r.subscribeConfig()

	r.ensureLoopStarted()

	if reconnect {
		r.deps.Logger.Warn("board reconnected", "device_id", board.DeviceID)
	} else {
		r.deps.Logger.Info("board connected", "device_id", board.DeviceID, "sensor_count", len(manifest.GetSensors()))
	}
}

// publishManifest marshals and publishes the retained DeviceManifest for
// board (a snapshot taken by the caller under r.mu, then used after the
// lock is released for the network call). Shared by onConnect and
// config_apply.go's handleConfig -- the config-apply re-publish step -- so
// the wire encoding and retained/QoS flags can't drift between the two call
// sites.
func (r *Runner) publishManifest(board Board) *firmwarepb.DeviceManifest {
	manifest := BuildManifest(board)
	payload, err := proto.Marshal(manifest)
	if err != nil {
		r.deps.Logger.Error("failed to marshal device manifest", "device_id", board.DeviceID, "error", err)
		return manifest
	}
	if err := r.transport.Publish(manifestTopic(board.DeviceID), 1, true, payload); err != nil {
		r.deps.Logger.Warn("retried manifest publish", "device_id", board.DeviceID, "error", err)
	}
	return manifest
}

// ensureLoopStarted starts the per-sensor reading loop goroutine the first
// time it's called for this Runner; later calls (reconnects) are a no-op
// since the loop is already running and simply resumes publishing once the
// transport is reconnected.
func (r *Runner) ensureLoopStarted() {
	r.mu.Lock()
	if r.loopStarted || r.stopped {
		r.mu.Unlock()
		return
	}
	r.loopStarted = true
	r.boardStart = r.clock.Now()
	r.stopCh = make(chan struct{})
	r.doneCh = make(chan struct{})
	r.walkers = make(map[string]*Walker, len(r.board.Sensors))
	r.nextDue = make(map[string]time.Time, len(r.board.Sensors))
	for _, s := range r.board.Sensors {
		if !s.Enabled {
			continue
		}
		r.walkers[s.Name] = NewWalker(s.SensorType, r.deps.Rand)
		r.nextDue[s.Name] = r.boardStart.Add(intervalFor(r.deps.DefaultInterval, s))
	}
	stopCh := r.stopCh
	r.mu.Unlock()

	go r.loop(stopCh)
}

// reconcileLoopStateLocked keeps r.walkers/r.nextDue in sync after a
// config_apply.go push swaps r.board.Sensors from old to new (same length
// and order -- applyConfig only ever mutates fields in place). Must be
// called with r.mu held.
//
// Deliberately never calls NewWalker: a newly-enabled sensor's walker is
// created lazily by publishDueReadings on the loop's own goroutine instead,
// so deps.Rand (shared, not concurrency-safe -- see RunnerDeps and
// valuegen.go) is only ever touched from the reading-loop goroutine, never
// from handleConfig's.
func (r *Runner) reconcileLoopStateLocked(old, newSensors []Sensor) {
	for i, ns := range newSensors {
		os := old[i]

		if os.Name != ns.Name {
			// Move the existing walker/due-time to the new key so a rename
			// doesn't reset the sensor's value walk or scheduling -- only
			// its topic changes.
			if w, ok := r.walkers[os.Name]; ok {
				r.walkers[ns.Name] = w
				delete(r.walkers, os.Name)
			}
			if due, ok := r.nextDue[os.Name]; ok {
				r.nextDue[ns.Name] = due
				delete(r.nextDue, os.Name)
			}
		}

		if !ns.Enabled {
			delete(r.walkers, ns.Name)
			delete(r.nextDue, ns.Name)
		}
	}
}

// loop drives the single ticker for this board, checking each enabled
// sensor's next-due time on every tick rather than spawning one goroutine
// per sensor (NFR5).
func (r *Runner) loop(stopCh chan struct{}) {
	ticker := r.clock.NewTicker(tickGranularity)
	defer ticker.Stop()
	defer close(r.doneCh)

	for {
		select {
		case <-stopCh:
			return
		case now := <-ticker.C():
			r.publishDueReadings(now)
		}
	}
}

// publishDueReadings publishes a SensorReading for every enabled sensor
// whose next-due time has arrived, and reschedules it from its previous due
// time (not from now) so a sensor's average rate holds even if a tick is
// checked slightly late.
func (r *Runner) publishDueReadings(now time.Time) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	var due []Sensor
	for _, s := range r.board.Sensors {
		if !s.Enabled {
			continue
		}
		next, ok := r.nextDue[s.Name]
		if !ok {
			// Enabled but not yet tracked: a config_apply.go push just
			// enabled this sensor. Create its walker here, on the loop's
			// own goroutine -- deps.Rand must never be touched from
			// handleConfig's goroutine (valuegen.go) -- and give it a
			// fresh due time exactly like ensureLoopStarted does at
			// startup.
			r.walkers[s.Name] = NewWalker(s.SensorType, r.deps.Rand)
			next = now.Add(intervalFor(r.deps.DefaultInterval, s))
			r.nextDue[s.Name] = next
		}
		if now.Before(next) {
			continue
		}
		r.nextDue[s.Name] = next.Add(intervalFor(r.deps.DefaultInterval, s))
		due = append(due, s)
	}
	boardStart := r.boardStart
	r.mu.Unlock()

	for _, s := range due {
		r.publishReading(s, now, boardStart)
	}
}

func (r *Runner) publishReading(s Sensor, now, boardStart time.Time) {
	w := r.walkers[s.Name]
	reading := &firmwarepb.SensorReading{
		Value:    w.Next(),
		UptimeMs: uint32(now.Sub(boardStart).Milliseconds()),
	}
	payload, err := proto.Marshal(reading)
	if err != nil {
		r.deps.Logger.Error("failed to marshal sensor reading", "device_id", r.board.DeviceID, "sensor", s.Name, "error", err)
		return
	}
	if err := r.transport.Publish(sensorTopic(r.board.DeviceID, s.Name), 0, false, payload); err != nil {
		r.deps.Logger.Warn("retried sensor reading publish", "device_id", r.board.DeviceID, "sensor", s.Name, "error", err)
	}
}

// Stop performs a clean shutdown: stop the reading loop, publish "offline"
// retained explicitly (a clean stop must look offline immediately, not wait
// for the broker's keepalive to fire the LWT), then Disconnect(250). Safe
// to call once; a second call is a no-op.
func (r *Runner) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	started := r.loopStarted
	stopCh := r.stopCh
	doneCh := r.doneCh
	r.mu.Unlock()

	if started {
		close(stopCh)
		<-doneCh
	}

	if err := r.transport.Publish(statusTopic(r.board.DeviceID), 1, true, []byte("offline")); err != nil {
		r.deps.Logger.Warn("retried offline status publish on shutdown", "device_id", r.board.DeviceID, "error", err)
	}
	r.transport.Disconnect()
	r.deps.Logger.Info("board clean shutdown", "device_id", r.board.DeviceID)
}
