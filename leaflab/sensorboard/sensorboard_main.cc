// Sensorboard main loop — board-agnostic.
//
// This file never changes. Board-specific wiring and sensor choices live in
// a separate config file (e.g. elegoo_config.cc) that is linked into the
// esp32_firmware() target.
//
// The config file provides three functions:
//   firmware::II2CBus& GetBus()                        — initialised I2C bus
//   pw::span<firmware::ISensor* const> GetSensors()    — sensor registry
//   firmware::NetworkManager& GetNetwork()             — lazy-init WiFi+MQTT

#include <Arduino.h>

#include "board_pins.h"
#include "firmware/i2c/i2c_bus.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/sensor.h"
#include "pw_log/log.h"
#include "pw_span/span.h"

// Provided by the linked config file.
firmware::II2CBus& GetBus();
pw::span<firmware::ISensor* const> GetSensors();
firmware::NetworkManager& GetNetwork();

// Platform keep-alive from firmware/network/esp32_platform.cc.
// Must be called every loop() pass when connected.
extern void MQTTLoop();

void setup() {
    Serial.begin(115200);

    if (!GetBus().Init(board::kSda, board::kScl).ok()) {
        PW_LOG_ERROR("I2C bus init failed");
    }

    for (firmware::ISensor* s : GetSensors()) {
        pw::Status st = s->Init();
        if (!st.ok()) {
            PW_LOG_ERROR("Sensor init failed: %s", s->name());
        } else {
            PW_LOG_INFO("Sensor ready: %s @ 0x%02x", s->name(), s->address());
        }
    }

    GetNetwork().Connect();
}

void loop() {
    GetNetwork().Poll();
    MQTTLoop();

    for (firmware::ISensor* s : GetSensors()) {
        firmware::SensorReading r = s->Read();
        if (r.valid) {
            int whole  = static_cast<int>(r.value);
            int tenths = static_cast<int>((r.value - whole) * 10);
            //PW_LOG_INFO("%s: %d.%d", s->name(), whole, tenths);
        }
    }

    delay(1000);
}
