package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/whale-net/everything/libs/go/rmq"
	firmwarepb "github.com/whale-net/everything/firmware/proto"
	"google.golang.org/protobuf/proto"
)

// MessageHandler decodes and logs leaflab MQTT messages.
// Routing key format (MQTT '/' → AMQP '.'):
//   leaflab.<device_id>.sensor.<sensor_name>  → SensorReading
//   leaflab.<device_id>.manifest              → DeviceManifest
type MessageHandler struct {
	logger *slog.Logger
}

func NewMessageHandler(logger *slog.Logger) *MessageHandler {
	return &MessageHandler{logger: logger}
}

func (h *MessageHandler) Handle(ctx context.Context, msg rmq.Message) error {
	parts := strings.Split(msg.RoutingKey, ".")
	if len(parts) < 3 || parts[0] != "leaflab" {
		return fmt.Errorf("unexpected routing key: %s", msg.RoutingKey)
	}

	deviceID := parts[1]

	switch {
	case len(parts) == 3 && parts[2] == "manifest":
		return h.handleManifest(deviceID, msg.Body)
	case len(parts) == 4 && parts[2] == "sensor":
		return h.handleSensorReading(deviceID, parts[3], msg.Body)
	default:
		h.logger.Warn("unhandled routing key", "key", msg.RoutingKey)
		return nil
	}
}

func (h *MessageHandler) handleManifest(deviceID string, body []byte) error {
	var manifest firmwarepb.DeviceManifest
	if err := proto.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("failed to unmarshal DeviceManifest: %w", err)
	}

	sensorNames := make([]string, 0, len(manifest.Sensors))
	for _, s := range manifest.Sensors {
		sensorNames = append(sensorNames, fmt.Sprintf("%s(%s)", s.Name, s.Unit))
	}

	h.logger.Info("manifest",
		"device_id", deviceID,
		"sensors", strings.Join(sensorNames, ", "),
	)
	return nil
}

func (h *MessageHandler) handleSensorReading(deviceID, sensorName string, body []byte) error {
	var reading firmwarepb.SensorReading
	if err := proto.Unmarshal(body, &reading); err != nil {
		return fmt.Errorf("failed to unmarshal SensorReading: %w", err)
	}

	h.logger.Info("reading",
		"device_id", deviceID,
		"sensor", sensorName,
		"value", reading.Value,
		"uptime_ms", reading.UptimeMs,
	)
	return nil
}
