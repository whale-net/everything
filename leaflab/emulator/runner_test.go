package main

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// fakeTicker is a clockTicker whose channel is only ever fed by an explicit
// fakeClock.Tick() call -- never by a real timer -- so runner_test.go can
// drive the reading loop deterministically instead of sleeping in real
// time.
type fakeTicker struct {
	ch      chan time.Time
	stopped bool
}

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               { f.stopped = true }

// fakeClock is a runnerClock whose Now() only advances when the test calls
// Tick(); Runner.loop creates exactly one ticker per board (NFR5), so
// fakeClock tracks that single ticker directly rather than a set.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	ticker *fakeTicker
	// sleeps records every Sleep(d) call's duration, in order --
	// config_apply_test.go's subscribeConfig retry coverage (#2024) uses
	// this to assert the actual backoff durations attempted, since Sleep
	// itself is a no-op below.
	sleeps []time.Duration
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTicker(_ time.Duration) clockTicker {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTicker{ch: make(chan time.Time)}
	c.ticker = t
	return t
}

// Sleep does not actually block: fakeClock's whole purpose is instant,
// deterministic time (see Tick) -- a real sleep here would make every test
// that exercises subscribeConfig's retry backoff (config_apply.go, issue
// #2024) slow for no benefit, since nothing in these tests reads
// wall-clock time. The duration is still recorded (sleeps) so a test can
// assert the backoff schedule an implementation actually used.
func (c *fakeClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
}

// Sleeps returns a copy of every duration passed to Sleep so far, in call
// order.
func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// hasTicker reports whether Runner.loop has called NewTicker yet -- the
// loop goroutine spawned by ensureLoopStarted races with the test calling
// Tick, so tests must wait for this before ticking.
func (c *fakeClock) hasTicker() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ticker != nil
}

// Tick advances the fake clock by exactly tickGranularity and delivers the
// new "now" to the current ticker. The send blocks on the loop goroutine's
// unbuffered receive, so by the time Tick returns the loop has at least
// begun processing this tick (though publishDueReadings' own work happens
// after receipt -- callers that assert on its effects still need
// require.Eventually, see waitForPublishCount below).
func (c *fakeClock) Tick() time.Time {
	c.mu.Lock()
	c.now = c.now.Add(tickGranularity)
	now := c.now
	t := c.ticker
	c.mu.Unlock()
	t.ch <- now
	return now
}

// waitForTicker blocks until the runner's loop goroutine has registered its
// ticker with clock, so the first Tick() call has somewhere to send.
func waitForTicker(t *testing.T, clock *fakeClock) {
	t.Helper()
	require.Eventually(t, clock.hasTicker, time.Second, time.Millisecond, "runner loop never called clock.NewTicker")
}

// waitForPublishCount blocks until transport has recorded at least n
// publishes, so tests don't race the loop goroutine's own scheduling.
func waitForPublishCount(t *testing.T, transport *fakeTransport, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		return len(transport.publishes) >= n
	}, time.Second, time.Millisecond, "timed out waiting for %d publishes", n)
}

// newTestRunner builds a Runner over a fakeTransport and fakeClock for
// board -- the broker-free path every test in this file uses (no broker, no
// testcontainers; broker-backed exercise happens in the Tilt task).
func newTestRunner(board Board, deps RunnerDeps) (*Runner, *fakeTransport, *fakeClock) {
	transport := newFakeTransport()
	r := NewRunner(board, transport, deps)
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	r.clock = clock
	return r, transport, clock
}

// twoSensorBoard is the fixture used by the interval-override and
// connect-ordering tests: one sensor at the board default interval, one
// with a PollIntervalMS override.
func twoSensorBoard() Board {
	return Board{
		DeviceID: "leaflab-testboard01",
		Scenario: "synthetic",
		Sensors: []Sensor{
			{Name: "default-interval", ChipType: configpb.ChipType_CHIP_TYPE_BH1750, SensorType: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Enabled: true},
			{Name: "fast-interval", ChipType: configpb.ChipType_CHIP_TYPE_BH1750, SensorType: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Enabled: true, PollIntervalMS: 1000},
		},
	}
}

func testDeps() RunnerDeps {
	return RunnerDeps{
		DefaultInterval: 5 * time.Second,
		Rand:            rand.New(rand.NewSource(1)),
	}
}

