// config_apply_test.go exercises FR18's config-consumption flow
// (config_apply.go) against the fakeTransport/fakeClock doubles from #1769 --
// no broker. See this file's issue (#1771) Testing section for the full
// coverage list this pins.
package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// muxLightTempBoard loads the real mux-light-temp.json scenario (light @
// mux112/ch7/i2c35, temperature + humidity both @ mux112/ch5/i2c68,
// discriminated by sensor_type) so config-matching tests exercise the same
// (mux_address, mux_channel, i2c_address) + sensor_type resolution real
// scenario data goes through, not a synthetic stand-in.
func muxLightTempBoard(t *testing.T, deviceID string) Board {
	t.Helper()
	path := filepath.Join(scenariosDir(t), "mux-light-temp.json")
	scenario, err := LoadScenario(path)
	require.NoError(t, err)
	sensors, err := resolveBoardSensors(scenario)
	require.NoError(t, err)
	return Board{DeviceID: deviceID, Scenario: scenario.Name, Sensors: sensors}
}

// startedRunner builds and starts (via the no-broker test-double Start path)
// a Runner over board, returning it along with the fakeTransport/fakeClock
// so a test can push config through transport.deliver and drive the reading
// loop through clock.Tick.
func startedRunner(t *testing.T, board Board) (*Runner, *fakeTransport, *fakeClock) {
	t.Helper()
	r, transport, clock := newTestRunner(board, testDeps())
	require.NoError(t, r.Start())
	waitForTicker(t, clock)
	return r, transport, clock
}

// pushConfig marshals cfg and delivers it on board's config topic through
// transport, exactly as a real broker delivery would invoke the subscribed
// handler.
func pushConfig(t *testing.T, transport *fakeTransport, deviceID string, cfg *configpb.DeviceConfig) {
	t.Helper()
	payload, err := proto.Marshal(cfg)
	require.NoError(t, err)
	transport.deliver(configTopic(deviceID), payload)
}

// lastAck returns the most recent DeviceConfigAck published to deviceID's
// ack topic.
func lastAck(t *testing.T, transport *fakeTransport, deviceID string) *configpb.DeviceConfigAck {
	t.Helper()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	topic := configAckTopic(deviceID)
	for i := len(transport.publishes) - 1; i >= 0; i-- {
		rec := transport.publishes[i]
		if rec.topic != topic {
			continue
		}
		assert.Equal(t, byte(1), rec.qos, "ack must be QoS 1")
		assert.False(t, rec.retained, "ack must not be retained")
		var ack configpb.DeviceConfigAck
		require.NoError(t, proto.Unmarshal(rec.payload, &ack))
		return &ack
	}
	t.Fatalf("no DeviceConfigAck published for %s", deviceID)
	return nil
}

// manifestPublishCount counts how many times deviceID's retained manifest
// topic was published to.
func manifestPublishCount(transport *fakeTransport, deviceID string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	topic := manifestTopic(deviceID)
	n := 0
	for _, rec := range transport.publishes {
		if rec.topic == topic {
			n++
		}
	}
	return n
}

// topicPublishCount counts how many times topic was published to.
func topicPublishCount(transport *fakeTransport, topic string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	n := 0
	for _, rec := range transport.publishes {
		if rec.topic == topic {
			n++
		}
	}
	return n
}

// waitForTopicPublishCount blocks until topic has been published to at
// least n times -- used instead of the raw waitForPublishCount (which
// counts every topic) when a test needs to isolate one sensor's reading
// topic from its board-mates' own independently-scheduled publishes.
func waitForTopicPublishCount(t *testing.T, transport *fakeTransport, topic string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return topicPublishCount(transport, topic) >= n
	}, time.Second, time.Millisecond, "timed out waiting for %d publishes to topic %q", n, topic)
}

