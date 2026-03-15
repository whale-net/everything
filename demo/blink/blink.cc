// Blink demo for ELEGOO ESP32 (Xtensa LX6).
//
// Uses pw_log instead of Serial.print so logging can be swapped for
// tokenized / RPC-based log transports without changing application code.

#include <Arduino.h>
#include "pw_log/log.h"

namespace {
constexpr int kLedPin = 2;  // Built-in LED on most ESP32 dev boards
}  // namespace

void setup() {
    Serial.begin(115200);
    pinMode(kLedPin, OUTPUT);
    PW_LOG_INFO("Blink starting on ELEGOO ESP32");
}

void loop() {
    digitalWrite(kLedPin, HIGH);
    PW_LOG_DEBUG("LED on");
    delay(1000);

    digitalWrite(kLedPin, LOW);
    PW_LOG_DEBUG("LED off");
    delay(1000);
}
