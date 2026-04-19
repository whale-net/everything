// Sensorboard main loop — board-agnostic.
//
// This file never changes. Board-specific wiring and sensor choices live in
// a separate config file (e.g. elegoo_config.cc) that is linked into the
// esp32_firmware() target.
//
// The config file provides:
//   firmware::II2CBus& GetBus()                        — initialised I2C bus
//   pw::span<firmware::ISensor* const> GetSensors()    — sensor registry
//   firmware::NetworkManager& GetNetwork()             — lazy-init WiFi+MQTT
//   firmware::FirmwarePublisher& GetPublisher()         — proto MQTT publisher

#include <Arduino.h>

#include "board_pins.h"
#include "firmware/i2c/i2c_bus.h"
#include "firmware/mqtt/firmware_publisher.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/sensor.h"
#include "pw_log/log.h"
#include "pw_span/span.h"

// Provided by the linked config file.
firmware::II2CBus& GetBus();
pw::span<firmware::ISensor* const> GetSensors();
firmware::NetworkManager& GetNetwork();
firmware::FirmwarePublisher& GetPublisher();

// Platform keep-alive from firmware/network/esp32_platform.cc.
extern void MQTTLoop();

// CheckSensorNames halts with an error log if any two sensors share a name.
// Duplicate names would cause overlapping sensor_reading rows in the database.
// This runs once at startup from a fixed static array so the loop is trivially small.
static void CheckSensorNames(pw::span<firmware::ISensor* const> sensors) {
    for (size_t i = 0; i < sensors.size(); ++i) {
        for (size_t j = i + 1; j < sensors.size(); ++j) {
            if (strcmp(sensors[i]->name(), sensors[j]->name()) == 0) {
                // Log and spin — duplicate names are a config bug, not a runtime error.
                while (true) {
                    PW_LOG_ERROR(
                        "DUPLICATE SENSOR NAME '%s' at indices %u and %u — "
                        "fix the config and reflash",
                        sensors[i]->name(),
                        static_cast<unsigned>(i),
                        static_cast<unsigned>(j));
                    delay(5000);
                }
            }
        }
    }
}

void setup() {
    Serial.begin(115200);

    CheckSensorNames(GetSensors());

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
    static auto prev_state = firmware::NetworkManager::State::kIdle;

    auto state = GetNetwork().Poll();
    MQTTLoop();

    // On each transition into kReady: publish "online" + manifest.
    if (state == firmware::NetworkManager::State::kReady &&
        prev_state != firmware::NetworkManager::State::kReady) {
        GetPublisher().OnConnect();
    }
    prev_state = state;

    // Publish sensor readings every loop pass while connected.
    if (state == firmware::NetworkManager::State::kReady) {
        GetPublisher().PublishReadings();
    }

    delay(1000);
}
