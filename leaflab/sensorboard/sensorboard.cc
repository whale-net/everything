// Sensorboard firmware — BH1750 ambient light sensor via I2C.
// Wiring: SDA→GPIO21, SCL→GPIO22, ADDR→GND (address 0x23).

#include <Arduino.h>
#include <Wire.h>
#include "board_pins.h"
#include "pw_log/log.h"

static constexpr uint8_t kBH1750Addr     = 0x23;  // ADDR pin = GND
static constexpr uint8_t kOneTimeHighRes = 0x20;  // one-shot, 1 lx resolution

// Trigger one high-res measurement, wait for completion, return lux.
// Returns -1 on error.
static float read_lux() {
    Wire.beginTransmission(kBH1750Addr);
    Wire.write(kOneTimeHighRes);
    if (Wire.endTransmission() != 0) {
        PW_LOG_ERROR("BH1750 write failed");
        return -1.0f;
    }
    delay(180);
    if (Wire.requestFrom(kBH1750Addr, (uint8_t)2) != 2) {
        PW_LOG_ERROR("BH1750 read failed");
        return -1.0f;
    }
    uint16_t raw = ((uint16_t)Wire.read() << 8) | Wire.read();
    return raw / 1.2f;
}

void setup() {
    Serial.begin(115200);
    Wire.begin(board::kSda, board::kScl);
    Wire.setTimeOut(100);
    PW_LOG_INFO("Sensorboard starting (SDA=%d SCL=%d)", board::kSda, board::kScl);
}

void loop() {
    float lux = read_lux();
    if (lux >= 0.0f) {
        int whole = (int)lux;
        int tenths = (int)((lux - whole) * 10);
        PW_LOG_INFO("Light: %d.%d lx", whole, tenths);
    }
    delay(820);  // 820 ms + 180 ms measurement = ~1 s per reading
}
