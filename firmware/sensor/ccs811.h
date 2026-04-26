#pragma once

// CCS811 eCO2 + TVOC gas sensor — non-blocking II2CBus-backed implementation.
//
// WAKE pin: must be held low during I2C communication. For wall-powered
// deployments, tie WAKE to GND (always asserted) — safe and intended use.
// For battery devices, control WAKE via GPIO and pass wake_assert/wake_release
// lambdas to the config instead (see elegoo_multiplex_config.cc).
//
// Wiring: ADDR pin to GND → I2C address 0x5A.
//         ADDR pin to VCC → I2C address 0x5B.
//
// The CCS811 requires a 20-minute burn-in on first power-on and a 48-hour
// run-in for accurate baselines. Readings before that are approximate.
//
// Usage in a board config:
//
//   static firmware::CCS811Device    ccs811_dev(ch2, 0x5A, millis);
//   static firmware::CCS811eCO2      ccs811_eco2(ccs811_dev, "eco2");
//   static firmware::CCS811TVOC      ccs811_tvoc(ccs811_dev, "tvoc");
//   static firmware::ISensor* const kSensors[] = {&ccs811_eco2, &ccs811_tvoc};

#include <cstdint>

#include "firmware/i2c/i2c_bus.h"
#include "firmware/sensor/sensor.h"
#include "pw_status/status.h"

namespace firmware {

// CCS811Device — handles I2C, app start, and caches latest measurement.
// Shared between CCS811eCO2 and CCS811TVOC.
class CCS811Device {
 public:
  CCS811Device(II2CBus& bus, uint8_t address, uint32_t (*clock_fn)());

  // Boots the device, loads and starts the measurement app.
  pw::Status Init();

  // Reads a new measurement if the data-ready flag is set.
  // Returns latest eCO2 in ppm.
  float ReadECO2();

  // Returns latest TVOC in ppb.
  float ReadTVOC();

  bool eco2_valid() const { return valid_; }
  bool tvoc_valid() const { return valid_; }
  uint8_t address() const { return address_; }

 private:
  void Poll();

  II2CBus& bus_;
  uint8_t address_;
  uint32_t (*clock_fn_)();

  float last_eco2_ppm_ = 0.0f;
  float last_tvoc_ppb_ = 0.0f;
  bool valid_ = false;
  bool init_ok_ = false;
  uint32_t last_poll_ms_ = 0;

  // CCS811 drive mode 1: constant measurement every 1 second.
  static constexpr uint32_t kPollIntervalMs = 1000;

  // Register addresses
  static constexpr uint8_t kRegStatus      = 0x00;
  static constexpr uint8_t kRegMeasMode    = 0x01;
  static constexpr uint8_t kRegAlgResult   = 0x02;
  static constexpr uint8_t kRegAppStart    = 0xF4;
  static constexpr uint8_t kRegHwId       = 0x20;

  // Status register bits
  static constexpr uint8_t kStatusAppValid  = 0x10;
  static constexpr uint8_t kStatusDataReady = 0x08;

  // Meas mode: drive mode 1 (1s), no interrupts
  static constexpr uint8_t kMeasMode1 = 0x10;
};

// ISensor wrapper for eCO2.
class CCS811eCO2 final : public ISensor {
 public:
  CCS811eCO2(CCS811Device& dev, const char* name);

  pw::Status Init() override;
  SensorReading Read() override;
  const char* name()    const override { return name_; }
  uint8_t address()     const override { return dev_.address(); }
  firmware_SensorType type() const override { return firmware_SensorType_SENSOR_TYPE_ECO2; }
  const char* unit()    const override { return "ppm"; }

 private:
  CCS811Device& dev_;
  const char* name_;
};

// ISensor wrapper for TVOC.
class CCS811TVOC final : public ISensor {
 public:
  CCS811TVOC(CCS811Device& dev, const char* name);

  pw::Status Init() override;
  SensorReading Read() override;
  const char* name()    const override { return name_; }
  uint8_t address()     const override { return dev_.address(); }
  firmware_SensorType type() const override { return firmware_SensorType_SENSOR_TYPE_TVOC; }
  const char* unit()    const override { return "ppb"; }

 private:
  CCS811Device& dev_;
  const char* name_;
};

}  // namespace firmware
