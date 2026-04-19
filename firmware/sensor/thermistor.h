#pragma once

// NTC thermistor sensor via ADC voltage divider.
//
// Implements ISensor. On ESP32, use an ADC1 pin (GPIO 32–39);
// ADC2 is unavailable when Wi-Fi is active.
//
// The ADC pin number doubles as the ISensor::address() identifier.

#include <cstdint>

#include "firmware/adc/adc.h"
#include "firmware/sensor/sensor.h"
#include "firmware/sensor/thermistor_math.h"
#include "pw_status/status.h"

namespace firmware {

class ThermistorSensor final : public ISensor {
 public:
  explicit ThermistorSensor(uint8_t adc_pin, IAdc* adc,
                             thermistor::Config cfg = thermistor::Config{},
                             const char* sensor_name = "thermistor")
      : pin_(adc_pin), adc_(adc), cfg_(cfg), name_(sensor_name) {}

  // Initialises the ADC pin and syncs cfg_.adc_max from adc->max_value().
  // Call once from setup().
  pw::Status Init() override;

  // Returns temperature in °C. Non-blocking; returns last valid reading on
  // transient ADC rail events. Returns SensorReading::Invalid() before the
  // first successful read or on persistent open/short circuit.
  SensorReading Read() override;

  const char* name()    const override { return name_; }
  uint8_t address()     const override { return pin_; }
  SensorType  type()    const override { return SensorType::kTemperature; }
  const char* unit()    const override { return "C"; }

 private:
  uint8_t pin_;
  IAdc* adc_;
  thermistor::Config cfg_;
  const char* name_;
  float last_value_ = 0.0f;
  bool valid_ = false;
};

}  // namespace firmware
