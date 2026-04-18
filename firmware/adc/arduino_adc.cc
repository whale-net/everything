// ArduinoAdc — Arduino-side ADC implementation.
// Calls analogRead() / analogReadResolution() / pinMode().
// May only be compiled for device targets (target_compatible_with enforced in BUILD.bazel).

#include "firmware/adc/arduino_adc.h"

#include <Arduino.h>

namespace firmware {

pw::Status ArduinoAdc::Init(uint8_t pin) {
    analogReadResolution(12);  // ESP32 ADC: 12-bit (0–4095); idempotent
    pinMode(pin, INPUT);
    return pw::OkStatus();
}

int ArduinoAdc::Read(uint8_t pin) {
    return analogRead(pin);
}

}  // namespace firmware