// topicUptimes decodes, in publish order, the UptimeMs of every
// SensorReading published to topic.
func topicUptimes(t *testing.T, transport *fakeTransport, topic string) []uint32 {
	t.Helper()
	transport.mu.Lock()
	recs := make([]publishRecord, 0)
	for _, rec := range transport.publishes {
		if rec.topic == topic {
			recs = append(recs, rec)
		}
	}
	transport.mu.Unlock()

	ups := make([]uint32, 0, len(recs))
	for _, rec := range recs {
		var reading firmwarepb.SensorReading
		require.NoError(t, proto.Unmarshal(rec.payload, &reading))
		ups = append(ups, reading.GetUptimeMs())
	}
	return ups
}

// latestManifest decodes the most recently published retained manifest for
// deviceID.
func latestManifest(t *testing.T, transport *fakeTransport, deviceID string) *firmwarepb.DeviceManifest {
	t.Helper()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	topic := manifestTopic(deviceID)
	for i := len(transport.publishes) - 1; i >= 0; i-- {
		rec := transport.publishes[i]
		if rec.topic != topic {
			continue
		}
		var manifest firmwarepb.DeviceManifest
		require.NoError(t, proto.Unmarshal(rec.payload, &manifest))
		return &manifest
	}
	t.Fatalf("no manifest published for %s", deviceID)
	return nil
}

// lightMuxPath and shtMuxPath are mux-light-temp.json's real mux hops
// (from the scenario file: light on ch7, temperature+humidity on ch5, both
// off mux 112) -- matchSensorEntry keys on (mux_address, mux_channel,
// i2c_address), so a SensorConfig entry that omits mux_path (defaulting to
// 0/0) never matches any sensor on this board and every test below must set
// it explicitly.
var (
	lightMuxPath = []*configpb.MuxHop{{MuxAddress: 112, MuxChannel: 7}}
	shtMuxPath   = []*configpb.MuxHop{{MuxAddress: 112, MuxChannel: 5}}
)

func manifestNames(m *firmwarepb.DeviceManifest) map[string]bool {
	out := make(map[string]bool, len(m.GetSensors()))
	for _, d := range m.GetSensors() {
		out[d.GetName()] = true
	}
	return out
}

// TestHandleConfig_VersionRejection covers rejection of a push whose version
// is not strictly greater than current: equal, less-than, and the zero
// value (always a rejection since versions are monotonic and 1-based).
func TestHandleConfig_VersionRejection(t *testing.T) {
	for _, pushVersion := range []uint64{5, 3, 0} {
		t.Run("", func(t *testing.T) {
			board := muxLightTempBoard(t, "leaflab-versionreject01")
			r, transport, _ := startedRunner(t, board)
			r.configVersion = 5

			manifestsBefore := manifestPublishCount(transport, board.DeviceID)

			pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
				Version: pushVersion,
				Sensors: []*configpb.SensorConfig{
					{MuxPath: lightMuxPath, I2CAddress: 35, Name: "renamed-light"},
				},
			})

			ack := lastAck(t, transport, board.DeviceID)
			assert.False(t, ack.GetAccepted())
			assert.Equal(t, uint64(5), ack.GetAppliedVersion())
			assert.NotEmpty(t, ack.GetReason())

			assert.Equal(t, manifestsBefore, manifestPublishCount(transport, board.DeviceID), "no manifest re-publish on rejection")

			r.mu.Lock()
			assert.Equal(t, "light", r.board.Sensors[0].Name, "sensor state must be unchanged on rejection")
			r.mu.Unlock()
		})
	}
}

// TestHandleConfig_VersionAcceptance covers the accept path across a
// sequence of pushes: 0 -> 1 (accept) -> 2 (accept) -> 1 again (reject,
// since current is now 2).
func TestHandleConfig_VersionAcceptance(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-versionaccept01")
	r, transport, _ := startedRunner(t, board)
	require.Equal(t, uint64(0), r.configVersion)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{Version: 1})
	ack := lastAck(t, transport, board.DeviceID)
	assert.True(t, ack.GetAccepted())
	assert.Equal(t, uint64(1), ack.GetAppliedVersion())

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{Version: 2})
	ack = lastAck(t, transport, board.DeviceID)
	assert.True(t, ack.GetAccepted())
	assert.Equal(t, uint64(2), ack.GetAppliedVersion())

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{Version: 1})
	ack = lastAck(t, transport, board.DeviceID)
	assert.False(t, ack.GetAccepted())
	assert.Equal(t, uint64(2), ack.GetAppliedVersion(), "rejection reports current, not the rejected, version")
}

