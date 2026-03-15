#pragma once

#include <cstdint>

#include "pw_status/status.h"

namespace firmware {

// ISensor — board-agnostic interface for any I2C (or other) sensor.
//
// Concrete implementations live beside the board's BUILD.bazel:
//   - firmware/sensor/bme280_sensor.h   (real, ESP32 target)
//   - firmware/sensor/mock_sensor.h     (host-side tests)
//
// Compile-time dependency injection: main.cpp for a specific board
// instantiates the correct concrete types and passes them to MQTTWriter.

class ISensor {
 public:
  virtual ~ISensor() = default;

  // Initialise the sensor hardware.  Called once from setup().
  // Returns OK on success, or a descriptive status on failure.
  virtual pw::Status Init() = 0;

  // Read the primary measurement value.
  // Must be non-blocking; never calls delay().
  // Returns the last valid reading if hardware is temporarily unavailable.
  virtual float Read() = 0;

  // Human-readable identifier used as the MQTT sub-topic.
  // Must be a string literal with static lifetime (no heap allocation).
  virtual const char* name() const = 0;

  // Unique hardware address or channel identifier (e.g. I2C address).
  // Used for diagnostics and de-duplication.
  virtual uint8_t address() const = 0;
};

}  // namespace firmware
