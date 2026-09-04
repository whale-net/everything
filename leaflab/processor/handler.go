package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
	"github.com/whale-net/everything/leaflab/configcompose"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// SensorRepository is the persistence interface used by MessageHandler.
type SensorRepository interface {
	UpsertBoard(ctx context.Context, deviceID string) (int64, error)
	UpsertSensorType(ctx context.Context, name, unit string) (int64, error)
	UpsertSensor(ctx context.Context, boardID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (int64, *int64, error)
	UpsertSensorLabel(ctx context.Context, sensorID int64, name string) error
	UpsertSensorHWHistory(ctx context.Context, sensorID int64, hw *HardwareAddress) error
	GetSensor(ctx context.Context, deviceID, sensorName string) (SensorInfo, bool, error)
	InsertReading(ctx context.Context, sensorID int64, regionID *int64, value float64, valid bool, uptimeS uint32, recordedAt time.Time, configVersion *int64) error
	UpsertDeviceConfig(ctx context.Context, boardID int64, version int64, configJSON []byte) error
	AckDeviceConfig(ctx context.Context, boardID int64, version int64, accepted bool, reason string) error
	ApplyConfigRegions(ctx context.Context, boardID int64, version int64) error
	SetSensorChipID(ctx context.Context, sensorID int64, chipModel string) error
	IsKnownChipAddress(ctx context.Context, chipModel string, i2cAddress uint32) (bool, error)

	// -- FR9/NFR4: process-internal corrective push convergence --------------
	// These back leaflab/processor/convergence.go's composition and storm
	// guards. Full desired-state inputs mirror leaflab/api/repository.go's
	// ListSensorInventoryForBoard/GetLatestAcceptedConfig (same data,
	// composed by the same shared leaflab/configcompose.ComposeDesiredSensors
	// function) so FR9's corrective push and FR8's caller-invoked push stay
	// byte-identical for the same DB state.

	// ListSensorInventoryForBoard returns boardID's current sensor
	// inventory -- one of ComposeDesiredSensors' three inputs.
	ListSensorInventoryForBoard(ctx context.Context, boardID int64) ([]configcompose.InventorySensor, error)
	// GetLatestAcceptedConfig returns deviceID's last accepted DeviceConfig
	// (nil if none yet) -- ComposeDesiredSensors' second input.
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	// GetCorrectivePushState reads NFR4's two guard columns for a sensor:
	// attempts bounds the sequential/reconnect-storm guard at 3;
	// outstandingVersion is non-nil while a corrective push for this sensor
	// is issued but not yet acked (the concurrent guard).
	GetCorrectivePushState(ctx context.Context, sensorID int64) (attempts int32, outstandingVersion *int64, err error)
	// InsertCorrectiveConfigNextVersion atomically assigns the next
	// device_config.version for boardID, inserts the corrective config row,
	// increments sensor.corrective_push_attempts, and sets
	// sensor.corrective_push_outstanding_version to that version -- one
	// transaction, using the same atomic conflict-safe next-version pattern
	// as leaflab/api/repository.go's InsertDeviceConfigNextVersion so a
	// user-initiated push and a corrective push for the same board can never
	// collide on one device_config.version.
	InsertCorrectiveConfigNextVersion(ctx context.Context, boardID, sensorID int64, configJSON []byte) (version int64, err error)
}

// correctivePublisher is the one *rmq.Publisher method the FR9 convergence
// path (leaflab/processor/convergence.go) calls, extracted the same way
// leaflab/api/server.go's configPublisher is -- so tests can substitute a
// fake without a live AMQP connection. *rmq.Publisher satisfies this as-is.
type correctivePublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, body interface{}) error
}

// MessageHandler decodes leaflab MQTT messages and persists them.
type MessageHandler struct {
	logger    *slog.Logger
	repo      SensorRepository
	cache     *SensorCache
	publisher correctivePublisher
}

