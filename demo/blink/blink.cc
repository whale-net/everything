// Blink demo for ELEGOO ESP32 (Xtensa LX6).
// Validates that the toolchain, Arduino core, and board pin config are all working.

#include <Arduino.h>
#include "board_pins.h"  // provided by //tools/firmware:board_pins

void setup() {
    Serial.begin(115200);
    pinMode(board::kLed, OUTPUT);
    Serial.printf("Blink starting (LED pin=%d)\n", board::kLed);
}

void loop() {
    digitalWrite(board::kLed, HIGH);
    delay(300);
    digitalWrite(board::kLed, LOW);
    delay(300);
}
