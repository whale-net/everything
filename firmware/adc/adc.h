#pragma once

// IAdc — board-agnostic ADC interface.
//
// Concrete implementations:
//   ArduinoAdc  — wraps analogRead(); device-only (target_compatible_with enforced in BUILD)
//   FakeAdc     — preset per-pin values; host-side tests
//
// Usage in firmware:
//
//   ArduinoAdc adc;
//   ThermistorSensor thermistor(board::kAdc0, &adc);
//
//   void setup() {
//       thermistor.Init();  // calls adc.Init(pin) internally
//   }
//
//   void loop() {
//       float t = thermistor.Read();
//   }

#include <cstdint>

#include "pw_status/status.h"

namespace firmware {

class IAdc {
 public:
  virtual ~IAdc() = default;

  // Initialise the given ADC channel/pin. Call once per pin from setup().
  virtual pw::Status Init(uint8_t pin) = 0;

  // Read raw ADC count for pin. Range: 0 .. max_value() inclusive.
  virtual int Read(uint8_t pin) = 0;

  // Full-scale count (e.g. 4095 for 12-bit, 1023 for 10-bit).
  virtual int max_value() const = 0;
};

}  // namespace firmware
