#pragma once

// ConfigApplier — matches DeviceConfig entries to hardware sensors by their
// (mux_path, i2c_address) identity and applies name overrides and
// enabled/disabled state.
//
// Hardware sensors are identified at compile time; their I2C address and mux
// path are physical facts. ConfigApplier bridges the runtime config to them.
//
// Host-compilable: no Arduino dependency.

#include <cstddef>
#include <cstdint>

#include "firmware/proto/config.pb.h"
#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"

namespace firmware {

class ConfigApplier {
 public:
  explicit ConfigApplier(pw::span<ISensor* const> sensors);

  // Apply name overrides and enabled/poll settings from cfg to the sensor
  // array. Sensors with no matching SensorConfig entry are left unchanged
  // (name keeps its compile-time default; enabled defaults to true).
  void Apply(const firmware_DeviceConfig& cfg);

  // True if the sensor at index i is currently enabled.
  // Sensors not mentioned in any applied config are enabled by default.
  bool IsEnabled(size_t index) const;

  // Per-sensor poll interval in ms. Returns 0 if the device default applies.
  uint32_t PollIntervalMs(size_t index) const;

 private:
  static constexpr size_t kMaxSensors = 16;

  pw::span<ISensor* const> sensors_;
  bool enabled_[kMaxSensors];
  uint32_t poll_ms_[kMaxSensors];
  bool initialized_ = false;
};

}  // namespace firmware
