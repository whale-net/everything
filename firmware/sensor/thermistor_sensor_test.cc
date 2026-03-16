// Host-side unit tests for ThermistorSensor using FakeAdc.
// No hardware required.

#include "firmware/sensor/thermistor.h"

#include <cmath>

#include "firmware/adc/fake_adc.h"
#include "pw_unit_test/framework.h"

namespace firmware {
namespace {

constexpr uint8_t kPin = 34;

// Init() calls FakeAdc::Init(pin) and syncs cfg_.adc_max.
TEST(ThermistorSensorTest, InitCallsAdc) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);

    EXPECT_EQ(adc.init_call_count(kPin), 0);
    pw::Status s = sensor.Init();
    EXPECT_TRUE(s.ok());
    EXPECT_EQ(adc.init_call_count(kPin), 1);
}

// Read() at midpoint should produce ~25 °C for the default NTC config.
TEST(ThermistorSensorTest, ReadConvertsRawToTemperature) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);
    sensor.Init();

    // Midpoint of 12-bit ADC: NTC == R_ref → T = T0 = 25 °C exactly.
    adc.SetReading(kPin, adc.max_value() / 2);
    float t = sensor.Read();

    // Allow ±1 °C tolerance for integer midpoint rounding.
    EXPECT_FALSE(std::isnan(t));
    EXPECT_GT(t, 24.0f);
    EXPECT_LT(t, 26.0f);
}

// IsValid() is false before the first successful (non-NaN) Read().
TEST(ThermistorSensorTest, IsValidFalseBeforeFirstGoodRead) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);
    sensor.Init();

    EXPECT_FALSE(sensor.IsValid());
}

// IsValid() stays false when the ADC is at the 0 rail (open circuit).
TEST(ThermistorSensorTest, IsValidFalseOnRailValue) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);
    sensor.Init();

    adc.SetReading(kPin, 0);  // lower rail → NaN from adc_to_celsius
    sensor.Read();

    EXPECT_FALSE(sensor.IsValid());
}

// IsValid() becomes true after a good midpoint read.
TEST(ThermistorSensorTest, IsValidTrueAfterGoodRead) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);
    sensor.Init();

    adc.SetReading(kPin, adc.max_value() / 2);
    sensor.Read();

    EXPECT_TRUE(sensor.IsValid());
}

}  // namespace
}  // namespace firmware
