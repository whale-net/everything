#pragma once

// SHT3x temperature + humidity sensor — non-blocking II2CBus-backed implementation.
//
// The SHT3x returns both temperature and humidity in a single 6-byte read.
// SHT3xDevice performs the I2C transaction and caches both values.
// SHT3xTemperature and SHT3xHumidity are thin ISensor wrappers that share one
// SHT3xDevice — both must be registered in the sensor array.
//
// Wiring: ADDR pin to GND → I2C address 0x44.
//         ADDR pin to VCC → I2C address 0x45.
//
// Usage in a board config:
//
//   static firmware::SHT3xDevice    sht3x_dev(bus, 0x44, millis);
//   static firmware::SHT3xTemperature sht3x_temp(sht3x_dev, "temp");
//   static firmware::SHT3xHumidity    sht3x_humi(sht3x_dev, "humidity");
//   static firmware::ISensor* const kSensors[] = {&sht3x_temp, &sht3x_humi};

#include <cstdint>

#include "firmware/i2c/i2c_bus.h"
#include "firmware/sensor/sensor.h"
#include "pw_status/status.h"

namespace firmware {

// SHT3xDevice — handles I2C and caches the latest measurement.
// Shared between SHT3xTemperature and SHT3xHumidity.
class SHT3xDevice {
 public:
  SHT3xDevice(II2CBus& bus, uint8_t address, uint32_t (*clock_fn)());

  // Sends a single-shot high-repeatability measurement command.
  pw::Status Init();

  // Polls for a completed measurement if kMeasureTimeMs has elapsed,
  // then re-arms. Returns the latest valid temperature in °C.
  float ReadTemperature();

  // Returns the latest valid humidity in %RH.
  float ReadHumidity();

  bool temp_valid()  const { return temp_valid_; }
  bool humi_valid()  const { return humi_valid_; }
  uint8_t address()      const { return address_; }
  uint8_t mux_address()  const { return bus_.mux_address(); }
  uint8_t mux_channel()  const { return bus_.mux_channel(); }

 private:
  void Poll();
  pw::Status Trigger();

  II2CBus& bus_;
  uint8_t address_;
  uint32_t (*clock_fn_)();

  float last_temp_c_ = 0.0f;
  float last_humi_pct_ = 0.0f;
  bool temp_valid_ = false;
  bool humi_valid_ = false;
  bool init_ok_ = false;
  uint32_t trigger_ms_ = 0;

  // Single-shot high-repeatability: ~15 ms measurement time.
  static constexpr uint32_t kMeasureTimeMs = 20;
  // Command: single shot, high repeatability, clock stretching disabled.
  static constexpr uint8_t kCmdMSB = 0x24;
  static constexpr uint8_t kCmdLSB = 0x00;
};

// ISensor wrapper for temperature.
class SHT3xTemperature final : public ISensor {
 public:
  SHT3xTemperature(SHT3xDevice& dev, const char* name);

  pw::Status Init() override;
  SensorReading Read() override;
  const char* name()         const override { return name_; }
  uint8_t address()          const override { return dev_.address(); }
  firmware_SensorType type() const override { return firmware_SensorType_SENSOR_TYPE_TEMPERATURE; }
  SensorUnit unit()          const override { return SensorUnit::kCelsius; }
  uint8_t mux_address()      const override { return dev_.mux_address(); }
  uint8_t mux_channel()      const override { return dev_.mux_channel(); }

 private:
  SHT3xDevice& dev_;
  const char* name_;
};

// ISensor wrapper for humidity.
class SHT3xHumidity final : public ISensor {
 public:
  SHT3xHumidity(SHT3xDevice& dev, const char* name);

  pw::Status Init() override;
  SensorReading Read() override;
  const char* name()         const override { return name_; }
  uint8_t address()          const override { return dev_.address(); }
  firmware_SensorType type() const override { return firmware_SensorType_SENSOR_TYPE_HUMIDITY; }
  SensorUnit unit()          const override { return SensorUnit::kRelativeHumidity; }
  uint8_t mux_address()      const override { return dev_.mux_address(); }
  uint8_t mux_channel()      const override { return dev_.mux_channel(); }

 private:
  SHT3xDevice& dev_;
  const char* name_;
};

}  // namespace firmware
