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
//
// Usage:
//   // On every transition to kReady:
//   publisher.OnConnect();
//
//   // Each loop() pass while kReady:
//   publisher.PublishReadings();

#include <cstdint>

#include "firmware/config/config_applier.h"
#include "firmware/config/config_store.h"
#include "firmware/device_id/device_id.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"

namespace firmware {

class FirmwarePublisher {
 public:
  // sensors: compile-time sensor list (static board targets).
  //          Pass an empty span for dynamic targets — sensors come from config_applier.
  FirmwarePublisher(const IDeviceId& device_id,
                    NetworkManager& net,
                    ConfigApplier& config_applier,
                    ConfigStore* config_store = nullptr);

  // Subscribe to the config topic, publish "online" status, and publish
  // the device manifest. Call once each time the network transitions to kReady.
  void OnConnect();

  // Read enabled sensors and publish SensorReading protos for valid readings.
  // Non-blocking. Call every loop() pass while kReady.
  void PublishReadings();

 private:
  void PublishManifest();
  void PublishStatus(const char* status);
  void TryConfigSubscribe();
  void HandleConfigMessage(const uint8_t* payload, size_t length);
  void PublishConfigAck(uint64_t version, bool accepted, const char* reason);

  // Static trampoline — PubSubClient requires a bare function pointer.
  // Safe because there is exactly one FirmwarePublisher per device.
  static FirmwarePublisher* instance_;
  static void OnMQTTMessage(const char* topic, const uint8_t* payload,
                             size_t length);

  // True until the config topic subscription is confirmed. PublishReadings()
  // retries Subscribe() on each call until it succeeds.
  bool config_subscribe_pending_ = true;

  const IDeviceId& device_id_;
  NetworkManager&  net_;
  ConfigApplier&   config_applier_;
  ConfigStore*     config_store_;
};

}  // namespace firmware
