// Blink demo for ELEGOO ESP32 (Xtensa LX6).
//
// Uses pw_log instead of Serial.print so logging can be swapped for
// tokenized / RPC-based log transports without changing application code.

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
    PW_LOG_DEBUG("LED on");
    delay(1000);

    digitalWrite(board::kLed, LOW);
    PW_LOG_DEBUG("LED off");
    delay(1000);
}
