// Host-side unit tests for ThermistorSensor using FakeAdc.
// No hardware required.

#include "firmware/sensor/thermistor.h"

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
    auto r = sensor.Read();

    // Allow ±1 °C tolerance for integer midpoint rounding.
    EXPECT_TRUE(r.valid);
    EXPECT_GT(r.value, 24.0f);
    EXPECT_LT(r.value, 26.0f);
}

// Read() returns Invalid before the first successful (non-NaN) read.
TEST(ThermistorSensorTest, InvalidBeforeFirstGoodRead) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);
    sensor.Init();

    EXPECT_FALSE(sensor.Read().valid);
}

// Read() stays Invalid when the ADC is at the 0 rail (open circuit).
TEST(ThermistorSensorTest, InvalidOnRailValue) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);
    sensor.Init();

    adc.SetReading(kPin, 0);  // lower rail → NaN from adc_to_celsius
    EXPECT_FALSE(sensor.Read().valid);
}

// Read() returns valid after a good midpoint read.
TEST(ThermistorSensorTest, ValidAfterGoodRead) {
    testing::FakeAdc adc;
    ThermistorSensor sensor(kPin, &adc);
    sensor.Init();

    adc.SetReading(kPin, adc.max_value() / 2);
    EXPECT_TRUE(sensor.Read().valid);
}

}  // namespace
}  // namespace firmware