func NewMessageHandler(logger *slog.Logger, repo SensorRepository, cache *SensorCache, publisher correctivePublisher) *MessageHandler {
	return &MessageHandler{logger: logger, repo: repo, cache: cache, publisher: publisher}
}

func (h *MessageHandler) Handle(ctx context.Context, msg rmq.Message) error {
	parts := strings.Split(msg.RoutingKey, ".")
	if len(parts) < 3 || parts[0] != "leaflab" {
		return &rmq.PermanentError{Err: fmt.Errorf("unexpected routing key: %s", msg.RoutingKey)}
	}

	deviceID := parts[1]

	switch {
	case len(parts) == 3 && parts[2] == "manifest":
		return h.handleManifest(ctx, deviceID, msg.Body)
	case len(parts) == 4 && parts[2] == "sensor":
		return h.handleSensorReading(ctx, deviceID, parts[3], msg.Body)
	case len(parts) == 3 && parts[2] == "config":
		return h.handleConfigPush(ctx, deviceID, msg.Body)
	case len(parts) == 4 && parts[2] == "config" && parts[3] == "ack":
		return h.handleConfigAck(ctx, deviceID, msg.Body)
	default:
		h.logger.Warn("unhandled routing key", "key", msg.RoutingKey)
		return nil
	}
}

