#pragma once

#include <cstdint>

#include "firmware/i2c/i2c_bus.h"
#include "firmware/proto/firmware.pb.h"
#include "pw_status/status.h"

namespace firmware {

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

// Canonical SI unit for a sensor measurement.
// Add new values here when a new physical quantity is introduced.
// UnitString() maps each value to its wire-format string.
enum class SensorUnit {
  kUnknown          = 0,
  kLux              = 1,  // lx   — illuminance
  kCelsius          = 2,  // °C   — temperature
  kRelativeHumidity = 3,  // %RH  — relative humidity
  kPPM              = 4,  // ppm  — parts per million (eCO2)
  kPPB              = 5,  // ppb  — parts per billion (TVOC)
};

inline const char* UnitString(SensorUnit u) {
  switch (u) {
    case SensorUnit::kLux:              return "lx";
    case SensorUnit::kCelsius:          return "\xc2\xb0""C";  // °C (UTF-8)
    case SensorUnit::kRelativeHumidity: return "%RH";
    case SensorUnit::kPPM:              return "ppm";
    case SensorUnit::kPPB:              return "ppb";
    default:                            return "";
  }
}

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
  virtual const char* name() const = 0;

  // Override the logical name at runtime (from a DeviceConfig push).
  // Returns true if the sensor has a mutable name buffer and the override
  // was applied. Returns false if the name is a compile-time constant.
  virtual bool SetName(const char* /*name*/) { return false; }

  // Unique hardware address or channel identifier (e.g. I2C address).
  virtual uint8_t address() const = 0;

  // Sensor type and unit, used to populate the device manifest.
  virtual firmware_SensorType type() const = 0;
  virtual SensorUnit           unit() const = 0;

  // Depth of the mux chain from root bus to this sensor.
  // 0 = sensor is directly on the root bus.
  virtual size_t mux_depth() const { return 0; }

  // Returns the MuxHop at the given depth (0 = outermost mux).
  // Undefined if depth >= mux_depth().
  virtual MuxHop mux_hop(size_t /*depth*/) const { return {0, 0}; }

  // Convenience: innermost mux address/channel (single-level compat).
  uint8_t mux_address() const {
    return mux_depth() > 0 ? mux_hop(mux_depth() - 1).address : 0;
  }
  uint8_t mux_channel() const {
    return mux_depth() > 0 ? mux_hop(mux_depth() - 1).channel : 0;
  }
};

}  // namespace firmware