// TestHandleConfig_RenameApplies covers the rename path: the re-published
// manifest carries the new name, and subsequent readings publish to the new
// topic and no longer to the old one.
func TestHandleConfig_RenameApplies(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-rename01")
	_, transport, clock := startedRunner(t, board)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, Name: "brightness"},
		},
	})
	ack := lastAck(t, transport, board.DeviceID)
	require.True(t, ack.GetAccepted())

	manifest := latestManifest(t, transport, board.DeviceID)
	names := manifestNames(manifest)
	assert.True(t, names["brightness"], "renamed sensor must appear under its new name")
	assert.False(t, names["light"], "renamed sensor must not appear under its old name")

	oldTopic := sensorTopic(board.DeviceID, "light")
	newTopic := sensorTopic(board.DeviceID, "brightness")

	publishesBefore := len(transport.publishes)
	for i := 0; i < 50; i++ { // 5s of ticks; board default interval
		clock.Tick()
	}
	waitForPublishCount(t, transport, publishesBefore+1)

	transport.mu.Lock()
	defer transport.mu.Unlock()
	sawNew := false
	for _, rec := range transport.publishes[publishesBefore:] {
		assert.NotEqual(t, oldTopic, rec.topic, "no publish should land on the pre-rename topic after rename")
		if rec.topic == newTopic {
			sawNew = true
		}
	}
	assert.True(t, sawNew, "expected at least one reading on the renamed topic")
}

// TestHandleConfig_DuplicateNameRejectedAtomically covers the
// duplicate-name rejection: renaming temperature to humidity must reject
// the whole push, leaving the manifest and topics completely untouched
// (not partially applied).
func TestHandleConfig_DuplicateNameRejectedAtomically(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-dupname01")
	r, transport, _ := startedRunner(t, board)

	manifestsBefore := manifestPublishCount(transport, board.DeviceID)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: shtMuxPath, I2CAddress: 68, SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Name: "humidity"},
		},
	})

	ack := lastAck(t, transport, board.DeviceID)
	assert.False(t, ack.GetAccepted())
	assert.Contains(t, ack.GetReason(), "duplicate")
	assert.Contains(t, ack.GetReason(), "humidity")

	assert.Equal(t, manifestsBefore, manifestPublishCount(transport, board.DeviceID), "no manifest re-publish on rejection")

	r.mu.Lock()
	for _, s := range r.board.Sensors {
		if s.I2CAddress == 68 && s.MuxChannel == 5 && s.SensorType == firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE {
			assert.Equal(t, "temperature", s.Name, "reject must be atomic: nothing applied")
		}
	}
	r.mu.Unlock()
}

