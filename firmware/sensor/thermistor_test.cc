// Unit tests for thermistor math.
//
// Tests the pure adc_to_celsius() function — no Arduino dependency, runs on host.
//
// Reference values derived from the B-parameter equation with the default
// Config (R_ref = R0 = 10 kΩ, B = 3950, T0 = 25 °C, adc_max = 4095):
//
//   ~0 °C  → adc_raw ≈  938   (NTC ≈ 33.6 kΩ)
//   ~25 °C → adc_raw ≈ 2047   (NTC ≈ R0 = 10 kΩ, midpoint)
//   ~50 °C → adc_raw ≈ 3012   (NTC ≈  3.6 kΩ)

#include "firmware/sensor/thermistor_math.h"

#include <cmath>

#include "pw_unit_test/framework.h"

using firmware::thermistor::Config;
using firmware::thermistor::adc_to_celsius;

namespace {
constexpr float kEpsilon = 1.0f;  // ±1 °C tolerance for integer ADC inputs
}

TEST(ThermistorMath, MidpointIsRoomTemperature) {
    // At adc_raw = adc_max/2, R_ntc ≈ R_ref, so T ≈ T0 = 25 °C.
    float temp = adc_to_celsius(2047);
    EXPECT_GT(temp, 25.0f - kEpsilon);
    EXPECT_LT(temp, 25.0f + kEpsilon);
}

TEST(ThermistorMath, LowRawIsCold) {
    // adc_raw ≈ 938 → T ≈ 0 °C
    float temp = adc_to_celsius(938);
    EXPECT_GT(temp, 0.0f - kEpsilon);
    EXPECT_LT(temp, 0.0f + kEpsilon);
}

TEST(ThermistorMath, HighRawIsHot) {
    // adc_raw ≈ 3012 → T ≈ 50 °C
    float temp = adc_to_celsius(3012);
    EXPECT_GT(temp, 50.0f - kEpsilon);
    EXPECT_LT(temp, 50.0f + kEpsilon);
}

TEST(ThermistorMath, OpenCircuitReturnsNaN) {
    // adc_raw == 0 means ADC pin is floating low (broken wire).
    EXPECT_TRUE(std::isnan(adc_to_celsius(0)));
}

TEST(ThermistorMath, ShortCircuitReturnsNaN) {
    // adc_raw == adc_max means NTC is shorted to VCC.
    EXPECT_TRUE(std::isnan(adc_to_celsius(4095)));
}

TEST(ThermistorMath, CustomConfigScalesCorrectly) {
    // With a 100 kΩ reference resistor and 100 kΩ NTC (B=3950),
    // the midpoint should still be ~25 °C.
    Config cfg;
    cfg.r_ref = 100000.0f;
    cfg.r0 = 100000.0f;
    float temp = adc_to_celsius(2047, cfg);
    EXPECT_GT(temp, 25.0f - kEpsilon);
    EXPECT_LT(temp, 25.0f + kEpsilon);
}
