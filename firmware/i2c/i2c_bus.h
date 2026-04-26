#pragma once

// II2CBus — board-agnostic I2C bus interface.
//
// Concrete implementations:
//   ArduinoI2CBus  — wraps Wire; device-only (target_compatible_with enforced in BUILD)
//   FakeI2CBus     — records transactions; host-side tests
//
// Usage in firmware:
//
//   ArduinoI2CBus bus;
//   MySensor sensor(bus, 0x76);
//
//   void setup() {
//       bus.Init(board::kSda, board::kScl);
//       sensor.Init();
//   }
//
//   void loop() {
//       float v = sensor.Read();
//   }
//
// Multiple sensors share one bus instance (I2C is a shared bus by design).
// Each sensor carries its own device address and calls the bus independently.

#include <cstddef>
#include <cstdint>

#include "pw_status/status.h"

namespace firmware {

class II2CBus {
 public:
  virtual ~II2CBus() = default;

  // Initialize the bus on the given SDA/SCL pins.
  // Call once from setup(), before any sensor Init() calls.
  virtual pw::Status Init(uint8_t sda_pin, uint8_t scl_pin) = 0;

  // Write `len` bytes from `data` to device at `address`.
  virtual pw::Status Write(uint8_t address,
                           const uint8_t* data,
                           size_t len) = 0;

  // Read `len` bytes from device at `address` into `buf`.
  virtual pw::Status Read(uint8_t address, uint8_t* buf, size_t len) = 0;

  // Write register address `reg`, then read `len` bytes into `buf`.
  // Uses a repeated-start condition (no STOP between the write and the read),
  // which is required by most I2C sensors.
  virtual pw::Status ReadRegister(uint8_t address,
                                  uint8_t reg,
                                  uint8_t* buf,
                                  size_t len) = 0;

  // Write register address `reg` followed by `len` bytes from `data`.
  virtual pw::Status WriteRegister(uint8_t address,
                                   uint8_t reg,
                                   const uint8_t* data,
                                   size_t len) = 0;

  // I2C address of the TCA9548A mux this bus is a channel of, or 0 if not
  // mux-backed. Used to populate the device manifest hardware address fields.
  virtual uint8_t mux_address() const { return 0; }

  // Channel number selected on the mux, or 0 if not mux-backed.
  virtual uint8_t mux_channel() const { return 0; }
};

}  // namespace firmware