// TestHandleConfig_EnabledTriState covers the three enabled states: absent
// (leave unchanged), explicit false (disable), explicit true (re-enable).
func TestHandleConfig_EnabledTriState(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-enabledtristate01")
	_, transport, clock := startedRunner(t, board)

	// Absent `enabled` (only name touched, unrelated) leaves the sensor's
	// current (enabled=true) state alone.
	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, Name: "light"}, // Enabled left nil
		},
	})
	ack := lastAck(t, transport, board.DeviceID)
	require.True(t, ack.GetAccepted())
	assert.True(t, manifestNames(latestManifest(t, transport, board.DeviceID))["light"], "absent `enabled` must leave the sensor's current state alone")

	// Explicit enabled=false removes it from the manifest and stops its
	// readings.
	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 2,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, Enabled: proto.Bool(false)},
		},
	})
	ack = lastAck(t, transport, board.DeviceID)
	require.True(t, ack.GetAccepted())
	assert.False(t, manifestNames(latestManifest(t, transport, board.DeviceID))["light"], "enabled=false must remove the sensor from the manifest")

	lightTopic := sensorTopic(board.DeviceID, "light")
	lightCountAtDisable := topicPublishCount(transport, lightTopic)
	for i := 0; i < 50; i++ { // 5s: at least one board-default tick for temperature/humidity to settle on
		clock.Tick()
	}
	waitForTopicPublishCount(t, transport, sensorTopic(board.DeviceID, "temperature"), 1)
	assert.Equal(t, lightCountAtDisable, topicPublishCount(transport, lightTopic), "disabled sensor must not publish readings")

	// Explicit enabled=true restores both manifest presence and readings.
	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 3,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, Enabled: proto.Bool(true)},
		},
	})
	ack = lastAck(t, transport, board.DeviceID)
	require.True(t, ack.GetAccepted())
	assert.True(t, manifestNames(latestManifest(t, transport, board.DeviceID))["light"], "enabled=true must restore manifest presence")

	// A re-enabled sensor's due time is created fresh on the loop's own
	// goroutine the next time it's checked (runner.go's publishDueReadings),
	// one full board-default interval out from that discovery tick -- not
	// from the moment it was re-enabled -- so this needs a full interval of
	// ticks plus slack for the discovery tick itself.
	for i := 0; i < 60; i++ { // 6s of slack over the 5s board default
		clock.Tick()
	}
	waitForTopicPublishCount(t, transport, lightTopic, lightCountAtDisable+1)
}

// TestHandleConfig_PollIntervalOverride covers poll_interval_ms: non-zero
// becomes the sensor's override interval, 0 reverts it to the board
// default.
// TestHandleConfig_PollIntervalOverride pins the exact reschedule semantics:
// applying an override does not move a sensor's already-scheduled next-due
// time (set at board-default cadence before the push landed), only the
// *next* reschedule after that uses the new interval -- so this drives the
// fake clock through an exact, hand-computed timeline (light's board
// default is 5000ms, the override is 1000ms) rather than assuming an
// override takes effect immediately.
func TestHandleConfig_PollIntervalOverride(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-pollinterval01")
	_, transport, clock := startedRunner(t, board)
	lightTopic := sensorTopic(board.DeviceID, "light")

	// Override applied before any tick: light's pre-existing next-due time
	// (boardStart+5000ms) is unaffected, but every reschedule computed from
	// then on uses the 1000ms override.
	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, PollIntervalMs: 1000},
		},
	})
	require.True(t, lastAck(t, transport, board.DeviceID).GetAccepted())

	// Tick to 13000ms: fires at 5000,6000,...,13000 -- 9 firings, 1000ms
	// apart, all reflecting the override (the very first firing's *time* is
	// the stale pre-override due time, but every *gap* from it onward
	// already reflects the override since the reschedule reads current
	// sensor state).
	for i := 0; i < 130; i++ {
		clock.Tick()
	}
	waitForTopicPublishCount(t, transport, lightTopic, 9)

	// Revert to board default. Must be pushed only after the 13000ms firing
	// is actually recorded (not just ticked) -- otherwise this push could
	// race that firing's own reschedule of nextDue, which reads
	// PollIntervalMS under the same lock this push takes.
	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 2,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, PollIntervalMs: 0},
		},
	})
	require.True(t, lastAck(t, transport, board.DeviceID).GetAccepted())

	// The next-due time already queued from the 13000ms firing (14000ms,
	// 1000ms later) is unaffected by the revert -- it still fires there.
	for i := 0; i < 10; i++ {
		clock.Tick()
	}
	waitForTopicPublishCount(t, transport, lightTopic, 10)

	// But its own reschedule, computed after the revert has landed, must
	// use the 5000ms board default: 14000+5000 = 19000, not 14000+1000.
	for i := 0; i < 50; i++ {
		clock.Tick()
	}
	waitForTopicPublishCount(t, transport, lightTopic, 11)

	uptimes := topicUptimes(t, transport, lightTopic)
	require.Len(t, uptimes, 11)
	for i := 1; i < 9; i++ {
		assert.Equal(t, uint32(1000), uptimes[i]-uptimes[i-1], "gap %d must reflect the 1000ms override", i)
	}
	assert.Equal(t, uint32(1000), uptimes[9]-uptimes[8], "the already-queued 14000ms firing must still be 1000ms after 13000ms")
	assert.Equal(t, uint32(5000), uptimes[10]-uptimes[9], "poll_interval_ms=0 must revert to the 5000ms board default for the next reschedule")
}

