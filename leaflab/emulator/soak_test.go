// soak_test.go is #1774's concurrency, resource, and shutdown soak
// coverage: NFR5 (the emulator carries the full scenario set concurrently
// on a developer laptop without excessive CPU/memory) and the
// sustained-session half of NFR6 (a safe, bounded default publish rate).
// Everything here runs against a Transport double and the injectable clock
// from #1769 -- no broker, no testcontainers -- so it stays a fast,
// hermetic unit test CI can run on every change rather than a manual
// benchmark. See this file's issue (#1774) for the full coverage list this
// pins.
//
// Race-sensitive assertions (TestSoak_ConcurrentConfigPushUnderLoad) are
// only meaningful run under the race detector:
//
//	bazel test --@rules_go//go/config:race //leaflab/emulator:emulator_test
//
// TestHandleConfig_RaceWithReadingLoop (config_apply_test.go, #1771) already
// proves the mutex is race-safe for a single board; this file's version
// proves the same thing holds when every board in the default scenario set
// is doing it at once, which is what NFR5 actually promises.
package main

import (
	"runtime"
	"sync"
	"testing"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// countingTransport is a Transport double that tracks only aggregate
// per-topic counts and the most recent payload per topic -- never a growing
// history of every publish, unlike fakeTransport (transport_test.go). The
// soak tests below drive tens of thousands of publishes; if the transport
// double itself accumulated every one, its own growth would swamp the
// signal TestSoak_NoUnboundedAllocationOverLongRun is trying to catch in
// the emulator's own code.
type countingTransport struct {
	mu           sync.Mutex
	counts       map[string]int64
	lastPayload  map[string][]byte
	handlers     map[string]func(topic string, payload []byte)
	disconnected bool
}

func newCountingTransport() *countingTransport {
	return &countingTransport{
		counts:      make(map[string]int64),
		lastPayload: make(map[string][]byte),
	}
}

func (c *countingTransport) Publish(topic string, _ byte, _ bool, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[topic]++
	c.lastPayload[topic] = payload
	return nil
}

func (c *countingTransport) Subscribe(topic string, _ byte, cb func(topic string, payload []byte)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handlers == nil {
		c.handlers = make(map[string]func(string, []byte))
	}
	c.handlers[topic] = cb
	return nil
}

func (c *countingTransport) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnected = true
}

// deliver invokes the handler subscribed for topic, simulating an incoming
// broker message -- mirrors fakeTransport.deliver (transport_test.go).
func (c *countingTransport) deliver(topic string, payload []byte) {
	c.mu.Lock()
	cb := c.handlers[topic]
	c.mu.Unlock()
	if cb == nil {
		panic("countingTransport: deliver to topic " + topic + " with no subscriber")
	}
	cb(topic, payload)
}

func (c *countingTransport) count(topic string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[topic]
}

func (c *countingTransport) last(topic string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastPayload[topic]
}

// soakRig bundles one board's running Runner with its test doubles, so the
// soak tests can iterate "every board" without repeating four-value tuples
// everywhere.
type soakRig struct {
	board     Board
	runner    *Runner
	transport *countingTransport
	clock     *fakeClock
}

// realScenarios loads the actual leaflab/scripts/scenarios/*.json files --
// the same fixtures push-config.sh and `tilt up` use -- so NFR5's "the
// existing scenario set" claim is checked against what a developer actually
// has on disk, not a synthetic stand-in.
func realScenarios(t *testing.T) map[string]*Scenario {
	t.Helper()
	scenarios, err := LoadScenarioDir(scenariosDir(t))
	require.NoError(t, err)
	require.NotEmpty(t, scenarios, "expected at least one scenario file in %s", scenariosDir(t))
	return scenarios
}

