// Tests for FakeI2CBus and the II2CBus interface contract.
//
// Covers:
//   - FakeI2CBus: Init, Write, Read, ReadRegister, WriteRegister
//   - Preset data: register isolation, address isolation, partial-length copies
//   - Error injection: single-shot semantics across all operation types
//   - Transaction log: counts, last_address, clear()
//   - Contract tests: a realistic multi-register I2C device using the interface
//
// Run:
//   bazel test //firmware/i2c:i2c_bus_test

#include "firmware/i2c/fake_i2c_bus.h"

#include "pw_status/status.h"
#include "pw_unit_test/framework.h"

using firmware::testing::FakeI2CBus;
using Transaction = FakeI2CBus::Transaction;

// ── Init ──────────────────────────────────────────────────────────────────────

TEST(FakeI2CBus, InitSucceeds) {
    FakeI2CBus bus;
    EXPECT_TRUE(bus.Init(21, 22).ok());
}

TEST(FakeI2CBus, InitRecordsPins) {
    FakeI2CBus bus;
    bus.Init(21, 22);
    ASSERT_EQ(bus.transaction_count(), 1);
    const auto& t = bus.transactions()[0];
    EXPECT_EQ(t.type, Transaction::Type::kInit);
    EXPECT_EQ(t.data[0], 21u);  // sda
    EXPECT_EQ(t.data[1], 22u);  // scl
}

// ── Write ─────────────────────────────────────────────────────────────────────

TEST(FakeI2CBus, WriteRecordsAddressAndData) {
    FakeI2CBus bus;
    const uint8_t data[] = {0xAB, 0xCD};
    EXPECT_TRUE(bus.Write(0x40, data, 2).ok());
    ASSERT_EQ(bus.transaction_count(), 1);
    const auto& t = bus.transactions()[0];
    EXPECT_EQ(t.type, Transaction::Type::kWrite);
    EXPECT_EQ(t.address, 0x40u);
    EXPECT_EQ(t.data[0], 0xABu);
    EXPECT_EQ(t.data[1], 0xCDu);
}

TEST(FakeI2CBus, WriteDoesNotAffectReadPresets) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x40, 0x00, {0xFF});
    const uint8_t data[] = {0x00};
    bus.Write(0x40, data, 1);
    uint8_t buf[1] = {};
    bus.ReadRegister(0x40, 0x00, buf, 1);
    EXPECT_EQ(buf[0], 0xFFu);
}

// ── Read ──────────────────────────────────────────────────────────────────────

TEST(FakeI2CBus, ReadReturnsPresetData) {
    FakeI2CBus bus;
    bus.SetReadData(0x40, {0x12, 0x34});
    uint8_t buf[2] = {};
    EXPECT_TRUE(bus.Read(0x40, buf, 2).ok());
    EXPECT_EQ(buf[0], 0x12u);
    EXPECT_EQ(buf[1], 0x34u);
}

TEST(FakeI2CBus, ReadWithNoPresetZeroFills) {
    FakeI2CBus bus;
    uint8_t buf[2] = {0xFF, 0xFF};
    EXPECT_TRUE(bus.Read(0x40, buf, 2).ok());
    EXPECT_EQ(buf[0], 0x00u);
    EXPECT_EQ(buf[1], 0x00u);
}

TEST(FakeI2CBus, ReadRecordsTransaction) {
    FakeI2CBus bus;
    bus.SetReadData(0x40, {0xBE, 0xEF});
    uint8_t buf[2] = {};
    bus.Read(0x40, buf, 2);
    const auto& t = bus.transactions()[0];
    EXPECT_EQ(t.type, Transaction::Type::kRead);
    EXPECT_EQ(t.address, 0x40u);
    EXPECT_EQ(t.data[0], 0xBEu);
    EXPECT_EQ(t.data[1], 0xEFu);
}

// ── ReadRegister ──────────────────────────────────────────────────────────────

