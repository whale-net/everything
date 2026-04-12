#pragma once

// Host-side mock for ISensor.
//
// Used in cc_test targets that run on the developer's machine without hardware.
// Pigweed's pw_unit_test provides the googletest-compatible TEST() / EXPECT_*
// macros.  The mock itself uses pw_unit_test's built-in framework directly
// (no gmock dependency needed — we control the interface).
//
// For tests that need configurable sensor behaviour, use FakeSensor below.
// For tests that verify call counts / argument capture, see RecordingSensor.

#include <cstdint>

#include "firmware/sensor/sensor.h"
#include "pw_status/status.h"

namespace firmware {
namespace testing {

// FakeSensor — returns a fixed value; configurable at construction time.
// Use this in the vast majority of unit tests.
class FakeSensor final : public ISensor {
 public:
  FakeSensor(const char* name, uint8_t address, float value,
             pw::Status init_status = pw::OkStatus())
      : name_(name),
        address_(address),
        value_(value),
        init_status_(init_status) {}

  pw::Status Init() override { return init_status_; }
  SensorReading Read() override { return SensorReading::Ok(value_); }
  const char* name() const override { return name_; }
  uint8_t address() const override { return address_; }

  // Allow tests to change the reading mid-test.
  void set_value(float v) { value_ = v; }
  void set_init_status(pw::Status s) { init_status_ = s; }

 private:
  const char* name_;
  uint8_t address_;
  float value_;
  pw::Status init_status_;
};

// RecordingSensor — tracks how many times Init() and Read() were called.
// Use this when you need to assert on interaction counts.
class RecordingSensor final : public ISensor {
 public:
  RecordingSensor(const char* name, uint8_t address, float value)
      : name_(name), address_(address), value_(value) {}

  pw::Status Init() override {
    init_call_count_++;
    return pw::OkStatus();
  }

  SensorReading Read() override {
    read_call_count_++;
    return SensorReading::Ok(value_);
  }

  const char* name() const override { return name_; }
  uint8_t address() const override { return address_; }

  int init_call_count() const { return init_call_count_; }
  int read_call_count() const { return read_call_count_; }

 private:
  const char* name_;
  uint8_t address_;
  float value_;
  int init_call_count_ = 0;
  int read_call_count_ = 0;
};

}  // namespace testing
}  // namespace firmware