// TestHandleConfig_SensorTypeDiscriminator covers using sensor_type to
// disambiguate two sensors sharing an i2c address (SHT3x temp/humidity):
// a push targeting addr 68 / mux ch5 with sensor_type HUMIDITY must rename
// only humidity, leaving temperature untouched.
func TestHandleConfig_SensorTypeDiscriminator(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-discriminator01")
	_, transport, _ := startedRunner(t, board)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{
				MuxPath:    []*configpb.MuxHop{{MuxAddress: 112, MuxChannel: 5}},
				I2CAddress: 68,
				SensorType: firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY,
				Name:       "rh",
			},
		},
	})
	require.True(t, lastAck(t, transport, board.DeviceID).GetAccepted())

	names := manifestNames(latestManifest(t, transport, board.DeviceID))
	assert.True(t, names["rh"], "humidity sensor should be renamed")
	assert.True(t, names["temperature"], "temperature sensor must be untouched by a humidity-discriminated push")
	assert.False(t, names["humidity"], "old humidity name must be gone")
}

// TestHandleConfig_UnmatchedEntrySkipped covers an entry for hardware this
// board does not have: it is skipped (logged, not an error), other entries
// in the same push still apply, and the push is still accepted overall.
func TestHandleConfig_UnmatchedEntrySkipped(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-unmatched01")
	_, transport, _ := startedRunner(t, board)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{I2CAddress: 99, Name: "does-not-exist"}, // no sensor at this address
			{MuxPath: lightMuxPath, I2CAddress: 35, Name: "brightness"},     // matches "light"
		},
	})

	ack := lastAck(t, transport, board.DeviceID)
	assert.True(t, ack.GetAccepted(), "an unmatched entry must not cause the whole push to be rejected")

	names := manifestNames(latestManifest(t, transport, board.DeviceID))
	assert.True(t, names["brightness"], "the matched entry must still apply")
}

// TestHandleConfig_MalformedPayload covers a payload that doesn't decode as
// a DeviceConfig: acked rejected with a reason, no state change, no panic.
func TestHandleConfig_MalformedPayload(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-malformed01")
	r, transport, _ := startedRunner(t, board)

	manifestsBefore := manifestPublishCount(transport, board.DeviceID)

	assert.NotPanics(t, func() {
		transport.deliver(configTopic(board.DeviceID), []byte{0xff, 0x00, 0xff, 0x00, 0x01, 0x02})
	})

	ack := lastAck(t, transport, board.DeviceID)
	assert.False(t, ack.GetAccepted())
	assert.NotEmpty(t, ack.GetReason())
	assert.Equal(t, manifestsBefore, manifestPublishCount(transport, board.DeviceID), "no manifest re-publish on malformed payload")

	r.mu.Lock()
	assert.Equal(t, uint64(0), r.configVersion, "malformed payload must not change configVersion")
	r.mu.Unlock()
}

// TestHandleConfig_DeviceIDMismatch covers a DeviceConfig whose device_id
// field (authoritative per this issue's spec, even though the topic already
// scopes delivery) doesn't match this board.
func TestHandleConfig_DeviceIDMismatch(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-mismatch01")
	r, transport, _ := startedRunner(t, board)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		DeviceId: "leaflab-someotherboard",
		Version:  1,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, Name: "brightness"},
		},
	})

	ack := lastAck(t, transport, board.DeviceID)
	assert.False(t, ack.GetAccepted())
	assert.NotEmpty(t, ack.GetReason())

	r.mu.Lock()
	assert.Equal(t, "light", r.board.Sensors[0].Name, "mismatched device_id must not apply the push")
	r.mu.Unlock()
}