// buildAllBoards resolves the default EMULATOR_BOARDS spec (empty --
// one board per loaded scenario, NFR5's default load) against the real
// scenario set.
func buildAllBoards(t *testing.T) []Board {
	t.Helper()
	scenarios := realScenarios(t)
	boards, err := BuildBoards(&Config{EmulatorBoards: ""}, scenarios)
	require.NoError(t, err)
	return boards
}

// startSoakRigs builds and starts a Runner per board over a fresh
// countingTransport + fakeClock pair each (no broker -- see newTestRunner
// in runner_test.go for the same no-broker pattern this mirrors), waiting
// for every reading loop to register its ticker before returning.
func startSoakRigs(t *testing.T, boards []Board) []soakRig {
	t.Helper()
	rigs := make([]soakRig, len(boards))
	for i, b := range boards {
		transport := newCountingTransport()
		r := NewRunner(b, transport, testDeps())
		clock := newFakeClock(time.Unix(1_700_000_000, 0))
		r.clock = clock
		require.NoError(t, r.Start())
		waitForTicker(t, clock)
		rigs[i] = soakRig{board: b, runner: r, transport: transport, clock: clock}
	}
	return rigs
}

// stopSoakRigs stops every rig's Runner concurrently, mirroring main.go's
// own shutdown fan-out (run: `wg.Wait()` over `go r.Stop()` per runner) so
// the shutdown test exercises the same concurrency shape production uses.
func stopSoakRigs(rigs []soakRig) {
	var wg sync.WaitGroup
	for _, rg := range rigs {
		wg.Add(1)
		go func(r *Runner) {
			defer wg.Done()
			r.Stop()
		}(rg.runner)
	}
	wg.Wait()
}

// enabledSensorCount sums enabled sensors across boards.
func enabledSensorCount(boards []Board) int {
	n := 0
	for _, b := range boards {
		for _, s := range b.Sensors {
			if s.Enabled {
				n++
			}
		}
	}
	return n
}

// stableGoroutineCount reads runtime.NumGoroutine() until two consecutive
// reads agree, so a transient scheduler/GC-worker goroutine mid-transition
// doesn't make the goroutine-count assertions below flaky. This is test
// synchronization overhead only -- it never stands in for the injected
// clock, which is what governs simulated board/session time everywhere
// else in this file.
func stableGoroutineCount(t *testing.T) int {
	t.Helper()
	var stable int
	require.Eventually(t, func() bool {
		a := runtime.NumGoroutine()
		time.Sleep(time.Millisecond)
		b := runtime.NumGoroutine()
		if a == b {
			stable = a
			return true
		}
		return false
	}, 2*time.Second, time.Millisecond, "runtime.NumGoroutine() never stabilized")
	return stable
}

// tickAllConcurrently advances every rig's fakeClock by n ticks each,
// running each board's tick loop on its own goroutine so wall-clock time is
// bounded by the slowest board rather than the sum across boards -- boards
// are fully independent (separate Runner, separate fakeClock), so there is
// nothing to serialize here.
func tickAllConcurrently(rigs []soakRig, n int) {
	var wg sync.WaitGroup
	for _, rg := range rigs {
		wg.Add(1)
		go func(clock *fakeClock) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				clock.Tick()
			}
		}(rg.clock)
	}
	wg.Wait()
}