// handleManifest upserts the board and all its sensors, then populates the
// cache.
//
// FR9 drift detection: before doing any of this manifest's own writes, it
// snapshots each already-known sensor's current DB display name (the same
// COALESCE(sensor_name_history.name, sensor.name) value
// ListSensorInventoryForBoard/ComposeDesiredSensors use). This snapshot is
// the baseline handleManifest's existing per-sensor writes are compared
// against below -- capturing it after those writes would be too late,
// since UpsertSensorLabel unconditionally records the device's own
// self-reported name into sensor_name_history (existing, unmodified
// behavior; see its own doc comment and #1772's "why this is safe" note).
// A difference between the snapshot and the device-reported name hands off
// to the convergence path (convergence.go); it does not change any of the
// existing upsert calls themselves.
func (h *MessageHandler) handleManifest(ctx context.Context, deviceID string, body []byte) error {
	var manifest firmwarepb.DeviceManifest
	if err := proto.Unmarshal(body, &manifest); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal DeviceManifest: %w", err)}
	}

	boardID, err := h.repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		return err
	}
	h.logger.Info("board registered", "device_id", deviceID, "board_id", boardID)

	priorNames, err := h.priorSensorNames(ctx, boardID)
	if err != nil {
		// Non-fatal: drift detection is best-effort on top of manifest
		// ingest, which must not fail because of it. No corrective pushes
		// will be triggered for this manifest, but ordinary ingest below
		// proceeds unaffected.
		h.logger.Warn("failed to load prior sensor names for drift detection", "device_id", deviceID, "err", err)
	}

	var firstErr error
	for _, sd := range manifest.Sensors {
		typeName := sensorTypeName(sd.Type)

		sensorTypeID, err := h.repo.UpsertSensorType(ctx, typeName, sd.Unit)
		if err != nil {
			h.logger.Error("failed to upsert sensor_type", "name", typeName, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		// Build hardware address. Firmware currently sends single-hop mux via
		// scalar fields; multi-hop will be added when the firmware proto is updated.
		var hw *HardwareAddress
		if sd.I2CAddress > 0 {
			hw = &HardwareAddress{I2CAddress: sd.I2CAddress}
			if sd.MuxAddress > 0 {
				hw.MuxPath = []MuxHop{{MuxAddress: sd.MuxAddress, MuxChannel: sd.MuxChannel}}
			}
		}

		sensorID, regionID, err := h.repo.UpsertSensor(ctx, boardID, sensorTypeID, sd.Name, sd.Unit, hw)
		if err != nil {
			h.logger.Error("failed to upsert sensor", "name", sd.Name, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		if err := h.repo.UpsertSensorLabel(ctx, sensorID, sd.Name); err != nil {
			h.logger.Warn("failed to upsert sensor label", "name", sd.Name, "err", err)
		}

		if err := h.repo.UpsertSensorHWHistory(ctx, sensorID, hw); err != nil {
			h.logger.Error("failed to upsert sensor hw history", "name", sd.Name, "err", err)
		}

		if err := h.repo.SetSensorChipID(ctx, sensorID, sd.ChipModel); err != nil {
			h.logger.Warn("failed to set sensor_chip_id", "name", sd.Name, "chip_model", sd.ChipModel, "err", err)
		}

		if sd.ChipModel != "" && sd.I2CAddress > 0 {
			if ok, err := h.repo.IsKnownChipAddress(ctx, sd.ChipModel, sd.I2CAddress); err != nil {
				h.logger.Warn("chip address check failed", "name", sd.Name, "err", err)
			} else if !ok {
				h.logger.Warn("sensor reports unrecognised address for chip — possible misconfiguration",
					"name", sd.Name,
					"chip_model", sd.ChipModel,
					"i2c_address", fmt.Sprintf("0x%02x", sd.I2CAddress),
				)
			}
		}

		// FR9: the device's self-reported name for this sensor has drifted
		// from what the DB held immediately before this manifest (the
		// snapshot taken above, before UpsertSensorLabel's unconditional
		// write). priorNames only has entries for sensors this board
		// already had on file, so a brand-new sensor's first manifest never
		// triggers this. Best-effort: a convergence failure is logged, not
		// propagated as firstErr -- it must not fail manifest ingest.
		if priorName, ok := priorNames[sensorID]; ok && priorName != sd.Name {
			if err := h.converge(ctx, deviceID, boardID, sensorID); err != nil {
				h.logger.Error("corrective config convergence failed",
					"device_id", deviceID, "sensor", sd.Name, "sensor_id", sensorID, "err", err)
			}
		}

		h.cache.Set(deviceID, sd.Name, SensorInfo{SensorID: sensorID, RegionID: regionID})
		h.logger.Info("sensor registered",
			"device_id", deviceID,
			"sensor", sd.Name,
			"type", typeName,
			"unit", sd.Unit,
			"sensor_id", sensorID,
			"region_id", regionID,
			"i2c_address", sd.I2CAddress,
			"mux_address", sd.MuxAddress,
			"mux_channel", sd.MuxChannel,
			"chip_model", sd.ChipModel,
		)
	}

	return firstErr
}

// priorSensorNames returns boardID's hardware-addressed sensors' current
// sensor_id -> display name, as of just before the calling manifest's own
// writes -- FR9 drift detection's baseline. Reuses
// ListSensorInventoryForBoard (ComposeDesiredSensors' own inventory input),
// so "current name" here means exactly what a corrective push would compose
// from: COALESCE(sensor_name_history.name, sensor.name). Sensors with no
// i2c_address are out of scope (ListSensorInventoryForBoard's own scope) --
// those are matched by name on upsert, so a device-side rename there
// creates a new row rather than drifting an existing one.
func (h *MessageHandler) priorSensorNames(ctx context.Context, boardID int64) (map[int64]string, error) {
	inventory, err := h.repo.ListSensorInventoryForBoard(ctx, boardID)
	if err != nil {
		return nil, fmt.Errorf("list sensor inventory for board %d: %w", boardID, err)
	}
	out := make(map[int64]string, len(inventory))
	for _, inv := range inventory {
		out[inv.SensorID] = inv.Name
	}
	return out, nil
}

// handleSensorReading writes a reading row.
func (h *MessageHandler) handleSensorReading(ctx context.Context, deviceID, sensorName string, body []byte) error {
	info, ok := h.cache.Get(deviceID, sensorName)
	if !ok {
		var err error
		info, ok, err = h.repo.GetSensor(ctx, deviceID, sensorName)
		if err != nil {
			return fmt.Errorf("cache miss DB lookup for %s/%s: %w", deviceID, sensorName, err)
		}
		if !ok {
			h.logger.Warn("reading dropped — sensor not in DB yet, manifest not received",
				"device_id", deviceID,
				"sensor", sensorName,
			)
			return nil
		}
		h.cache.Set(deviceID, sensorName, info)
	}

	var reading firmwarepb.SensorReading
	if err := proto.Unmarshal(body, &reading); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal SensorReading: %w", err)}
	}

	var configVersion *int64
	if v, ok := h.cache.GetConfigVersion(deviceID); ok {
		configVersion = &v
	}

	if err := h.repo.InsertReading(
		ctx,
		info.SensorID,
		info.RegionID,
		float64(reading.Value),
		true,
		reading.UptimeMs/1000,
		time.Now(),
		configVersion,
	); err != nil {
		return err
	}

	var cvLog any = "none"
	if configVersion != nil {
		cvLog = *configVersion
	}
	h.logger.Debug("reading written",
		"device_id", deviceID,
		"sensor", sensorName,
		"value", reading.Value,
		"uptime_s", reading.UptimeMs/1000,
		"config_version", cvLog,
	)
	return nil
}

// handleConfigPush records a DeviceConfig push observed on the broker.
func (h *MessageHandler) handleConfigPush(ctx context.Context, deviceID string, body []byte) error {
	var cfg configpb.DeviceConfig
	if err := proto.Unmarshal(body, &cfg); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal DeviceConfig: %w", err)}
	}

	if cfg.Version > 1<<63-1 {
		return &rmq.PermanentError{Err: fmt.Errorf("DeviceConfig.version %d overflows int64", cfg.Version)}
	}

	configJSON, err := protojson.Marshal(&cfg)
	if err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("protojson DeviceConfig: %w", err)}
	}

	boardID, err := h.repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		return err
	}
	if err := h.repo.UpsertDeviceConfig(ctx, boardID, int64(cfg.Version), configJSON); err != nil {
		return err
	}
	h.logger.Info("device_config recorded", "device_id", deviceID, "version", cfg.Version)
	return nil
}