// TestRunner_ConnectOrdering asserts the connect sequence publishes status
// before manifest, and both before the first reading -- and pins the exact
// retained/qos flags and payload bytes leaflab/MQTT.md requires.
func TestRunner_ConnectOrdering(t *testing.T) {
	board := twoSensorBoard()
	r, transport, clock := newTestRunner(board, testDeps())

	require.NoError(t, r.Start())
	waitForTicker(t, clock)

	// Drive one board-default interval (5s = 50 * 100ms ticks) so at least
	// one reading has been published for both sensors.
	for i := 0; i < 50; i++ {
		clock.Tick()
	}
	waitForPublishCount(t, transport, 3) // status, manifest, >=1 reading

	transport.mu.Lock()
	publishes := append([]publishRecord(nil), transport.publishes...)
	transport.mu.Unlock()

	require.GreaterOrEqual(t, len(publishes), 3)

	status := publishes[0]
	assert.Equal(t, statusTopic(board.DeviceID), status.topic)
	assert.Equal(t, byte(1), status.qos)
	assert.True(t, status.retained, "status must be retained")
	assert.Equal(t, []byte("online"), status.payload, "status payload must be exactly \"online\", byte-compare")

	manifestRec := publishes[1]
	assert.Equal(t, manifestTopic(board.DeviceID), manifestRec.topic)
	assert.Equal(t, byte(1), manifestRec.qos)
	assert.True(t, manifestRec.retained, "manifest must be retained")

	var manifest firmwarepb.DeviceManifest
	require.NoError(t, proto.Unmarshal(manifestRec.payload, &manifest))
	assert.Equal(t, board.DeviceID, manifest.GetDeviceId())

	for _, rec := range publishes[2:] {
		assert.False(t, rec.retained, "sensor readings must not be retained")
		assert.Equal(t, byte(0), rec.qos)
	}
}

// TestRunner_TopicCorrectness asserts the exact literal sensor topic string
// for every enabled sensor, not a prefix match.
func TestRunner_TopicCorrectness(t *testing.T) {
	board := twoSensorBoard()
	r, transport, clock := newTestRunner(board, testDeps())

	require.NoError(t, r.Start())
	waitForTicker(t, clock)
	for i := 0; i < 50; i++ {
		clock.Tick()
	}
	waitForPublishCount(t, transport, 8) // status, manifest, 1 default-interval reading, 5 fast-interval readings

	transport.mu.Lock()
	defer transport.mu.Unlock()

	wantTopics := map[string]bool{
		"leaflab/leaflab-testboard01/sensor/default-interval": false,
		"leaflab/leaflab-testboard01/sensor/fast-interval":    false,
	}
	for _, rec := range transport.publishes {
		if _, ok := wantTopics[rec.topic]; ok {
			wantTopics[rec.topic] = true
		}
	}
	for topic, seen := range wantTopics {
		assert.True(t, seen, "expected a publish to exact topic %q", topic)
	}
}

// TestRunner_ReadingDecodes asserts a captured reading payload decodes to a
// SensorReading whose Value is in range for the sensor's type and whose
// UptimeMs is monotonically non-decreasing across ticks.
func TestRunner_ReadingDecodes(t *testing.T) {
	board := Board{
		DeviceID: "leaflab-testboard02",
		Sensors: []Sensor{
			{Name: "light", ChipType: configpb.ChipType_CHIP_TYPE_BH1750, SensorType: firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE, Enabled: true, PollIntervalMS: 100},
		},
	}
	deps := testDeps()
	r, transport, clock := newTestRunner(board, deps)

	require.NoError(t, r.Start())
	waitForTicker(t, clock)

	const numTicks = 10
	for i := 0; i < numTicks; i++ {
		clock.Tick()
	}
	// status + manifest + one reading per tick at a 100ms interval.
	waitForPublishCount(t, transport, 2+numTicks)

	transport.mu.Lock()
	publishes := append([]publishRecord(nil), transport.publishes...)
	transport.mu.Unlock()

	wantRange := rangeFor(firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE)
	sensorTopicStr := sensorTopic(board.DeviceID, "light")

	var lastUptime uint32
	var seen int
	for _, rec := range publishes {
		if rec.topic != sensorTopicStr {
			continue
		}
		var reading firmwarepb.SensorReading
		require.NoError(t, proto.Unmarshal(rec.payload, &reading))
		assert.GreaterOrEqual(t, reading.GetValue(), wantRange.min)
		assert.LessOrEqual(t, reading.GetValue(), wantRange.max)
		assert.GreaterOrEqual(t, reading.GetUptimeMs(), lastUptime, "UptimeMs must be monotonically non-decreasing")
		lastUptime = reading.GetUptimeMs()
		seen++
	}
	assert.GreaterOrEqual(t, seen, numTicks-1, "expected roughly one reading per 100ms tick")
}

