#pragma once

// BH1750 ambient light sensor — non-blocking II2CBus-backed implementation.
//
// Wiring: ADDR pin to GND → I2C address 0x23.
//         ADDR pin to VCC → I2C address 0x5C.
//
// Measurement mode: one-shot high-resolution (1 lx resolution, 180 ms window).
// Read() is non-blocking: it returns the cached lux value while the hardware
// is integrating, then retrieves the result and re-arms on the next call after
// kMeasureTimeMs has elapsed.
//
// Time source is injected at construction so the sensor is host-testable.
// On device, pass `millis` (Arduino). In tests, pass a stub function.

#include <cstdint>

#include "firmware/i2c/i2c_bus.h"
#include "firmware/sensor/sensor.h"
#include "pw_status/status.h"

namespace firmware {

class BH1750Sensor final : public ISensor {
 public:
  // clock_fn: returns elapsed time in milliseconds. Pass `millis` on device.
  BH1750Sensor(II2CBus& bus, uint8_t address, const char* name,
               uint32_t (*clock_fn)());

  // Powers on the sensor and triggers the first measurement.
  pw::Status Init() override;

  // Returns the last cached lux reading. Non-blocking.
  // After kMeasureTimeMs elapses, retrieves the result and re-arms.
  SensorReading Read() override;

  const char* name()    const override { return name_; }
  uint8_t address()     const override { return address_; }
  firmware_SensorType type() const override { return firmware_SensorType_SENSOR_TYPE_ILLUMINANCE; }
  SensorUnit unit()     const override { return SensorUnit::kLux; }

 private:
  pw::Status Trigger();

  II2CBus& bus_;
  uint8_t address_;
  const char* name_;
  uint32_t (*clock_fn_)();

  float last_lux_ = 0.0f;
  bool valid_ = false;
  bool init_ok_ = false;
  uint32_t trigger_ms_ = 0;

  static constexpr uint32_t kMeasureTimeMs = 180;
  static constexpr uint8_t kCmdPowerOn = 0x01;
  static constexpr uint8_t kCmdOneShot = 0x20;
};

}  // namespace firmware
