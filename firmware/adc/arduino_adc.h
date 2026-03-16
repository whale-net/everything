#pragma once

// ArduinoAdc — IAdc implementation backed by the Arduino analogRead() API.
//
// Only compiles for device targets; target_compatible_with is enforced in
// BUILD.bazel. Do not include this header in host-compiled code or tests —
// use FakeAdc instead.

#include <cstdint>

#include "firmware/adc/adc.h"
#include "pw_status/status.h"

namespace firmware {

class ArduinoAdc final : public IAdc {
 public:
  // Configures 12-bit resolution (once, idempotent) and sets pin to INPUT.
  pw::Status Init(uint8_t pin) override;

  // Returns analogRead(pin).
  int Read(uint8_t pin) override;

  // Returns 4095 (12-bit ADC full-scale).
  int max_value() const override { return 4095; }
};

}  // namespace firmware