// TestHandleConfig_ManifestBeforeAck covers ordering: on an accepted push,
// the manifest publish must be recorded before the ack publish, so a
// consumer that reacts to the ack can never observe a stale manifest.
func TestHandleConfig_ManifestBeforeAck(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-ordering01")
	_, transport, _ := startedRunner(t, board)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, Name: "brightness"},
		},
	})

	transport.mu.Lock()
	defer transport.mu.Unlock()

	manifestIdx, ackIdx := -1, -1
	ackTopic := configAckTopic(board.DeviceID)
	mTopic := manifestTopic(board.DeviceID)
	for i, rec := range transport.publishes {
		if rec.topic == mTopic {
			manifestIdx = i // last manifest publish
		}
		if rec.topic == ackTopic {
			ackIdx = i // last ack publish
		}
	}
	require.NotEqual(t, -1, manifestIdx)
	require.NotEqual(t, -1, ackIdx)
	assert.Less(t, manifestIdx, ackIdx, "manifest publish must be recorded before the ack publish")
}

// TestHandleConfig_ReconnectConvergence exercises FR18's explicit
// reconnect-convergence callout end to end against the fake: connect,
// apply a rename at version 1, simulate a disconnect/reconnect (a second
// onConnect call, exactly as paho invokes it), and assert "online" +
// a retained manifest carrying the rename + a re-subscription to the config
// topic, then that the config version survived the reconnect (a re-push of
// version 1 rejected, version 2 accepted).
func TestHandleConfig_ReconnectConvergence(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-reconnect01")
	r, transport, _ := startedRunner(t, board)

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{MuxPath: lightMuxPath, I2CAddress: 35, Name: "brightness"},
		},
	})
	require.True(t, lastAck(t, transport, board.DeviceID).GetAccepted())

	statusT, manifestT, cfgT := statusTopic(board.DeviceID), manifestTopic(board.DeviceID), configTopic(board.DeviceID)

	countBefore := func() (online, manifest, subscribes int) {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		for _, rec := range transport.publishes {
			if rec.topic == statusT && string(rec.payload) == "online" {
				online++
			}
			if rec.topic == manifestT {
				manifest++
			}
		}
		for _, sub := range transport.subscribes {
			if sub.topic == cfgT {
				subscribes++
			}
		}
		return
	}
	onlineBefore, manifestBefore, subscribesBefore := countBefore()

	// Simulate a disconnect/reconnect: paho invokes onConnect again on every
	// reconnect, not just first connect.
	r.onConnect()

	onlineAfter, manifestAfter, subscribesAfter := countBefore()

	assert.Equal(t, onlineBefore+1, onlineAfter, "\"online\" must be republished on reconnect")
	assert.Equal(t, manifestBefore+1, manifestAfter, "manifest must be republished on reconnect")
	assert.Equal(t, subscribesBefore+1, subscribesAfter, "config topic must be re-subscribed on reconnect")

	manifest := latestManifest(t, transport, board.DeviceID)
	assert.True(t, manifestNames(manifest)["brightness"], "reconnect manifest must reflect the applied rename")

	// configVersion survived the reconnect: 1 is now stale, 2 is accepted.
	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{Version: 1})
	assert.False(t, lastAck(t, transport, board.DeviceID).GetAccepted(), "version 1 must still be rejected after reconnect")

	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{Version: 2})
	assert.True(t, lastAck(t, transport, board.DeviceID).GetAccepted(), "version survived the reconnect, so version 2 must be accepted")
}

// TestHandleConfig_RaceWithReadingLoop drives the config handler
// concurrently with the reading loop's own goroutine -- handleConfig fires
// on whatever goroutine delivers the message (paho's handler goroutine for
// a real broker; this test's own goroutine here) while publishDueReadings
// runs on the loop goroutine, exactly like production. Meaningful only
// under `bazel test --features=race` (or `go test -race`); a torn read/
// write here would show up as a corrupted topic name rather than a crash,
// which is exactly what the race detector -- not just this assertion --
// catches.
func TestHandleConfig_RaceWithReadingLoop(t *testing.T) {
	board := muxLightTempBoard(t, "leaflab-race01")
	r, transport, clock := startedRunner(t, board)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
				Version: i,
				Sensors: []*configpb.SensorConfig{
					{MuxPath: lightMuxPath, I2CAddress: 35, Name: "brightness", PollIntervalMs: 50},
					{MuxPath: shtMuxPath, I2CAddress: 68, SensorType: firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE, Enabled: proto.Bool(true)},
				},
			})
		}
	}()

	for i := 0; i < 200; i++ {
		clock.Tick()
	}
	close(stop)
	wg.Wait()

	// The test passes if it completes without the race detector firing and
	// without a panic; a final sanity read confirms state is still coherent.
	r.mu.Lock()
	assert.NotEmpty(t, r.board.Sensors)
	r.mu.Unlock()
}