TEST(FakeI2CBus, ReadRegisterReturnsPresetData) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x76, 0xD0, {0x60});
    uint8_t buf[1] = {};
    EXPECT_TRUE(bus.ReadRegister(0x76, 0xD0, buf, 1).ok());
    EXPECT_EQ(buf[0], 0x60u);
}

TEST(FakeI2CBus, ReadRegisterWithNoPresetZeroFills) {
    FakeI2CBus bus;
    uint8_t buf[2] = {0xFF, 0xFF};
    bus.ReadRegister(0x76, 0x00, buf, 2);
    EXPECT_EQ(buf[0], 0x00u);
    EXPECT_EQ(buf[1], 0x00u);
}

TEST(FakeI2CBus, ReadRegisterRecordsAddressAndReg) {
    FakeI2CBus bus;
    uint8_t buf[1] = {};
    bus.ReadRegister(0x76, 0xD0, buf, 1);
    const auto& t = bus.transactions()[0];
    EXPECT_EQ(t.type, Transaction::Type::kReadRegister);
    EXPECT_EQ(t.address, 0x76u);
    EXPECT_EQ(t.reg, 0xD0u);
}

TEST(FakeI2CBus, ReadRegisterIsolatesByAddress) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x76, 0x01, {0xAA});
    bus.SetRegisterData(0x77, 0x01, {0xBB});
    uint8_t a[1] = {}, b[1] = {};
    bus.ReadRegister(0x76, 0x01, a, 1);
    bus.ReadRegister(0x77, 0x01, b, 1);
    EXPECT_EQ(a[0], 0xAAu);
    EXPECT_EQ(b[0], 0xBBu);
}

TEST(FakeI2CBus, ReadRegisterIsolatesByReg) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x40, 0x00, {0x11});
    bus.SetRegisterData(0x40, 0x01, {0x22});
    uint8_t a[1] = {}, b[1] = {};
    bus.ReadRegister(0x40, 0x00, a, 1);
    bus.ReadRegister(0x40, 0x01, b, 1);
    EXPECT_EQ(a[0], 0x11u);
    EXPECT_EQ(b[0], 0x22u);
}

TEST(FakeI2CBus, ReadRegisterCopiesOnlyRequestedLength) {
    FakeI2CBus bus;
    // Preset 4 bytes, request only 2.
    bus.SetRegisterData(0x40, 0x02, {0x01, 0x02, 0x03, 0x04});
    uint8_t buf[2] = {};
    bus.ReadRegister(0x40, 0x02, buf, 2);
    EXPECT_EQ(buf[0], 0x01u);
    EXPECT_EQ(buf[1], 0x02u);
}

// ── WriteRegister ─────────────────────────────────────────────────────────────

TEST(FakeI2CBus, WriteRegisterRecordsAllFields) {
    FakeI2CBus bus;
    const uint8_t data[] = {0x01};
    EXPECT_TRUE(bus.WriteRegister(0x40, 0x00, data, 1).ok());
    const auto& t = bus.transactions()[0];
    EXPECT_EQ(t.type, Transaction::Type::kWriteRegister);
    EXPECT_EQ(t.address, 0x40u);
    EXPECT_EQ(t.reg, 0x00u);
    EXPECT_EQ(t.data[0], 0x01u);
}

TEST(FakeI2CBus, WriteRegisterMultiByte) {
    FakeI2CBus bus;
    const uint8_t data[] = {0xDE, 0xAD, 0xBE, 0xEF};
    bus.WriteRegister(0x40, 0x10, data, 4);
    const auto& t = bus.transactions()[0];
    EXPECT_EQ(t.data.size(), 4u);
    EXPECT_EQ(t.data[2], 0xBEu);
    EXPECT_EQ(t.data[3], 0xEFu);
}

// ── Error injection ───────────────────────────────────────────────────────────