// handleConfigAck records the device's ack for a config push.
// On acceptance, applies region assignments and updates the config version cache.
func (h *MessageHandler) handleConfigAck(ctx context.Context, deviceID string, body []byte) error {
	var ack configpb.DeviceConfigAck
	if err := proto.Unmarshal(body, &ack); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal DeviceConfigAck: %w", err)}
	}
	if ack.AppliedVersion > 1<<63-1 {
		return &rmq.PermanentError{Err: fmt.Errorf("DeviceConfigAck.applied_version %d overflows int64", ack.AppliedVersion)}
	}
	boardID, err := h.repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		return err
	}
	if err := h.repo.AckDeviceConfig(ctx, boardID, int64(ack.AppliedVersion), ack.Accepted, ack.Reason); err != nil {
		return err
	}
	if ack.Accepted {
		if err := h.repo.ApplyConfigRegions(ctx, boardID, int64(ack.AppliedVersion)); err != nil {
			h.logger.Warn("failed to apply config regions", "device_id", deviceID, "version", ack.AppliedVersion, "err", err)
		}
		h.cache.SetConfigVersion(deviceID, int64(ack.AppliedVersion))
		h.logger.Info("device_config acked", "device_id", deviceID, "version", ack.AppliedVersion)
	} else {
		h.logger.Warn("device rejected config",
			"device_id", deviceID,
			"version", ack.AppliedVersion,
			"reason", ack.Reason)
	}
	return nil
}

// sensorTypeName converts a proto SensorType to the DB name.
func sensorTypeName(t firmwarepb.SensorType) string {
	raw := t.String()
	name, _ := strings.CutPrefix(raw, "SENSOR_TYPE_")
	return strings.ToLower(name)
}