// TestSoak_FullScenarioSetBoots is NFR5's baseline: BuildBoards over the
// real scenario directory with an empty EMULATOR_BOARDS yields one board
// per file, all with distinct device_ids, and every board connects
// (publishes "online" status + retained manifest) exactly once. The
// expected count is derived from the loaded scenario map, not hardcoded --
// adding an 8th scenario file must grow this count, not fail the test.
func TestSoak_FullScenarioSetBoots(t *testing.T) {
	scenarios := realScenarios(t)

	boards, err := BuildBoards(&Config{EmulatorBoards: ""}, scenarios)
	require.NoError(t, err)
	assert.Len(t, boards, len(scenarios), "empty EMULATOR_BOARDS should yield exactly one board per loaded scenario file")

	rigs := startSoakRigs(t, boards)
	defer stopSoakRigs(rigs)

	seen := make(map[string]bool, len(rigs))
	for _, rg := range rigs {
		assert.False(t, seen[rg.board.DeviceID], "duplicate device_id %s", rg.board.DeviceID)
		seen[rg.board.DeviceID] = true

		assert.Equal(t, int64(1), rg.transport.count(statusTopic(rg.board.DeviceID)), "board %s should publish online status exactly once on connect", rg.board.DeviceID)
		assert.Equal(t, int64(1), rg.transport.count(manifestTopic(rg.board.DeviceID)), "board %s should publish its retained manifest exactly once on connect", rg.board.DeviceID)
	}
	assert.Len(t, seen, len(scenarios))
}

// TestSoak_GoroutineCountLinearInBoards is the direct regression guard on
// #1769's per-board (not per-sensor) goroutine requirement: the delta
// between pre-start and post-start runtime.NumGoroutine() must scale with
// board count, not sensor count.
//
// k = 1 here: with the countingTransport test double (no paho client), the
// only goroutine a Runner adds is its own reading-loop goroutine
// (ensureLoopStarted's `go r.loop(stopCh)` in runner.go) -- Start() itself
// runs the connect sequence synchronously on the test-double path. A real
// broker connection (NewPahoRunner) adds several more per client internally
// (paho's read/keepalive/outbound goroutines), which this hermetic test
// does not exercise; that overhead is why NFR5's real-machine measurement
// (recorded on #1774) matters as a separate check alongside this one.
func TestSoak_GoroutineCountLinearInBoards(t *testing.T) {
	boards := buildAllBoards(t)
	sensorCount := enabledSensorCount(boards)
	require.Greater(t, sensorCount, len(boards), "fixture sanity: expect more sensors than boards so this test can distinguish per-board from per-sensor scaling")

	baseline := stableGoroutineCount(t)

	rigs := startSoakRigs(t, boards)
	defer stopSoakRigs(rigs)

	afterStart := stableGoroutineCount(t)
	delta := afterStart - baseline

	const k = 1
	assert.LessOrEqual(t, delta, len(boards)*k,
		"goroutine count must scale with board count (%d boards), not sensor count (%d sensors): delta=%d", len(boards), sensorCount, delta)
	assert.Less(t, delta, sensorCount*2,
		"goroutine delta (%d) should be comfortably below sensors*2 (%d)", delta, sensorCount*2)
}

// TestSoak_NoGoroutineLeakOnShutdown asserts every goroutine a soak session
// added is gone again after every Runner.Stop() has returned -- retried
// with a timeout rather than a fixed sleep, so it's fast when the loops
// exit promptly and only slow (and eventually fails loudly) when they
// don't.
func TestSoak_NoGoroutineLeakOnShutdown(t *testing.T) {
	baseline := stableGoroutineCount(t)

	boards := buildAllBoards(t)
	rigs := startSoakRigs(t, boards)

	// Drive a few ticks so every loop is doing real work (readings +
	// walker/nextDue bookkeeping), not just idling on its ticker channel,
	// before shutdown.
	tickAllConcurrently(rigs, 5)

	stopSoakRigs(rigs)

	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+1
	}, 2*time.Second, 10*time.Millisecond,
		"goroutines leaked after every Runner.Stop() returned: baseline=%d, now=%d", baseline, runtime.NumGoroutine())
}

