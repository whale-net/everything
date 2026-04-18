#pragma once

// Pure NTC thermistor math — no Arduino dependency; testable on host.
//
// Circuit (standard voltage divider):
//
//   VCC ── NTC ──┬── R_ref ── GND
//                └── ADC pin
//
// As temperature rises, NTC resistance falls, junction voltage falls,
// and adc_raw falls. At exactly 25°C (T0), NTC = R_ref, adc_raw ≈ adc_max/2.
//
// Temperature is derived from the B-parameter (two-point) NTC equation:
//
//   R_ntc = R_ref * (adc_max / adc_raw - 1)
//   T_K   = 1 / (1/T0 + (1/B) * ln(R_ntc / R0))
//   T_C   = T_K - 273.15

#include <cmath>
#include <limits>

namespace firmware {
namespace thermistor {

struct Config {
    float r_ref     = 10000.0f;  // series resistor to GND (Ω)
    float r0        = 10000.0f;  // NTC resistance at T0 (Ω)
    float t0_kelvin = 298.15f;   // reference temperature (K) = 25 °C
    float b_coeff   = 3950.0f;   // NTC B-coefficient (K)
    int   adc_max   = 4095;      // ADC full-scale (ESP32 = 12-bit = 4095)
};

// Convert a raw ADC reading to °C using the B-parameter equation.
//
// Returns NaN when adc_raw is at either rail (open or short circuit).
inline float adc_to_celsius(int adc_raw, const Config& cfg = Config{}) {
    if (adc_raw <= 0 || adc_raw >= cfg.adc_max) {
        return std::numeric_limits<float>::quiet_NaN();
    }
    float r_ntc = cfg.r_ref * (static_cast<float>(cfg.adc_max) / adc_raw - 1.0f);
    float inv_t = (1.0f / cfg.t0_kelvin) + (1.0f / cfg.b_coeff) * std::log(r_ntc / cfg.r0);
    return 1.0f / inv_t - 273.15f;
}

}  // namespace thermistor
}  // namespace firmware
