#pragma once

// MQTTWriter — aggregates an array of ISensor* and publishes readings.
//
// Design goals:
//   - Zero heap allocation: sensor array is a fixed-size span, payloads
//     are formatted into a stack-allocated pw::StringBuffer.
//   - Board-agnostic: the actual publish() call is injected via
//     IPublisher (below), keeping this file compilable on host.
//   - Non-blocking: all operations complete in bounded time.
//
// Typical compile-time DI setup in main.cpp:
//
//   BME280Sensor temp_sensor(0x76);
//   SoilSensor   soil_sensor(0x48);
//   ISensor* sensors[] = { &temp_sensor, &soil_sensor };
//
//   RealPublisher publisher(&network_manager);
//   MQTTWriter writer(sensors, "home/greenhouse", &publisher);
//
//   // In loop() when timer fires:
//   writer.PublishAll();

#include <cstddef>
#include <cstdint>

#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"
#include "pw_status/status.h"
#include "pw_string/string_builder.h"

namespace firmware {

// IPublisher — thin abstraction over the actual MQTT send call.
// Real implementation wraps PubSubClient::publish().
// Test implementation captures the published messages.
class IPublisher {
 public:
  virtual ~IPublisher() = default;

  // Publish payload to topic.  Non-blocking.
  // Returns OK on success, Unavailable if not connected.
  virtual pw::Status Publish(const char* topic, const char* payload) = 0;
};

class MQTTWriter {
 public:
  // topic_prefix: e.g. "home/greenhouse" → publishes to
  //   "home/greenhouse/<sensor_name>"
  MQTTWriter(pw::span<ISensor* const> sensors,
             const char* topic_prefix,
             IPublisher* publisher)
      : sensors_(sensors),
        topic_prefix_(topic_prefix),
        publisher_(publisher) {}

  // Iterate all sensors, read each, publish "<prefix>/<name>" → "<value>".
  // Skips sensors whose Init() has not returned OK.
  // Returns the number of sensors successfully published.
  int PublishAll();

  // Initialise all sensors.  Call once from setup().
  // Returns number of sensors that initialised successfully.
  int InitAll();

 private:
  pw::span<ISensor* const> sensors_;
  const char* topic_prefix_;
  IPublisher* publisher_;

  // Track which sensors initialised successfully.
  // Max 16 sensors — adjust if needed, still stack-allocated.
  bool sensor_ok_[16] = {};
};

}  // namespace firmware
