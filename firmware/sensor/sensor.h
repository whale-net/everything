#pragma once

#include <cstdint>

#include "pw_status/status.h"

namespace firmware {

// Matches leaflab.SensorType in proto/leaflab/leaflab.proto.
// Values must stay in sync with the proto enum.
enum class SensorType : uint32_t {
  kUnknown     = 0,
  kIlluminance = 1,
  kTemperature = 2,
  kHumidity    = 3,
};

// Typed return value from ISensor::Read().
// Bundles the measured value with a validity flag so callers never need to
// interpret sentinel floats (NaN, -1, etc.) to detect sensor failure.
struct SensorReading {
  float value;
  bool valid;

  static SensorReading Ok(float v) { return {v, true}; }
  static SensorReading Invalid()   { return {0.0f, false}; }
};

// ISensor — board-agnostic interface for any I2C (or other) sensor.
//
// Concrete implementations live beside the board's BUILD.bazel:
//   - firmware/sensor/bh1750.h          (BH1750 ambient light, II2CBus-backed)
//   - firmware/sensor/thermistor.h      (NTC thermistor, IAdc-backed)
//   - firmware/sensor/mock_sensor.h     (host-side tests)
//
// Compile-time dependency injection: board config file instantiates the
// correct concrete types and provides them via GetSensors().

class ISensor {
 public:
  virtual ~ISensor() = default;

  // Initialise the sensor hardware.  Called once from setup().
  // Returns OK on success, or a descriptive status on failure.
  virtual pw::Status Init() = 0;

  // Read the primary measurement value.
  // Must be non-blocking; never calls delay().
  // Returns SensorReading::Invalid() if no valid reading is available.
  virtual SensorReading Read() = 0;

  // Human-readable identifier, unique per device (e.g. "light_canopy").
  // Used as the MQTT sub-topic and manifest sensor name.
  // Must be a string literal with static lifetime (no heap allocation).
  virtual const char* name() const = 0;

  // Unique hardware address or channel identifier (e.g. I2C address).
  // Used for diagnostics and de-duplication.
  virtual uint8_t address() const = 0;

  // Sensor type and SI unit string, used to populate the device manifest.
  virtual SensorType   type() const = 0;
  virtual const char*  unit() const = 0;
};

}  // namespace firmware
