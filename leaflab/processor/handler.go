package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/protobuf/proto"
)

// SensorRepository is the persistence interface used by MessageHandler.
// *Repository satisfies this interface; tests use stub implementations.
type SensorRepository interface {
	UpsertBoard(ctx context.Context, deviceID string) (int64, error)
	UpsertSensorType(ctx context.Context, name, unit string) (int64, error)
	UpsertSensor(ctx context.Context, boardID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (int64, *int64, error)
	UpsertSensorHWHistory(ctx context.Context, sensorID int64, hw *HardwareAddress) error
	GetSensor(ctx context.Context, deviceID, sensorName string) (SensorInfo, bool, error)
	InsertReading(ctx context.Context, sensorID int64, regionID *int64, value float64, valid bool, uptimeMs uint32, recordedAt time.Time) error
}

// MessageHandler decodes leaflab MQTT messages and persists them.
// Routing key format (MQTT '/' → AMQP '.'):
//
//	leaflab.<device_id>.manifest             → DeviceManifest
//	leaflab.<device_id>.sensor.<name>        → SensorReading
type MessageHandler struct {
	logger *slog.Logger
	repo   SensorRepository
	cache  *SensorCache
}

func NewMessageHandler(logger *slog.Logger, repo SensorRepository, cache *SensorCache) *MessageHandler {
	return &MessageHandler{logger: logger, repo: repo, cache: cache}
}

func (h *MessageHandler) Handle(ctx context.Context, msg rmq.Message) error {
	parts := strings.Split(msg.RoutingKey, ".")
	if len(parts) < 3 || parts[0] != "leaflab" {
		return fmt.Errorf("unexpected routing key: %s", msg.RoutingKey)
	}

	deviceID := parts[1]

	switch {
	case len(parts) == 3 && parts[2] == "manifest":
		return h.handleManifest(ctx, deviceID, msg.Body)
	case len(parts) == 4 && parts[2] == "sensor":
		return h.handleSensorReading(ctx, deviceID, parts[3], msg.Body)
	default:
		h.logger.Warn("unhandled routing key", "key", msg.RoutingKey)
		return nil
	}
}

// handleManifest upserts the board and all its sensors, then populates the cache.
func (h *MessageHandler) handleManifest(ctx context.Context, deviceID string, body []byte) error {
	var manifest firmwarepb.DeviceManifest
	if err := proto.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("unmarshal DeviceManifest: %w", err)
	}

	boardID, err := h.repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		return err
	}
	h.logger.Info("board registered", "device_id", deviceID, "board_id", boardID)

	for _, sd := range manifest.Sensors {
		typeName := sensorTypeName(sd.Type)

		sensorTypeID, err := h.repo.UpsertSensorType(ctx, typeName, sd.Unit)
		if err != nil {
			h.logger.Error("failed to upsert sensor_type", "name", typeName, "err", err)
			continue
		}

		var hw *HardwareAddress
		if sd.I2CAddress > 0 {
			hw = &HardwareAddress{
				I2CAddress: sd.I2CAddress,
				MuxAddress: sd.MuxAddress,
				MuxChannel: sd.MuxChannel,
			}
		}

		sensorID, regionID, err := h.repo.UpsertSensor(ctx, boardID, sensorTypeID, sd.Name, sd.Unit, hw)
		if err != nil {
			h.logger.Error("failed to upsert sensor", "name", sd.Name, "err", err)
			continue
		}

		if err := h.repo.UpsertSensorHWHistory(ctx, sensorID, hw); err != nil {
			h.logger.Error("failed to upsert sensor hw history", "name", sd.Name, "err", err)
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
		)
	}

	return nil
}

// handleSensorReading writes a reading row. Drops the message if the sensor is
// not yet in the cache (manifest not yet received for this device).
func (h *MessageHandler) handleSensorReading(ctx context.Context, deviceID, sensorName string, body []byte) error {
	info, ok := h.cache.Get(deviceID, sensorName)
	if !ok {
		// Cache miss — look up in DB (handles the case where the processor
		// restarted after the device sent its retained manifest).
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
		// Warm the cache so the next reading doesn't hit the DB.
		h.cache.Set(deviceID, sensorName, info)
	}

	var reading firmwarepb.SensorReading
	if err := proto.Unmarshal(body, &reading); err != nil {
		return fmt.Errorf("unmarshal SensorReading: %w", err)
	}

	if err := h.repo.InsertReading(
		ctx,
		info.SensorID,
		info.RegionID,
		float64(reading.Value),
		true,
		reading.UptimeMs,
		time.Now(),
	); err != nil {
		return err
	}

	h.logger.Debug("reading written",
		"device_id", deviceID,
		"sensor", sensorName,
		"value", reading.Value,
		"uptime_ms", reading.UptimeMs,
	)
	return nil
}

// sensorTypeName converts a proto SensorType enum value to the DB name.
// Strips the "SENSOR_TYPE_" prefix and lowercases the result.
// e.g. SENSOR_TYPE_ILLUMINANCE → "illuminance"
func sensorTypeName(t firmwarepb.SensorType) string {
	raw := t.String() // e.g. "SENSOR_TYPE_ILLUMINANCE"
	name, _ := strings.CutPrefix(raw, "SENSOR_TYPE_")
	return strings.ToLower(name)
}