// TestSoak_NoUnboundedAllocationOverLongRun drives the full scenario set
// through the equivalent of a multi-hour session (4 simulated hours at the
// reading loop's own 100ms tickGranularity, runner.go) and asserts
// HeapAlloc after a GC has not grown materially versus a reading taken
// after the first 100 ticks. This is the direct regression guard against an
// unbounded per-tick buffer or accumulating slice inside the emulator --
// countingTransport deliberately never accumulates a publish history
// itself, so any growth measured here can only come from the emulator's own
// runner/walker state.
func TestSoak_NoUnboundedAllocationOverLongRun(t *testing.T) {
	boards := buildAllBoards(t)
	rigs := startSoakRigs(t, boards)
	defer stopSoakRigs(rigs)

	const baselineTicks = 100
	// 4 simulated hours at the loop's 100ms tickGranularity (runner.go) --
	// NFR6's sustained-session duration, driven through the injected clock
	// rather than real time so this stays a seconds-long unit test.
	totalTicks := int(4 * time.Hour / tickGranularity)
	require.Greater(t, totalTicks, baselineTicks)

	tickAllConcurrently(rigs, baselineTicks)
	runtime.GC()
	runtime.GC()
	var baselineStats runtime.MemStats
	runtime.ReadMemStats(&baselineStats)

	tickAllConcurrently(rigs, totalTicks-baselineTicks)
	runtime.GC()
	runtime.GC()
	var finalStats runtime.MemStats
	runtime.ReadMemStats(&finalStats)

	t.Logf("heap after %d ticks: %d bytes; heap after %d ticks (%s simulated): %d bytes",
		baselineTicks, baselineStats.HeapAlloc, totalTicks, 4*time.Hour, finalStats.HeapAlloc)

	// A per-tick accumulating buffer would grow heap roughly linearly with
	// tick count (1,440x more ticks between the two snapshots here); allow
	// generous headroom for ordinary allocator/GC noise without masking a
	// real leak. Floor the bound so a tiny baseline reading doesn't make the
	// bound meaninglessly tight.
	const maxGrowthFactor = 5
	maxAllowed := baselineStats.HeapAlloc * maxGrowthFactor
	const floor = 2 << 20 // 2 MiB
	if maxAllowed < floor {
		maxAllowed = floor
	}
	assert.LessOrEqualf(t, finalStats.HeapAlloc, maxAllowed,
		"heap grew from %d to %d bytes over a simulated 4-hour session (>%dx) -- suspect an unbounded per-tick buffer",
		baselineStats.HeapAlloc, finalStats.HeapAlloc, maxGrowthFactor)
}

// TestSoak_MessageRateMatchesNFR6 drives the full scenario set through a
// simulated hour at the board default interval and asserts total sensor
// reading publishes equal the computed expectation (sensors * 3600s /
// interval), not a magic number, and that the derived aggregate rate is
// single-digit messages/second, as NFR6 promises for the default
// configuration.
func TestSoak_MessageRateMatchesNFR6(t *testing.T) {
	boards := buildAllBoards(t)
	rigs := startSoakRigs(t, boards)
	defer stopSoakRigs(rigs)

	sensorCount := enabledSensorCount(boards)
	require.Greater(t, sensorCount, 0)

	defaultInterval := testDeps().DefaultInterval
	const simulatedWindow = time.Hour
	ticks := int(simulatedWindow / tickGranularity)

	sumReadings := func() int64 {
		var total int64
		for _, rg := range rigs {
			for _, s := range rg.board.Sensors {
				if !s.Enabled {
					continue
				}
				total += rg.transport.count(sensorTopic(rg.board.DeviceID, s.Name))
			}
		}
		return total
	}

	tickAllConcurrently(rigs, ticks)

	wantPerSensor := simulatedWindow.Seconds() / defaultInterval.Seconds()
	wantTotal := wantPerSensor * float64(sensorCount)
	// One reading's worth of slack per sensor: the last tick's publishes
	// may still be in flight on each board's loop goroutine right as
	// tickAllConcurrently's WaitGroup (which only waits for every tick to
	// be *sent*, not processed) returns.
	tolerance := float64(sensorCount)

	require.Eventually(t, func() bool {
		return float64(sumReadings()) >= wantTotal-tolerance
	}, time.Second, time.Millisecond, "timed out waiting for the expected reading count to settle")

	total := sumReadings()
	assert.InDelta(t, wantTotal, float64(total), tolerance,
		"expected roughly sensors(%d) * window/interval(%.0f) = %.0f readings over a simulated %s, got %d",
		sensorCount, wantPerSensor, wantTotal, simulatedWindow, total)

	rate := float64(total) / simulatedWindow.Seconds()
	t.Logf("measured publish rate: %.3f msgs/sec across %d sensors over a simulated %s", rate, sensorCount, simulatedWindow)
	assert.Greater(t, rate, 0.0, "NFR6: expected a non-zero aggregate publish rate")
	assert.Less(t, rate, 10.0, "NFR6: default-interval aggregate rate should be single-digit messages/sec on a laptop-scale scenario set")
}

