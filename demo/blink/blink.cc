// Blink demo for ELEGOO ESP32 (Xtensa LX6).
// Validates that the toolchain, Arduino core, and board pin config are all working.

#include <Arduino.h>
#include "board_pins.h"  // provided by //tools/firmware:board_pins
#include "pw_log/log.h"

void setup() {
    Serial.begin(115200);
    pinMode(board::kLed, OUTPUT);
    PW_LOG_INFO("Blink starting (LED pin=%d)", board::kLed);
}

void loop() {
    digitalWrite(board::kLed, HIGH);
    PW_LOG_INFO("LED ON");
    delay(100);
    digitalWrite(board::kLed, LOW);
    PW_LOG_INFO("LED OFF");
    delay(1000);
}
