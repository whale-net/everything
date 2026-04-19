// Host-side unit tests for BH1750Sensor using FakeI2CBus.
// No hardware required.

#include "firmware/sensor/bh1750.h"

#include "firmware/i2c/fake_i2c_bus.h"
#include "pw_unit_test/framework.h"

namespace firmware {
namespace {

// Controllable fake clock for timing tests.
static uint32_t g_fake_millis = 0;
static uint32_t FakeMillis() { return g_fake_millis; }

constexpr uint8_t kAddr = 0x23;

class BH1750Test : public ::testing::Test {
 protected:
  void SetUp() override { g_fake_millis = 0; }

  testing::FakeI2CBus bus;
  BH1750Sensor sensor{bus, kAddr, "light", FakeMillis};
};

// Init() sends power-on command then one-shot trigger command.
TEST_F(BH1750Test, InitSendsPowerOnAndTrigger) {
    pw::Status s = sensor.Init();
    EXPECT_TRUE(s.ok());

    // Two writes: power-on (0x01) and trigger (0x20).
    EXPECT_EQ(bus.write_count(), 2);
    const auto& txns = bus.transactions();
    EXPECT_EQ(txns[0].data[0], 0x01u);  // kCmdPowerOn
    EXPECT_EQ(txns[1].data[0], 0x20u);  // kCmdOneShot
}

// Read() before kMeasureTimeMs returns Invalid (measurement still integrating).
TEST_F(BH1750Test, ReadBeforeMeasurementTimeReturnsInvalid) {
    sensor.Init();
    bus.clear();

    g_fake_millis = 100;  // < 180 ms
    SensorReading r = sensor.Read();
    EXPECT_FALSE(r.valid);
    EXPECT_EQ(bus.read_count(), 0);  // no I2C read attempted
}

// Read() after kMeasureTimeMs reads 2 bytes and converts to lux.
TEST_F(BH1750Test, ReadAfterMeasurementTimeReturnsLux) {
    sensor.Init();
    bus.clear();

    // Raw value 0x1A00 = 6656; 6656 / 1.2 = 5546.67 lux
    bus.SetReadData(kAddr, {0x1A, 0x00});
    g_fake_millis = 200;  // > 180 ms

    SensorReading r = sensor.Read();
    EXPECT_TRUE(r.valid);
    EXPECT_GT(r.value, 5545.0f);
    EXPECT_LT(r.value, 5548.0f);
}

// After a successful Read(), the sensor re-arms (triggers a new measurement).
TEST_F(BH1750Test, ReadReArmsAfterRetrievingResult) {
    sensor.Init();
    bus.clear();

    bus.SetReadData(kAddr, {0x10, 0x00});
    g_fake_millis = 200;
    sensor.Read();

    // Should have issued one I2C read + one trigger write.
    EXPECT_EQ(bus.read_count(), 1);
    EXPECT_EQ(bus.write_count(), 1);
    const auto& txns = bus.transactions();
    EXPECT_EQ(txns.back().data[0], 0x20u);  // kCmdOneShot re-arm
}

// Init failure propagates as not-ok status.
TEST_F(BH1750Test, InitFailurePropagates) {
    bus.InjectError(pw::Status::Unavailable());
    pw::Status s = sensor.Init();
    EXPECT_FALSE(s.ok());
}

// Read() after Init failure returns Invalid.
TEST_F(BH1750Test, ReadAfterInitFailureReturnsInvalid) {
    bus.InjectError(pw::Status::Unavailable());
    sensor.Init();

    g_fake_millis = 200;
    EXPECT_FALSE(sensor.Read().valid);
}

}  // namespace
}  // namespace firmware