TEST(FakeI2CBus, InjectedErrorIsReturnedOnce) {
    FakeI2CBus bus;
    bus.InjectError(pw::Status::Unavailable());
    const uint8_t data[] = {0x00};
    EXPECT_EQ(bus.Write(0x40, data, 1), pw::Status::Unavailable());
    EXPECT_TRUE(bus.Write(0x40, data, 1).ok());  // error consumed
}

TEST(FakeI2CBus, InjectedErrorOnRead) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x40, 0x02, {0xFF});
    bus.InjectError(pw::Status::DeadlineExceeded());
    uint8_t buf[1] = {};
    EXPECT_EQ(bus.ReadRegister(0x40, 0x02, buf, 1), pw::Status::DeadlineExceeded());
    // Data should not be written to buf on error.
    EXPECT_EQ(buf[0], 0x00u);
}

TEST(FakeI2CBus, InjectedErrorOnWriteRegister) {
    FakeI2CBus bus;
    bus.InjectError(pw::Status::NotFound());
    const uint8_t data[] = {0x01};
    EXPECT_EQ(bus.WriteRegister(0x40, 0x00, data, 1), pw::Status::NotFound());
    EXPECT_TRUE(bus.WriteRegister(0x40, 0x00, data, 1).ok());
}

TEST(FakeI2CBus, InjectedErrorOnInit) {
    FakeI2CBus bus;
    bus.InjectError(pw::Status::Internal());
    EXPECT_EQ(bus.Init(21, 22), pw::Status::Internal());
    EXPECT_TRUE(bus.Init(21, 22).ok());
}

// ── Transaction counting and inspection ───────────────────────────────────────

TEST(FakeI2CBus, WriteCountIncludesWriteAndWriteRegister) {
    FakeI2CBus bus;
    const uint8_t d[] = {0};
    bus.Write(0x40, d, 1);
    bus.WriteRegister(0x40, 0x00, d, 1);
    bus.WriteRegister(0x40, 0x01, d, 1);
    EXPECT_EQ(bus.write_count(), 3);
    EXPECT_EQ(bus.read_count(), 0);
}

TEST(FakeI2CBus, ReadCountIncludesReadAndReadRegister) {
    FakeI2CBus bus;
    uint8_t buf[1] = {};
    bus.Read(0x40, buf, 1);
    bus.ReadRegister(0x40, 0x00, buf, 1);
    EXPECT_EQ(bus.read_count(), 2);
    EXPECT_EQ(bus.write_count(), 0);
}

TEST(FakeI2CBus, LastAddressReflectsMostRecentTransaction) {
    FakeI2CBus bus;
    const uint8_t d[] = {0};
    bus.Write(0x40, d, 1);
    bus.Write(0x41, d, 1);
    EXPECT_EQ(bus.last_address(), 0x41);
}

TEST(FakeI2CBus, LastAddressMinusOneWhenEmpty) {
    FakeI2CBus bus;
    EXPECT_EQ(bus.last_address(), -1);
}

TEST(FakeI2CBus, ClearResetsTransactionsAndError) {
    FakeI2CBus bus;
    bus.Init(21, 22);
    const uint8_t d[] = {0};
    bus.Write(0x40, d, 1);
    bus.InjectError(pw::Status::Unavailable());
    bus.clear();
    EXPECT_EQ(bus.transaction_count(), 0);
    EXPECT_EQ(bus.write_count(), 0);
    // After clear(), injected error should also be gone.
    EXPECT_TRUE(bus.Write(0x40, d, 1).ok());
}

TEST(FakeI2CBus, ClearPreservesPresets) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x40, 0x02, {0xAB});
    bus.clear();
    uint8_t buf[1] = {};
    bus.ReadRegister(0x40, 0x02, buf, 1);
    EXPECT_EQ(buf[0], 0xABu);  // preset survives clear
}

