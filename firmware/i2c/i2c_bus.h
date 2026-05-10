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

// One step in a cascaded I2C mux chain.
// Defined here so both II2CBus and ISensor can use the same type.
struct MuxHop {
  uint8_t address;
  uint8_t channel;
};

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

  // Number of mux hops from the root bus to this bus.
  // 0 = this is the root bus (not mux-backed).
  // 1 = one TCA9548A between root and this bus, etc.
  virtual size_t mux_depth() const { return 0; }

  // Returns the MuxHop at the given depth (0 = outermost mux).
  // Undefined behaviour if depth >= mux_depth().
  virtual MuxHop mux_hop_at(size_t /*depth*/) const { return {0, 0}; }

  // Convenience accessors for single-level mux (innermost hop).
  uint8_t mux_address() const {
    return mux_depth() > 0 ? mux_hop_at(mux_depth() - 1).address : 0;
  }
  uint8_t mux_channel() const {
    return mux_depth() > 0 ? mux_hop_at(mux_depth() - 1).channel : 0;
  }
};

}  // namespace firmware
