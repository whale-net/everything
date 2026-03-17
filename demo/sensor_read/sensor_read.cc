// Thermistor sensor read demo for ELEGOO ESP32.
//
// Reads a generic 10 kΩ NTC thermistor wired as a voltage divider:
//
//   3.3V ── NTC ──┬── 10 kΩ ── GND
//                 └── GPIO 34 (board::kAdc0)
//
// Temperature is logged once per second via pw_log.
// Flash:  bazel run //demo/sensor_read:flash -- /dev/ttyUSB0

#include <Arduino.h>

#include "board_pins.h"              // board::kAdc0
#include "firmware/sensor/thermistor.h"
#include "pw_log/log.h"

namespace {
firmware::ThermistorSensor thermistor(board::kAdc0);
}

void setup() {
    Serial.begin(115200);
    auto status = thermistor.Init();
    if (!status.ok()) {
        PW_LOG_ERROR("Thermistor init failed");
    } else {
        PW_LOG_INFO("Thermistor ready on ADC pin %d", board::kAdc0);
    }
}

void loop() {
    float temp_c = thermistor.Read();
    PW_LOG_INFO("Temperature: %.1f C", temp_c);
    delay(1000);
}