// ── Contract tests: realistic I2C device client ───────────────────────────────
//
// TestDevice models a generic two-register sensor:
//   Init:  WriteRegister(addr, 0x00, [0x01])  — write config
//   Read:  ReadRegister(addr, 0x02, buf[2])   — read big-endian uint16

class TestDevice {
 public:
  TestDevice(firmware::II2CBus& bus, uint8_t address)
      : bus_(bus), address_(address) {}

  pw::Status Init() {
      const uint8_t config = 0x01;
      return bus_.WriteRegister(address_, 0x00, &config, 1);
  }

  pw::Status ReadValue(uint16_t* out) {
      uint8_t buf[2] = {};
      pw::Status s = bus_.ReadRegister(address_, 0x02, buf, 2);
      if (!s.ok()) return s;
      *out = static_cast<uint16_t>((static_cast<uint16_t>(buf[0]) << 8) | buf[1]);
      return pw::OkStatus();
  }

 private:
  firmware::II2CBus& bus_;
  uint8_t address_;
};

TEST(I2CBusContract, DeviceInitWritesConfigRegister) {
    FakeI2CBus bus;
    TestDevice dev(bus, 0x40);
    EXPECT_TRUE(dev.Init().ok());
    ASSERT_EQ(bus.write_count(), 1);
    const auto& t = bus.transactions()[0];
    EXPECT_EQ(t.address, 0x40u);
    EXPECT_EQ(t.reg, 0x00u);
    EXPECT_EQ(t.data[0], 0x01u);
}

TEST(I2CBusContract, DeviceReadReturnsPresetBigEndian) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x40, 0x02, {0x12, 0x34});
    TestDevice dev(bus, 0x40);
    dev.Init();
    uint16_t val = 0;
    EXPECT_TRUE(dev.ReadValue(&val).ok());
    EXPECT_EQ(val, 0x1234u);
}

TEST(I2CBusContract, DevicePropagatesBusWriteError) {
    FakeI2CBus bus;
    bus.InjectError(pw::Status::NotFound());  // NACK on address
    TestDevice dev(bus, 0x40);
    EXPECT_FALSE(dev.Init().ok());
}

TEST(I2CBusContract, DevicePropagatesBusReadError) {
    FakeI2CBus bus;
    TestDevice dev(bus, 0x40);
    dev.Init();
    bus.InjectError(pw::Status::DeadlineExceeded());
    uint16_t val = 0;
    EXPECT_FALSE(dev.ReadValue(&val).ok());
}

TEST(I2CBusContract, TwoDevicesOnSameBusDoNotInterfere) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x40, 0x02, {0x00, 0x64});  // device A: 100
    bus.SetRegisterData(0x41, 0x02, {0x01, 0x2C});  // device B: 300
    TestDevice dev_a(bus, 0x40);
    TestDevice dev_b(bus, 0x41);
    dev_a.Init();
    dev_b.Init();
    uint16_t a = 0, b = 0;
    EXPECT_TRUE(dev_a.ReadValue(&a).ok());
    EXPECT_TRUE(dev_b.ReadValue(&b).ok());
    EXPECT_EQ(a, 100u);
    EXPECT_EQ(b, 300u);
    // Two inits + two reads = 4 total transactions
    EXPECT_EQ(bus.write_count(), 2);
    EXPECT_EQ(bus.read_count(), 2);
}

TEST(I2CBusContract, DeviceRecoveryAfterTransientError) {
    FakeI2CBus bus;
    bus.SetRegisterData(0x40, 0x02, {0x00, 0xFF});
    TestDevice dev(bus, 0x40);
    dev.Init();
    // First read fails transiently.
    bus.InjectError(pw::Status::Unavailable());
    uint16_t val = 0;
    EXPECT_FALSE(dev.ReadValue(&val).ok());
    // Second read succeeds — error was single-shot.
    EXPECT_TRUE(dev.ReadValue(&val).ok());
    EXPECT_EQ(val, 0x00FFu);
}
