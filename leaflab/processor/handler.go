package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/canonkey"
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
	GetSensorsByBoard(ctx context.Context, boardID int64) ([]SensorState, error)
	GetAffectedSensorsForConfig(ctx context.Context, boardID int64, version int64) ([]AffectedSensor, error)
}

// MessageHandler decodes leaflab MQTT messages and persists them.
type MessageHandler struct {
	logger      *slog.Logger
	repo        SensorRepository
	cache       *SensorCache
	invalidator CacheInvalidator
}

func NewMessageHandler(logger *slog.Logger, repo SensorRepository, cache *SensorCache, invalidator CacheInvalidator) *MessageHandler {
	return &MessageHandler{logger: logger, repo: repo, cache: cache, invalidator: invalidator}
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

// handleManifest upserts the board and all its sensors, then populates the cache.
// Before processing, it detects and rejects any hardware key swaps.
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

	// Fetch existing sensors to detect swaps before processing
	existingSensors, err := h.repo.GetSensorsByBoard(ctx, boardID)
	if err != nil {
		h.logger.Warn("failed to fetch existing sensors for swap detection", "board_id", boardID, "err", err)
		// Don't fail the manifest processing if we can't detect swaps;
		// just log and continue
	}

	// Build a map of manifest sensors with their type IDs
	manifestSensors := make(map[string]*ManifestSensorInfo)
	sensorTypeIDs := make(map[string]int64) // sensor name -> type ID

	// First pass: get/create all sensor types
	for _, sd := range manifest.Sensors {
		typeName := sensorTypeName(sd.Type)

		sensorTypeID, err := h.repo.UpsertSensorType(ctx, typeName, sd.Unit)
		if err != nil {
			h.logger.Error("failed to upsert sensor_type", "name", typeName, "err", err)
			// Continue anyway, we'll hit this error again in the main loop
		} else {
			sensorTypeIDs[sd.Name] = sensorTypeID

			// Build hardware address
			var hw *HardwareAddress
			if sd.I2CAddress > 0 {
				hw = &HardwareAddress{I2CAddress: sd.I2CAddress}
				if sd.MuxAddress > 0 {
					hw.MuxPath = []MuxHop{{MuxAddress: sd.MuxAddress, MuxChannel: sd.MuxChannel}}
				}
			}

			manifestSensors[sd.Name] = &ManifestSensorInfo{
				Name:   sd.Name,
				TypeID: sensorTypeID,
				HW:     hw,
			}
		}
	}

	// Detect swaps
	if existingSensors != nil && len(existingSensors) > 0 && len(manifestSensors) > 0 {
		swapped, sensor1, sensor2, conflictKey := DetectSwap(existingSensors, manifestSensors)
		if swapped {
			msg := fmt.Sprintf("hardware key swap detected in manifest: sensors %q and %q would exchange keys (conflicting key: %s). A swap must be refused or handled atomically; it cannot result in forked sensor identities.",
				sensor1, sensor2, conflictKey)
			h.logger.Error("manifest rejected", "reason", msg, "device_id", deviceID, "board_id", boardID)
			return &rmq.PermanentError{Err: fmt.Errorf(msg)}
		}
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

	// Canonicalize sensors on ingress at the proto/JSON boundary
	for _, sensor := range cfg.Sensors {
		if err := canonkey.ValidateAndCanonicalizeSensorConfig(sensor); err != nil {
			h.logger.Warn("invalid sensor config in DeviceConfig push", "device_id", deviceID, "version", cfg.Version, "err", err)
			// Continue processing despite invalid sensor; log the issue but don't fail the whole push
		}
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
		// TODO(#1203): Publish cache invalidation signals for affected sensors.
		// ApplyConfigRegions applies region assignments; we need to fetch which sensors
		// were affected and publish invalidation signals for each.
		if err := h.publishRegionInvalidations(ctx, boardID, deviceID, int64(ack.AppliedVersion)); err != nil {
			h.logger.Warn("failed to publish cache invalidation signals", "device_id", deviceID, "err", err)
		}
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

// publishRegionInvalidations queries the config to find which sensors had region
// assignments updated, then publishes cache invalidation signals to all subscribers.
// This ensures that all cached views (in-process and cross-process) are invalidated
// before the next reading is processed.
func (h *MessageHandler) publishRegionInvalidations(ctx context.Context, boardID int64, deviceID string, version int64) error {
	// Get the list of sensors affected by this config version
	affected, err := h.repo.GetAffectedSensorsForConfig(ctx, boardID, version)
	if err != nil {
		h.logger.Warn("failed to get affected sensors for config version",
			"board_id", boardID,
			"version", version,
			"err", err,
		)
		return err
	}

	// Publish a cache invalidation signal for each affected sensor
	for _, sensor := range affected {
		signal := CacheInvalidationSignal{
			CommittedAt: time.Now(),
			DeviceID:    sensor.DeviceID,
			SensorID:    sensor.SensorID,
			RegionID:    sensor.RegionID,
			ChangeType:  "region",
		}

		if err := h.invalidator.PublishInvalidation(ctx, signal); err != nil {
			h.logger.Error("failed to publish region invalidation signal",
				"device_id", sensor.DeviceID,
				"sensor_id", sensor.SensorID,
				"region_id", sensor.RegionID,
				"err", err,
			)
			// Don't return early; publish all signals even if one fails
			// but do log the error
			continue
		}

		h.logger.Debug("published region invalidation signal",
			"device_id", sensor.DeviceID,
			"sensor_id", sensor.SensorID,
			"region_id", sensor.RegionID,
		)
	}

	return nil
}
