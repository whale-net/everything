#pragma once

// LeafLabPublisher — serialises sensor data to proto and publishes over MQTT.
//
// Publishes to the LeafLab topic structure:
//   leaflab/<device_id>/status            — "online" on connect (LWT="offline")
//   leaflab/<device_id>/manifest          — DeviceManifest proto, retained
//   leaflab/<device_id>/sensor/<name>     — SensorReading proto, each loop
//
// Usage (call from the board config / main loop):
//
//   // On every transition to kReady:
//   publisher.OnConnect();
//
//   // Each loop() pass while kReady:
//   publisher.PublishReadings();

#include <cstdint>

#include "firmware/device_id/device_id.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"

namespace firmware {

class LeafLabPublisher {
 public:
  LeafLabPublisher(const IDeviceId& device_id,
                   pw::span<ISensor* const> sensors,
                   NetworkManager& net);

  // Publish "online" to status topic and manifest to manifest topic (retained).
  // Call once each time the network transitions to kReady.
  void OnConnect();

  // Read all sensors and publish SensorReading protos for valid readings.
  // Non-blocking. Call every loop() pass while kReady.
  void PublishReadings();

 private:
  void PublishManifest();
  void PublishStatus(const char* status);

  const IDeviceId& device_id_;
  pw::span<ISensor* const> sensors_;
  NetworkManager& net_;
};

}  // namespace firmware
