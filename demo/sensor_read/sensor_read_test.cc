// Host-side tests for the sensor_read demo.
//
// Tests the thermistor math and ISensor interface behaviour using FakeSensor
// and the pure adc_to_celsius() function — no hardware or Arduino required.
//
// Build & run:
//   bazel test //demo/sensor_read:sensor_read_test

#include <cmath>

#include "firmware/sensor/mock_sensor.h"
#include "firmware/sensor/thermistor_math.h"
#include "pw_status/status.h"
#include "pw_unit_test/framework.h"

using firmware::thermistor::adc_to_celsius;
using firmware::testing::FakeSensor;
using firmware::testing::RecordingSensor;

// ── Thermistor conversion ─────────────────────────────────────────────────────

TEST(ThermistorConversion, RoomTemperatureAtMidpoint) {
    // adc_max/2 ≈ 2047: R_ntc ≈ R_ref ≈ R0, so T ≈ 25 °C
    float temp = adc_to_celsius(2047);
    EXPECT_GT(temp, 24.0f);
    EXPECT_LT(temp, 26.0f);
}

TEST(ThermistorConversion, OpenCircuitIsNaN) {
    EXPECT_TRUE(std::isnan(adc_to_celsius(0)));
}

TEST(ThermistorConversion, ShortCircuitIsNaN) {
    EXPECT_TRUE(std::isnan(adc_to_celsius(4095)));
}

// ── ISensor interface via FakeSensor ─────────────────────────────────────────

TEST(SensorInterface, SuccessfulInitReturnsOk) {
    FakeSensor sensor("thermistor", 34, 25.0f);
    EXPECT_TRUE(sensor.Init().ok());
}

TEST(SensorInterface, FailingInitReturnsError) {
    FakeSensor sensor("thermistor", 34, 0.0f, pw::Status::Unavailable());
    EXPECT_FALSE(sensor.Init().ok());
}

TEST(SensorInterface, ReadReturnsFakeValue) {
    FakeSensor sensor("thermistor", 34, 21.5f);
    auto r = sensor.Read();
    EXPECT_TRUE(r.valid);
    EXPECT_EQ(r.value, 21.5f);
}

TEST(SensorInterface, SetValueUpdatesReading) {
    FakeSensor sensor("thermistor", 34, 20.0f);
    sensor.set_value(37.0f);
    auto r = sensor.Read();
    EXPECT_TRUE(r.valid);
    EXPECT_EQ(r.value, 37.0f);
}

TEST(SensorInterface, NameAndAddressPreserved) {
    FakeSensor sensor("thermistor", 34, 0.0f);
    EXPECT_STREQ(sensor.name(), "thermistor");
    EXPECT_EQ(sensor.address(), 34);
}

// ── Polling behaviour ─────────────────────────────────────────────────────────

TEST(SensorPolling, ReadCalledOncePerLoop) {
    RecordingSensor sensor("thermistor", 34, 22.0f);
    sensor.Init();

    // Simulate three loop() iterations.
    for (int i = 0; i < 3; i++) {
        sensor.Read();
    }

    EXPECT_EQ(sensor.read_call_count(), 3);
    EXPECT_EQ(sensor.init_call_count(), 1);
}
