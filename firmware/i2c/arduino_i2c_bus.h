#pragma once

// ArduinoI2CBus — II2CBus implementation backed by the Arduino Wire library.
//
// Only compiles for device targets; target_compatible_with is enforced in
// BUILD.bazel. Do not include this header in host-compiled code or tests —
// use FakeI2CBus instead.

#include <cstdint>

#include "firmware/i2c/i2c_bus.h"
#include "pw_status/status.h"

namespace firmware {

class ArduinoI2CBus final : public II2CBus {
 public:
  pw::Status Init(uint8_t sda_pin, uint8_t scl_pin) override;

  pw::Status Write(uint8_t address,
                   const uint8_t* data,
                   size_t len) override;

  pw::Status Read(uint8_t address, uint8_t* buf, size_t len) override;

  pw::Status ReadRegister(uint8_t address,
                          uint8_t reg,
                          uint8_t* buf,
                          size_t len) override;

  pw::Status WriteRegister(uint8_t address,
                           uint8_t reg,
                           const uint8_t* data,
                           size_t len) override;
};

}  // namespace firmware