// TestSoak_ConcurrentConfigPushUnderLoad pushes a config to every board in
// the default scenario set at the same time, with every board's reading
// loop actively ticking concurrently, and asserts every board acks,
// applies, and keeps publishing -- now on its new topic. Meaningful under
// the race detector (see this file's package comment); TestHandleConfig_
// RaceWithReadingLoop (config_apply_test.go, #1771) already proves the
// per-Runner mutex is race-safe in isolation, so what this test adds is
// proof that holds when every board in the default load is doing it at
// once (NFR5).
func TestSoak_ConcurrentConfigPushUnderLoad(t *testing.T) {
	boards := buildAllBoards(t)
	rigs := startSoakRigs(t, boards)
	defer stopSoakRigs(rigs)

	const backgroundTicks = 30
	var tickWG sync.WaitGroup
	for _, rg := range rigs {
		tickWG.Add(1)
		go func(clock *fakeClock) {
			defer tickWG.Done()
			for i := 0; i < backgroundTicks; i++ {
				clock.Tick()
			}
		}(rg.clock)
	}

	var pushWG sync.WaitGroup
	for _, rg := range rigs {
		pushWG.Add(1)
		go func(rg soakRig) {
			defer pushWG.Done()
			s := rg.board.Sensors[0]
			entry := &configpb.SensorConfig{
				I2CAddress: s.I2CAddress,
				SensorType: s.SensorType,
				Name:       s.Name + "-v2",
			}
			if s.MuxAddress != 0 {
				entry.MuxPath = []*configpb.MuxHop{{MuxAddress: s.MuxAddress, MuxChannel: s.MuxChannel}}
			}
			payload, err := proto.Marshal(&configpb.DeviceConfig{Version: 1, Sensors: []*configpb.SensorConfig{entry}})
			require.NoError(t, err)
			rg.transport.deliver(configTopic(rg.board.DeviceID), payload)
		}(rg)
	}
	pushWG.Wait()
	tickWG.Wait()

	for _, rg := range rigs {
		payload := rg.transport.last(configAckTopic(rg.board.DeviceID))
		require.NotNil(t, payload, "board %s: expected a config ack", rg.board.DeviceID)
		var ack configpb.DeviceConfigAck
		require.NoError(t, proto.Unmarshal(payload, &ack))
		assert.True(t, ack.GetAccepted(), "board %s: expected the concurrent config push to be accepted", rg.board.DeviceID)
		assert.Equal(t, uint64(1), ack.GetAppliedVersion())

		newTopic := sensorTopic(rg.board.DeviceID, rg.board.Sensors[0].Name+"-v2")
		// Drive additional ticks post-push so the renamed sensor gets a
		// chance to publish under its new topic even on boards whose push
		// landed after their background ticking above already finished.
		for i := 0; i < 60; i++ {
			rg.clock.Tick()
		}
		require.Eventually(t, func() bool {
			return rg.transport.count(newTopic) >= 1
		}, time.Second, time.Millisecond,
			"board %s: expected a publish on the renamed topic %s after config apply", rg.board.DeviceID, newTopic)
	}
}
