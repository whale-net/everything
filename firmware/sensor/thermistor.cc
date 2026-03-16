// ThermistorSensor — Arduino-side implementation.
// This file calls analogRead() / analogReadResolution() and may only be
// compiled for device targets (target_compatible_with enforced in BUILD.bazel).

#include "firmware/sensor/thermistor.h"

#include <Arduino.h>
#include <cmath>

#include "pw_status/status.h"

namespace firmware {

pw::Status ThermistorSensor::Init() {
    analogReadResolution(12);  // ESP32 ADC: 12-bit (0–4095)
    pinMode(pin_, INPUT);
    return pw::OkStatus();
}

float ThermistorSensor::Read() {
    int raw = analogRead(pin_);
    float temp = thermistor::adc_to_celsius(raw, cfg_);
    if (!std::isnan(temp)) {
        last_valid_ = temp;
    }
    return last_valid_;
}

}  // namespace firmware
