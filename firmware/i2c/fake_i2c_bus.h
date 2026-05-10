#pragma once

// FakeI2CBus — deterministic, inspectable II2CBus for host-side unit tests.
//
// Key properties:
//   - Records every transaction (type, address, register, data).
//   - Returns preset data for Read / ReadRegister calls; zero-fills when no
//     preset is configured (behaviour is always defined, never undefined).
//   - Error injection: InjectError() causes the *next* transaction to fail,
//     then resets to OkStatus so subsequent calls succeed.
//   - No Arduino dependency; safe to include in any host test.
//
// Usage:
//
//   FakeI2CBus bus;
//   bus.SetRegisterData(0x76, 0xD0, {0x60});  // preset chip_id register
//   bus.InjectError(pw::Status::Unavailable());
//
//   MySensor sensor(bus, 0x76);
//   EXPECT_FALSE(sensor.Init().ok());          // error consumed here
//   EXPECT_TRUE(sensor.Init().ok());           // second call succeeds

#include <algorithm>
#include <cstdint>
#include <map>
#include <vector>

#include "firmware/i2c/i2c_bus.h"
#include "pw_status/status.h"

namespace firmware {
namespace testing {

class FakeI2CBus final : public II2CBus {
 public:
  // ── Preset read responses ──────────────────────────────────────────────────

  // Set data returned by ReadRegister(address, reg, ...).
  void SetRegisterData(uint8_t address, uint8_t reg,
                       std::vector<uint8_t> data) {
      register_data_[RegKey(address, reg)] = std::move(data);
  }

  // Set data returned by Read(address, ...).
  void SetReadData(uint8_t address, std::vector<uint8_t> data) {
      read_data_[address] = std::move(data);
  }

  // ── Error injection ────────────────────────────────────────────────────────

  // The next transaction (any type) will return this status. Resets to Ok
  // after one use, so subsequent calls are unaffected.
  void InjectError(pw::Status error) { injected_error_ = error; }

  // ── Transaction log ────────────────────────────────────────────────────────

  struct Transaction {
    enum class Type { kInit, kWrite, kRead, kWriteRegister, kReadRegister };
    Type type;
    uint8_t address = 0;
    uint8_t reg = 0;           // populated for WriteRegister / ReadRegister
    std::vector<uint8_t> data; // written bytes, or bytes placed into caller buf
  };

  const std::vector<Transaction>& transactions() const { return transactions_; }

  int transaction_count() const {
      return static_cast<int>(transactions_.size());
  }

  // Counts Write + WriteRegister calls.
  int write_count() const {
      return CountType(Transaction::Type::kWrite) +
             CountType(Transaction::Type::kWriteRegister);
  }

  // Counts Read + ReadRegister calls.
  int read_count() const {
      return CountType(Transaction::Type::kRead) +
             CountType(Transaction::Type::kReadRegister);
  }

  // Returns the address of the most recent transaction, or -1 if none.
  int last_address() const {
      if (transactions_.empty()) return -1;
      return transactions_.back().address;
  }

  // Reset all recorded transactions and injected errors. Presets are kept.
  void clear() {
      transactions_.clear();
      injected_error_ = pw::OkStatus();
  }

  // ── Mux identity (for testing sensors that query mux path) ──────────────────

  void set_mux_address(uint8_t a) { mux_address_ = a; }
  void set_mux_channel(uint8_t c) { mux_channel_ = c; }

  // ── II2CBus implementation ─────────────────────────────────────────────────

  size_t mux_depth() const override { return mux_address_ != 0 ? 1 : 0; }
  MuxHop mux_hop_at(size_t /*depth*/) const override {
      return {mux_address_, mux_channel_};
  }

  pw::Status Init(uint8_t sda_pin, uint8_t scl_pin) override {
      if (pw::Status e = ConsumeError(); !e.ok()) return e;
      transactions_.push_back(
          {Transaction::Type::kInit, 0, 0, {sda_pin, scl_pin}});
      return pw::OkStatus();
  }

  pw::Status Write(uint8_t address,
                   const uint8_t* data,
                   size_t len) override {
      if (pw::Status e = ConsumeError(); !e.ok()) return e;
      transactions_.push_back({Transaction::Type::kWrite, address, 0,
                               {data, data + len}});
      return pw::OkStatus();
  }

  pw::Status Read(uint8_t address, uint8_t* buf, size_t len) override {
      if (pw::Status e = ConsumeError(); !e.ok()) return e;
      FillBuffer(buf, len, read_data_, address);
      transactions_.push_back(
          {Transaction::Type::kRead, address, 0, {buf, buf + len}});
      return pw::OkStatus();
  }

  pw::Status ReadRegister(uint8_t address,
                          uint8_t reg,
                          uint8_t* buf,
                          size_t len) override {
      if (pw::Status e = ConsumeError(); !e.ok()) return e;
      auto it = register_data_.find(RegKey(address, reg));
      std::fill(buf, buf + len, 0);
      if (it != register_data_.end()) {
          size_t n = std::min(len, it->second.size());
          std::copy(it->second.begin(), it->second.begin() + n, buf);
      }
      transactions_.push_back(
          {Transaction::Type::kReadRegister, address, reg, {buf, buf + len}});
      return pw::OkStatus();
  }

  pw::Status WriteRegister(uint8_t address,
                           uint8_t reg,
                           const uint8_t* data,
                           size_t len) override {
      if (pw::Status e = ConsumeError(); !e.ok()) return e;
      transactions_.push_back({Transaction::Type::kWriteRegister, address, reg,
                               {data, data + len}});
      return pw::OkStatus();
  }

 private:
  // Encode (address, reg) into a single map key.
  static uint16_t RegKey(uint8_t address, uint8_t reg) {
      return static_cast<uint16_t>((static_cast<uint16_t>(address) << 8) | reg);
  }

  // Copy preset data into buf; zero-fill if no preset is configured.
  static void FillBuffer(uint8_t* buf, size_t len,
                         const std::map<uint8_t, std::vector<uint8_t>>& presets,
                         uint8_t address) {
      std::fill(buf, buf + len, 0);
      auto it = presets.find(address);
      if (it != presets.end()) {
          size_t n = std::min(len, it->second.size());
          std::copy(it->second.begin(), it->second.begin() + n, buf);
      }
  }

  pw::Status ConsumeError() {
      pw::Status e = injected_error_;
      injected_error_ = pw::OkStatus();
      return e;
  }

  int CountType(Transaction::Type type) const {
      int n = 0;
      for (const auto& t : transactions_) {
          if (t.type == type) n++;
      }
      return n;
  }

  std::vector<Transaction> transactions_;
  std::map<uint16_t, std::vector<uint8_t>> register_data_;
  std::map<uint8_t, std::vector<uint8_t>> read_data_;
  pw::Status injected_error_ = pw::OkStatus();
  uint8_t mux_address_ = 0;
  uint8_t mux_channel_ = 0;
};

}  // namespace testing
}  // namespace firmware
