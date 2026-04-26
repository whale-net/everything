#pragma once

// TCA9548ABus — II2CBus implementation for one channel of a TCA9548A I2C mux.
//
// Wraps a parent II2CBus and selects the configured channel before every
// operation. Multiple TCA9548ABus instances sharing the same parent bus can
// each represent a different channel — sensors attached to them work without
// any manual channel switching.
//
// Usage in a board config:
//
//   static firmware::ArduinoI2CBus bus;           // physical Wire bus
//   static firmware::TCA9548ABus ch1(bus, 0x70, 1); // mux channel SD1
//   static firmware::BH1750Sensor light(ch1, 0x23, "light", millis);
//
//   firmware::II2CBus& GetBus() { return bus; }   // root bus; Init() called here
//
// Init() on a TCA9548ABus is a no-op — the parent bus is already initialised
// by GetBus().Init() in sensorboard_main before sensor Init() calls run.

#include <cstdint>

#include "firmware/i2c/i2c_bus.h"
#include "pw_status/status.h"

namespace firmware {

class TCA9548ABus final : public II2CBus {
 public:
  // parent:      the underlying I2C bus (must outlive this object)
  // mux_address: I2C address of the TCA9548A (0x70–0x77 depending on A0/A1/A2)
  // channel:     SD port number 0–7
  TCA9548ABus(II2CBus& parent, uint8_t mux_address, uint8_t channel);

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

 private:
  II2CBus& parent_;
  uint8_t mux_address_;
  uint8_t channel_mask_;  // 1 << channel

  pw::Status SelectChannel();
};

}  // namespace firmware