// TestSubscribeConfig_RetriesTransientFailure pins subscribeConfig's
// bounded-retry behavior (config_apply.go, issue #2024): a transient
// Subscribe failure -- modeling the broker-side "queue still exists" race
// against a just-kicked prior connection's async teardown -- is retried
// with doubling backoff instead of leaving the board unsubscribed until the
// next reconnect.
func TestSubscribeConfig_RetriesTransientFailure(t *testing.T) {
	board := twoSensorBoard()
	r, transport, clock := newTestRunner(board, testDeps())

	// Fail the first subscribeRetryAttempts-1 attempts so the retry loop
	// must exhaust exactly one fewer than its budget before succeeding --
	// pins that success on the final attempt is still honored, not just
	// success-on-first-try.
	transport.failSubscribeNext(configTopic(board.DeviceID), subscribeRetryAttempts-1)

	require.NoError(t, r.Start())
	waitForTicker(t, clock)

	transport.mu.Lock()
	var attempts int
	for _, sub := range transport.subscribes {
		if sub.topic == configTopic(board.DeviceID) {
			attempts++
		}
	}
	transport.mu.Unlock()
	assert.Equal(t, subscribeRetryAttempts, attempts,
		"expected exactly subscribeRetryAttempts Subscribe calls (all-but-last failing, last succeeding)")

	assert.Equal(t, []time.Duration{subscribeRetryBaseBackoff, subscribeRetryBaseBackoff * 2}, clock.Sleeps(),
		"expected doubling backoff before each retry, none after the final (successful) attempt")

	// The eventual success must be a real, functional subscribe -- not just
	// a recorded attempt -- so a config push delivered afterward is still
	// handled and acked normally.
	pushConfig(t, transport, board.DeviceID, &configpb.DeviceConfig{
		Version: 1,
		Sensors: []*configpb.SensorConfig{
			{Name: "default-interval", Enabled: proto.Bool(false)},
		},
	})
	require.True(t, lastAck(t, transport, board.DeviceID).GetAccepted())
}

// TestSubscribeConfig_ExhaustsRetriesWithoutSubscribing pins the other half
// of #2024's bounded-retry behavior: when every attempt fails, subscribeConfig
// gives up after subscribeRetryAttempts (not indefinitely) and leaves the
// board genuinely unsubscribed until its next reconnect, rather than
// retrying forever or silently pretending to have subscribed.
func TestSubscribeConfig_ExhaustsRetriesWithoutSubscribing(t *testing.T) {
	board := twoSensorBoard()
	r, transport, clock := newTestRunner(board, testDeps())

	transport.failSubscribeNext(configTopic(board.DeviceID), subscribeRetryAttempts)

	require.NoError(t, r.Start())
	waitForTicker(t, clock)

	transport.mu.Lock()
	var attempts int
	for _, sub := range transport.subscribes {
		if sub.topic == configTopic(board.DeviceID) {
			attempts++
		}
	}
	transport.mu.Unlock()
	assert.Equal(t, subscribeRetryAttempts, attempts,
		"expected the retry loop to stop at subscribeRetryAttempts, not retry indefinitely")

	assert.Len(t, clock.Sleeps(), subscribeRetryAttempts-1,
		"expected one backoff sleep between each pair of attempts, none after the last failure")

	assert.Panics(t, func() {
		transport.deliver(configTopic(board.DeviceID), nil)
	}, "board must genuinely be unsubscribed after every retry attempt fails")
}
