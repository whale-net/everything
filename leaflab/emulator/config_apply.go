// config_apply.go implements FR18: consuming a DeviceConfig push, validating
// its version, applying name/enabled/poll_interval_ms overrides to matched
// sensors, re-publishing the retained DeviceManifest, and acking. See
// leaflab/MQTT.md for the wire flow this must match and this issue's body
// for the full spec (#1771).
package main

import (
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
)

func configTopic(deviceID string) string    { return fmt.Sprintf("leaflab/%s/config", deviceID) }
func configAckTopic(deviceID string) string { return fmt.Sprintf("leaflab/%s/config/ack", deviceID) }

// subscribeConfig subscribes to this board's config topic. Called from
// onConnect on every connect, including reconnects, so a config push lands
// (and is acked) the same way after a reconnect as on first connect.
func (r *Runner) subscribeConfig() {
	if err := r.transport.Subscribe(configTopic(r.board.DeviceID), 1, r.handleConfig); err != nil {
		r.deps.Logger.Warn("retried config subscribe", "device_id", r.board.DeviceID, "error", err)
	}
}

// handleConfig is the leaflab/<device_id>/config message handler: decode,
// validate device_id and version, apply matched sensor overrides, re-publish
// the manifest, then ack. Fires on whatever goroutine the transport delivers
// messages on (paho's own handler goroutine for a real broker) -- never the
// reading-loop goroutine -- so every read/write of board sensor state or
// configVersion goes through r.mu, and deps.Rand is never touched here (see
// reconcileLoopStateLocked).
func (r *Runner) handleConfig(_ string, payload []byte) {
	deviceID := r.board.DeviceID // immutable for the Runner's lifetime; safe unlocked

	var cfg configpb.DeviceConfig
	if err := proto.Unmarshal(payload, &cfg); err != nil {
		r.deps.Logger.Error("failed to decode DeviceConfig", "device_id", deviceID, "error", err)
		r.rejectConfig(deviceID, "malformed DeviceConfig")
		return
	}

	if incoming := cfg.GetDeviceId(); incoming != "" && incoming != deviceID {
		// The topic already scopes this to us, but the field is
		// authoritative per this issue's spec.
		r.deps.Logger.Warn("config push device_id mismatch", "device_id", deviceID, "config_device_id", incoming)
		r.rejectConfig(deviceID, "device_id mismatch")
		return
	}

	r.mu.Lock()
	if cfg.GetVersion() <= r.configVersion {
		current := r.configVersion
		r.mu.Unlock()
		reason := fmt.Sprintf("version %d is not greater than current %d", cfg.GetVersion(), current)
		r.deps.Logger.Warn("rejected config push: stale version", "device_id", deviceID, "pushed_version", cfg.GetVersion(), "current_version", current)
		r.publishAck(deviceID, current, false, reason)
		return
	}

	newSensors, rejectReason := applyConfig(r.board.Sensors, cfg.GetSensors(), r.deps.Logger, deviceID)
	if rejectReason != "" {
		current := r.configVersion
		r.mu.Unlock()
		r.deps.Logger.Warn("rejected config push", "device_id", deviceID, "reason", rejectReason)
		r.publishAck(deviceID, current, false, rejectReason)
		return
	}

	r.reconcileLoopStateLocked(r.board.Sensors, newSensors)
	r.board.Sensors = newSensors
	r.configVersion = cfg.GetVersion()
	board := r.board
	version := r.configVersion
	r.mu.Unlock()

	// Ordering matters: the server treats the ack as "apply finished", so
	// the manifest must land first or a consumer could read the ack and
	// then fetch a stale retained manifest.
	r.publishManifest(board)
	r.publishAck(deviceID, version, true, "")

	r.deps.Logger.Info("applied config push", "device_id", deviceID, "version", version)
}

// rejectConfig acks a rejection carrying the board's current (unchanged)
// applied version -- used by every reject path that runs before or without
// taking r.mu itself.
func (r *Runner) rejectConfig(deviceID, reason string) {
	r.mu.Lock()
	current := r.configVersion
	r.mu.Unlock()
	r.publishAck(deviceID, current, false, reason)
}

