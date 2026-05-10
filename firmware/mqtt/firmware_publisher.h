#pragma once

// FirmwarePublisher — serialises sensor data to proto and publishes over MQTT.
//
// Topics published:
//   leaflab/<device_id>/status            — "online" on connect (LWT="offline")
//   leaflab/<device_id>/manifest          — DeviceManifest proto, retained
//   leaflab/<device_id>/sensor/<name>     — SensorReading proto, each loop
//   leaflab/<device_id>/config/ack        — DeviceConfigAck proto (on config push)
//
// Topics subscribed:
//   leaflab/<device_id>/config            — DeviceConfig proto (from server)
//   leaflab/<device_id>/command           — text command ("reset", "factory_reset")
//
// Usage:
//   // On every transition to kReady:
//   publisher.OnConnect();
//
//   // Each loop() pass while kReady:
//   publisher.PublishReadings();
//   auto reset = publisher.ProcessPending();
//   if (reset != FirmwarePublisher::PendingReset::kNone) { ... esp_restart() ... }

#include <cstdint>

#include "firmware/config/config_applier.h"
#include "firmware/config/config_store.h"
#include "firmware/device_id/device_id.h"
#include "firmware/network/network_manager.h"
#include "firmware/proto/config.pb.h"
#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"

namespace firmware {

class FirmwarePublisher {
 public:
  enum class PendingReset { kNone, kSoft, kFactory };

  FirmwarePublisher(const IDeviceId& device_id,
                    NetworkManager& net,
                    ConfigApplier& config_applier,
                    ConfigStore* config_store = nullptr);

  // Subscribe to config + command topics, publish "online" status and manifest.
  // Call once each time the network transitions to kReady.
  void OnConnect();

  // Read enabled sensors and publish SensorReading protos for valid readings.
  // Non-blocking. Call every loop() pass while kReady.
  void PublishReadings();

  // Apply any pending config (I2C-safe — call from loop(), not a callback) and
  // return any pending reset type. kNone if nothing is pending.
  PendingReset ProcessPending();

  // Publish "offline" status. Call before executing a reset.
  void PublishOffline() { PublishStatus("offline"); }

 private:
  void PublishManifest();
  void PublishStatus(const char* status);
  void TrySubscriptions();
  void HandleConfigMessage(const uint8_t* payload, size_t length);
  void HandleCommandMessage(const uint8_t* payload, size_t length);
  void PublishConfigAck(uint64_t version, bool accepted, const char* reason);

  // Static trampoline — PubSubClient requires a bare function pointer.
  // Safe because there is exactly one FirmwarePublisher per device.
  static FirmwarePublisher* instance_;
  static void OnMQTTMessage(const char* topic, const uint8_t* payload,
                             size_t length);

  bool subscriptions_pending_ = true;

  // Config queued in the MQTT callback; applied from loop() via ProcessPending().
  bool                 config_pending_ = false;
  firmware_DeviceConfig pending_config_ = firmware_DeviceConfig_init_zero;

  PendingReset pending_reset_ = PendingReset::kNone;

  const IDeviceId& device_id_;
  NetworkManager&  net_;
  ConfigApplier&   config_applier_;
  ConfigStore*     config_store_;
};

}  // namespace firmware