// TestRunner_PerSensorIntervalOverride is the interval-override test: a
// sensor with PollIntervalMS = 1000 under a board default of 5s publishes
// 5x as often as the default-interval sensor over a simulated 5s window.
// Time is driven entirely through fakeClock, never time.Sleep, so this is
// fast and non-flaky.
func TestRunner_PerSensorIntervalOverride(t *testing.T) {
	board := twoSensorBoard()
	r, transport, clock := newTestRunner(board, testDeps())

	require.NoError(t, r.Start())
	waitForTicker(t, clock)

	// 5s of simulated time at 100ms granularity = 50 ticks.
	for i := 0; i < 50; i++ {
		clock.Tick()
	}
	waitForPublishCount(t, transport, 2+1+5) // status, manifest, 1 default + 5 fast

	transport.mu.Lock()
	defer transport.mu.Unlock()

	defaultCount, fastCount := 0, 0
	defaultTopic := sensorTopic(board.DeviceID, "default-interval")
	fastTopic := sensorTopic(board.DeviceID, "fast-interval")
	for _, rec := range transport.publishes {
		switch rec.topic {
		case defaultTopic:
			defaultCount++
		case fastTopic:
			fastCount++
		}
	}

	assert.Equal(t, 1, defaultCount, "5s-default sensor should publish once over a 5s window")
	assert.Equal(t, 5, fastCount, "1s-override sensor should publish 5x over a 5s window")
}

// TestRunner_ReconnectRepublishes invokes the OnConnect path (onConnect) a
// second time, as paho does on every reconnect, and asserts it republishes
// "online" and the retained manifest rather than assuming state survived
// the reconnect.
func TestRunner_ReconnectRepublishes(t *testing.T) {
	board := twoSensorBoard()
	r, transport, clock := newTestRunner(board, testDeps())

	require.NoError(t, r.Start())
	waitForTicker(t, clock)

	r.onConnect() // simulate a broker reconnect

	transport.mu.Lock()
	defer transport.mu.Unlock()

	statusPublishes, manifestPublishes := 0, 0
	for _, rec := range transport.publishes {
		switch rec.topic {
		case statusTopic(board.DeviceID):
			if string(rec.payload) == "online" {
				statusPublishes++
			}
		case manifestTopic(board.DeviceID):
			manifestPublishes++
		}
	}

	assert.Equal(t, 2, statusPublishes, "expected \"online\" republished on reconnect")
	assert.Equal(t, 2, manifestPublishes, "expected retained manifest republished on reconnect")
}

// TestRunner_CleanShutdown asserts Stop publishes "offline" retained, stops
// the reading loop, and that no further publishes occur once Stop returns.
func TestRunner_CleanShutdown(t *testing.T) {
	board := twoSensorBoard()
	r, transport, clock := newTestRunner(board, testDeps())

	require.NoError(t, r.Start())
	waitForTicker(t, clock)
	for i := 0; i < 10; i++ {
		clock.Tick()
	}
	waitForPublishCount(t, transport, 2)

	r.Stop()

	transport.mu.Lock()
	countAfterStop := len(transport.publishes)
	last := transport.publishes[len(transport.publishes)-1]
	disconnected := transport.disconnected
	transport.mu.Unlock()

	assert.Equal(t, statusTopic(board.DeviceID), last.topic)
	assert.True(t, last.retained, "shutdown status publish must be retained")
	assert.Equal(t, []byte("offline"), last.payload)
	assert.True(t, disconnected)

	// No further publishes once Stop has returned -- the loop goroutine
	// must actually have exited, not just been asked to.
	time.Sleep(20 * time.Millisecond)
	transport.mu.Lock()
	assert.Equal(t, countAfterStop, len(transport.publishes), "no publishes should occur after Stop returns")
	transport.mu.Unlock()

	// Second Stop call is a no-op.
	r.Stop()
	transport.mu.Lock()
	assert.Equal(t, countAfterStop, len(transport.publishes), "second Stop call must be a no-op")
	transport.mu.Unlock()
}
