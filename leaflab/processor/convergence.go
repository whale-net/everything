package main

import (
	"context"
	"fmt"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/configcompose"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// correctiveConfigExchange is the AMQP exchange the corrective push is
// published to -- the same exchange leaflab/api/server.go's PushDeviceConfig
// uses (mqttExchange there).
const correctiveConfigExchange = "amq.topic"

// maxCorrectivePushAttempts is NFR4's sequential/reconnect-storm guard
// bound: leaflab-processor stops auto-issuing corrective pushes for a
// sensor after this many acked-but-unconverged reconnect-triggered
// round trips.
const maxCorrectivePushAttempts = 3

// converge composes and publishes a corrective DeviceConfig for a single
// sensor whose device-reported name (handleManifest's drift detection, FR9)
// differs from the database's current name for that sensor. The composed
// sensor list uses leaflab/configcompose.ComposeDesiredSensors -- the same
// function, not a copy, that leaflab/api's PushDeviceConfig (FR8) uses --
// so the two call sites are guaranteed to produce byte-identical output for
// the same DB state (composition parity, FR9's explicit testable
// requirement).
//
// Both of NFR4's storm guards are enforced here, in order, before any
// composition/publish work happens:
//
//  1. Concurrent guard: if a corrective push for this sensor is already
//     outstanding (issued, not yet acked), do not issue a second one.
//  2. Sequential/reconnect-storm guard: attempts counts reconnect-triggered
//     corrective pushes already issued for this sensor that failed to
//     converge (the device acked accepted=true each time -- clearing the
//     outstanding marker -- but its next manifest still reported the stale
//     name, meaning it never persisted the correction to NVS). At
//     maxCorrectivePushAttempts, stop auto-issuing and log ERROR (giving
//     up, per AGENTS.md § Logging Levels); the first two detections of an
//     unconverged prior attempt log WARNING instead (the system retried to
//     keep going). Both counters live in Postgres (GetCorrectivePushState),
//     never in-memory -- see #1772 §6 / NFR4 "Counter persistence".
func (h *MessageHandler) converge(ctx context.Context, deviceID string, boardID, sensorID int64) error {
	attempts, outstandingVersion, err := h.repo.GetCorrectivePushState(ctx, sensorID)
	if err != nil {
		return fmt.Errorf("get corrective push state for sensor %d: %w", sensorID, err)
	}

	if outstandingVersion != nil {
		// Concurrent guard: a corrective push for this sensor is already in
		// flight. The next manifest after it acks will re-evaluate drift
		// (and, if still drifted, the sequential guard below) on its own.
		h.logger.Info("corrective push already outstanding for sensor, not issuing another",
			"device_id", deviceID, "sensor_id", sensorID, "outstanding_version", *outstandingVersion)
		return nil
	}

	if attempts >= maxCorrectivePushAttempts {
		h.logger.Error("giving up on corrective push convergence: device is not persisting the correction",
			"device_id", deviceID, "sensor_id", sensorID, "attempts", attempts)
		return nil
	}
	if attempts > 0 {
		h.logger.Warn("prior corrective push did not converge, retrying",
			"device_id", deviceID, "sensor_id", sensorID, "attempts", attempts)
	}

	inventory, err := h.repo.ListSensorInventoryForBoard(ctx, boardID)
	if err != nil {
		return fmt.Errorf("list sensor inventory for board %d: %w", boardID, err)
	}

	lastAccepted, err := h.repo.GetLatestAcceptedConfig(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("get latest accepted config for %s: %w", deviceID, err)
	}
	var lastAcceptedSensors []*configpb.SensorConfig
	if lastAccepted != nil {
		lastAcceptedSensors = lastAccepted.Sensors
	}

	// FR9's corrective push supplies no caller-content overrides at all --
	// every value composed comes from already-committed DB state (see
	// #1772/#1756 FR9's "why this is safe" note). Composition parity with
	// FR8 holds by construction: this is the same
	// configcompose.ComposeDesiredSensors call leaflab/api's PushDeviceConfig
	// makes, not a copy.
	composedSensors := configcompose.ComposeDesiredSensors(inventory, lastAcceptedSensors, nil)

	cfgProto := &configpb.DeviceConfig{
		DeviceId: deviceID,
		Sensors:  composedSensors,
	}
	return h.publishCorrectiveConfig(ctx, deviceID, boardID, sensorID, cfgProto)
}

// publishCorrectiveConfig marshals and publishes a composed corrective
// DeviceConfig, after atomically recording it via
// InsertCorrectiveConfigNextVersion (which also increments
// sensor.corrective_push_attempts and sets
// sensor.corrective_push_outstanding_version -- the NFR4 guard state
// converge's callers check next time). Routing key shape matches
// leaflab/api/server.go's PushDeviceConfig ("leaflab.<device_id>.config").
func (h *MessageHandler) publishCorrectiveConfig(ctx context.Context, deviceID string, boardID, sensorID int64, cfgProto *configpb.DeviceConfig) error {
	// configJSON (the device_config.config_json column) is protojson, same
	// as leaflab/api/repository.go's InsertDeviceConfigNextVersion stores;
	// the wire payload published below is binary proto, same as
	// leaflab/api/server.go's PushDeviceConfig.
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		return fmt.Errorf("marshal corrective config for %s: %w", deviceID, err)
	}

	version, err := h.repo.InsertCorrectiveConfigNextVersion(ctx, boardID, sensorID, configJSON)
	if err != nil {
		return fmt.Errorf("record corrective config push for board %d: %w", boardID, err)
	}

	cfgProto.Version = uint64(version)
	wire, err := proto.Marshal(cfgProto)
	if err != nil {
		return fmt.Errorf("marshal corrective config wire payload for %s: %w", deviceID, err)
	}

	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(deviceID, "/", "."))
	if err := h.publisher.Publish(ctx, correctiveConfigExchange, routingKey, wire); err != nil {
		return fmt.Errorf("publish corrective config for %s: %w", deviceID, err)
	}

	h.logger.Info("corrective config pushed",
		"device_id", deviceID,
		"sensor_id", sensorID,
		"version", version,
		"sensors", len(cfgProto.GetSensors()),
	)
	return nil
}