// publishAck marshals and publishes a DeviceConfigAck to
// leaflab/<device_id>/config/ack, QoS 1, non-retained -- a config push,
// accepted or not, always gets acked so leaflab-api is never left waiting.
func (r *Runner) publishAck(deviceID string, appliedVersion uint64, accepted bool, reason string) {
	ack := &configpb.DeviceConfigAck{
		DeviceId:       deviceID,
		AppliedVersion: appliedVersion,
		Accepted:       accepted,
		Reason:         reason,
	}
	payload, err := proto.Marshal(ack)
	if err != nil {
		r.deps.Logger.Error("failed to marshal DeviceConfigAck", "device_id", deviceID, "error", err)
		return
	}
	if err := r.transport.Publish(configAckTopic(deviceID), 1, false, payload); err != nil {
		r.deps.Logger.Warn("retried config ack publish", "device_id", deviceID, "error", err)
	}
}

// applyConfig computes the sensor set that would result from applying
// entries to current without mutating current (or any Sensor within it) --
// callers must be able to discard the result on rejection and leave state
// completely untouched. Returns a non-empty rejectReason if the push must be
// rejected in full (a resulting name collision); current is then unusable
// and must not be applied.
func applyConfig(current []Sensor, entries []*configpb.SensorConfig, logger *slog.Logger, deviceID string) ([]Sensor, string) {
	next := make([]Sensor, len(current))
	copy(next, current)

	for _, entry := range entries {
		idx, ok := matchSensorEntry(next, entry)
		if !ok {
			// Not an error: the server may push a config describing
			// hardware this board doesn't have (real-device behavior too).
			logger.Warn("config entry matched no sensor on this board",
				"device_id", deviceID,
				"i2c_address", entry.GetI2CAddress(),
				"sensor_type", entry.GetSensorType())
			continue
		}

		s := next[idx]
		if name := entry.GetName(); name != "" {
			s.Name = name
		}
		if entry.Enabled != nil {
			s.Enabled = entry.GetEnabled()
		}
		// poll_interval_ms has no "leave unchanged" sentinel distinct from
		// "use device default" (unlike enabled, it isn't an `optional`
		// field) -- 0 always reverts to the board default, non-zero always
		// becomes the override, per this issue's spec.
		s.PollIntervalMS = entry.GetPollIntervalMs()
		// region_id: server-side only, per the proto comment -- ignored.
		next[idx] = s
	}

	seen := make(map[string]struct{}, len(next))
	for _, s := range next {
		if _, dup := seen[s.Name]; dup {
			return nil, fmt.Sprintf("duplicate sensor name %s", s.Name)
		}
		seen[s.Name] = struct{}{}
	}

	return next, ""
}

// matchSensorEntry finds the index in sensors that entry targets, by
// (mux_address, mux_channel, i2c_address) -- mirroring resolveBoardSensors'
// single-hop-only model (board.go): entry.mux_path[0], or 0/0 for an empty
// mux_path. When more than one sensor shares that address (SHT3x, CCS811),
// entry.sensor_type discriminates; with no discriminator, or one that
// matches none of the candidates, the match is ambiguous and reported as
// not-found (logged and skipped by the caller) rather than guessed at.
func matchSensorEntry(sensors []Sensor, entry *configpb.SensorConfig) (int, bool) {
	var muxAddress, muxChannel uint32
	if hops := entry.GetMuxPath(); len(hops) > 0 {
		muxAddress = hops[0].GetMuxAddress()
		muxChannel = hops[0].GetMuxChannel()
	}
	i2cAddress := entry.GetI2CAddress()

	var matches []int
	for i, s := range sensors {
		if s.MuxAddress == muxAddress && s.MuxChannel == muxChannel && s.I2CAddress == i2cAddress {
			matches = append(matches, i)
		}
	}

	switch len(matches) {
	case 0:
		return -1, false
	case 1:
		return matches[0], true
	default:
		if entry.GetSensorType() != firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN {
			for _, i := range matches {
				if sensors[i].SensorType == entry.GetSensorType() {
					return i, true
				}
			}
		}
		return -1, false
	}
}
