// I2C bus scanner — probes all 7-bit addresses and logs which ones ACK.
//
// Usage:
//   bazel run //tools/firmware/esp32/i2c_scanner:flash -- /dev/ttyUSB0
//
// Useful for confirming wiring before writing sensor firmware.
// Expected output when a BH1750 is connected with ADDR=GND:
//   INF  Device found at 0x23
//   INF  Scan complete (1 device(s))

#include <Arduino.h>
#include <Wire.h>
#include "board_pins.h"
#include "pw_log/log.h"

void setup() {
    Serial.begin(115200);
    Wire.begin(board::kSda, board::kScl);
    Wire.setTimeOut(100);
    PW_LOG_INFO("I2C scanner (SDA=%d SCL=%d)", board::kSda, board::kScl);
}

void loop() {
    int found = 0;
    for (uint8_t addr = 1; addr < 127; addr++) {
        Wire.beginTransmission(addr);
        if (Wire.endTransmission() == 0) {
            PW_LOG_INFO("Device found at 0x%02X", addr);
            found++;
        }
    }
    if (found == 0) {
        PW_LOG_WARN("No devices found — check wiring");
    } else {
        PW_LOG_INFO("Scan complete (%d device(s))", found);
    }
    delay(3000);
}
