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
// Scaffold note: this establishes the compiling shape only. NFR4's two
// storm guards (concurrent-outstanding via GetCorrectivePushState's
// outstandingVersion, and sequential-storm via its attempts count against
// maxCorrectivePushAttempts), the WARNING/ERROR logging on the first two vs
// third failed attempt, and handleManifest's drift-detection call site are
// all Implementation-phase work -- see issue #1772 §§4-5. converge is not
// yet wired into handleManifest.
func (h *MessageHandler) converge(ctx context.Context, deviceID string, boardID, sensorID int64) error {
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
	// #1772/#1756 FR9's "why this is safe" note). The Implementation phase
	// wires NFR4's guards around this call; the composition itself is
	// already parity-guaranteed with FR8 by sharing ComposeDesiredSensors.
	composedSensors := configcompose.ComposeDesiredSensors(inventory, lastAcceptedSensors, nil)

	cfgProto := &configpb.DeviceConfig{
		DeviceId: deviceID,
		Sensors:  composedSensors,
	}
	return h.publishCorrectiveConfig(ctx, deviceID, boardID, sensorID, cfgProto)
}

// publishCorrectiveConfig marshals and publishes a composed corrective
// DeviceConfig, after atomically recording it via
// InsertCorrectiveConfigNextVersion. Routing key shape matches
// leaflab/api/server.go's PushDeviceConfig ("leaflab.<device_id>.config").
//
// Scaffold note: not yet called from handleManifest -- see converge's doc
// comment.
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
