#pragma once

// FakeAdc — deterministic, inspectable IAdc for host-side unit tests.
//
// Key properties:
//   - Returns preset per-pin values (default 0 for unconfigured pins).
//   - Records Init() call count per pin.
//   - Configurable max_value (default 4095).
//   - No Arduino dependency; safe to include in any host test.
//
// Usage:
//
//   FakeAdc adc;
//   adc.SetReading(board::kAdc0, 2048);  // midpoint — ~25 °C for standard NTC
//
//   ThermistorSensor sensor(board::kAdc0, &adc);
//   sensor.Init();
//   float t = sensor.Read();             // converts 2048 → ~25 °C

#include <cstdint>
#include <map>

#include "firmware/adc/adc.h"
#include "pw_status/status.h"

namespace firmware {
namespace testing {

class FakeAdc final : public IAdc {
 public:
  explicit FakeAdc(int max_val = 4095) : max_value_(max_val) {}

  // ── Preset configuration ───────────────────────────────────────────────────

  // Set the value returned by Read(pin).
  void SetReading(uint8_t pin, int value) { readings_[pin] = value; }

  // ── Inspection ─────────────────────────────────────────────────────────────

  // Number of times Init(pin) was called for the given pin.
  int init_call_count(uint8_t pin) const {
      auto it = init_counts_.find(pin);
      return it != init_counts_.end() ? it->second : 0;
  }

  // ── IAdc implementation ────────────────────────────────────────────────────

  pw::Status Init(uint8_t pin) override {
      init_counts_[pin]++;
      return pw::OkStatus();
  }

  int Read(uint8_t pin) override {
      auto it = readings_.find(pin);
      return it != readings_.end() ? it->second : 0;
  }

  int max_value() const override { return max_value_; }

 private:
  int max_value_;
  std::map<uint8_t, int> readings_;
  std::map<uint8_t, int> init_counts_;
};

}  // namespace testing
}  // namespace firmware
