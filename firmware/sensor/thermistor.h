#pragma once

// NTC thermistor sensor via ADC voltage divider.
//
// Implements ISensor. On ESP32, use an ADC1 pin (GPIO 32–39);
// ADC2 is unavailable when Wi-Fi is active.
//
// The ADC pin number doubles as the ISensor::address() identifier.

#include <cstdint>
#include <limits>

#include "firmware/sensor/sensor.h"
#include "firmware/sensor/thermistor_math.h"
#include "pw_status/status.h"

namespace firmware {

class ThermistorSensor final : public ISensor {
 public:
  explicit ThermistorSensor(uint8_t adc_pin,
                             thermistor::Config cfg = thermistor::Config{},
                             const char* sensor_name = "thermistor")
      : pin_(adc_pin), cfg_(cfg), name_(sensor_name) {}

  // Configures the ADC pin and resolution. Call once from setup().
  pw::Status Init() override;

  // Returns temperature in °C. Non-blocking; returns last valid reading on
  // transient ADC rail events. Returns NaN on persistent open/short circuit.
  float Read() override;

  const char* name() const override { return name_; }
  uint8_t address() const override { return pin_; }

 private:
  uint8_t pin_;
  thermistor::Config cfg_;
  const char* name_;
  float last_valid_ = std::numeric_limits<float>::quiet_NaN();
};

}  // namespace firmware
