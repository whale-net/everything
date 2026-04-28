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
#include <cstring>

#include "firmware/sensor/sensor.h"
#include "pw_status/status.h"

namespace firmware {
namespace testing {

// FakeSensor — returns a fixed value; configurable at construction time.
// Supports SetName() and configurable mux path for ConfigApplier tests.
class FakeSensor final : public ISensor {
 public:
  FakeSensor(const char* name, uint8_t address, float value,
             pw::Status init_status = pw::OkStatus())
      : address_(address),
        value_(value),
        init_status_(init_status),
        mux_depth_(0) {
      strncpy(name_buf_, name, sizeof(name_buf_) - 1);
      name_buf_[sizeof(name_buf_) - 1] = '\0';
  }

  pw::Status Init() override { return init_status_; }
  SensorReading Read() override { return SensorReading::Ok(value_); }
  const char* name()    const override { return name_buf_; }
  bool SetName(const char* name) override {
      strncpy(name_buf_, name, sizeof(name_buf_) - 1);
      name_buf_[sizeof(name_buf_) - 1] = '\0';
      return true;
  }
  uint8_t address()     const override { return address_; }
  firmware_SensorType type() const override { return firmware_SensorType_SENSOR_TYPE_UNKNOWN; }
  SensorUnit unit()     const override { return SensorUnit::kUnknown; }
  size_t mux_depth()    const override { return mux_depth_; }
  MuxHop mux_hop(size_t depth) const override {
      return depth < mux_depth_ ? mux_hops_[depth] : MuxHop{0, 0};
  }

  // Allow tests to change the reading mid-test.
  void set_value(float v) { value_ = v; }
  void set_init_status(pw::Status s) { init_status_ = s; }

  // Configure the mux chain. hops[0] is outermost, hops[n-1] is innermost.
  template <size_t N>
  void set_mux_path(const MuxHop (&hops)[N]) {
      static_assert(N <= kMaxHops, "too many mux hops");
      mux_depth_ = N;
      for (size_t i = 0; i < N; ++i) mux_hops_[i] = hops[i];
  }

 private:
  static constexpr size_t kMaxHops = 4;
  char name_buf_[32];
  uint8_t address_;
  float value_;
  pw::Status init_status_;
  size_t mux_depth_;
  MuxHop mux_hops_[kMaxHops] = {};
};

// RecordingSensor — tracks how many times Init() and Read() were called.
// Use this when you need to assert on interaction counts.
class RecordingSensor final : public ISensor {
 public:
  RecordingSensor(const char* name, uint8_t address, float value)
      : address_(address), value_(value) {
      strncpy(name_buf_, name, sizeof(name_buf_) - 1);
      name_buf_[sizeof(name_buf_) - 1] = '\0';
  }

  pw::Status Init() override {
    init_call_count_++;
    return pw::OkStatus();
  }

  SensorReading Read() override {
    read_call_count_++;
    return SensorReading::Ok(value_);
  }

  const char* name()    const override { return name_buf_; }
  uint8_t address()     const override { return address_; }
  firmware_SensorType type() const override { return firmware_SensorType_SENSOR_TYPE_UNKNOWN; }
  SensorUnit unit()     const override { return SensorUnit::kUnknown; }

  int init_call_count() const { return init_call_count_; }
  int read_call_count() const { return read_call_count_; }

 private:
  char name_buf_[32];
  uint8_t address_;
  float value_;
  int init_call_count_ = 0;
  int read_call_count_ = 0;
};

}  // namespace testing
}  // namespace firmware
