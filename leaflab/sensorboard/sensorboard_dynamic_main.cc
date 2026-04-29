// Sensorboard main loop — dynamic config variant.
//
// Extends the standard sensorboard_main by loading any persisted DeviceConfig
// from NVS at boot and applying name/enabled overrides before the first sensor
// readings are published.
//
// The config file (elegoo_dynamic_config.cc) additionally provides:
//   firmware::ConfigStore&   GetConfigStore()
//   firmware::ConfigApplier& GetConfigApplier()

#include <Arduino.h>

#include "board_pins.h"
#include "firmware/proto/config.pb.h"
#include "firmware/config/config_applier.h"
#include "firmware/config/config_store.h"
#include "firmware/i2c/i2c_bus.h"
#include "firmware/mqtt/firmware_publisher.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/sensor.h"
#include "pw_log/log.h"
#include "pw_span/span.h"

// Provided by elegoo_dynamic_config.cc
firmware::II2CBus& GetBus();
pw::span<firmware::ISensor* const> GetSensors();
firmware::NetworkManager& GetNetwork();
firmware::FirmwarePublisher& GetPublisher();
firmware::ConfigStore& GetConfigStore();
firmware::ConfigApplier& GetConfigApplier();

extern void MQTTLoop();

static void CheckSensorNames(pw::span<firmware::ISensor* const> sensors) {
    for (size_t i = 0; i < sensors.size(); ++i) {
        for (size_t j = i + 1; j < sensors.size(); ++j) {
            if (strcmp(sensors[i]->name(), sensors[j]->name()) == 0) {
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

    // Load persisted config and apply name overrides before sensor init so
    // the first manifest broadcast uses the correct logical names.
    {
        firmware_DeviceConfig stored = firmware_DeviceConfig_init_zero;
        if (GetConfigStore().Load(&stored).ok()) {
            GetConfigApplier().Apply(stored);
            PW_LOG_INFO("Dynamic config v%" PRIu64 " loaded from NVS",
                        stored.version);
        } else {
            PW_LOG_INFO("No persisted config — using compile-time defaults");
        }
    }

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

#ifndef SENSOR_POLL_INTERVAL_MS
#define SENSOR_POLL_INTERVAL_MS 60000
#endif

void loop() {
    static auto prev_state = firmware::NetworkManager::State::kIdle;
    static uint32_t last_publish_ms = 0;

    auto state = GetNetwork().Poll();
    MQTTLoop();

    if (state == firmware::NetworkManager::State::kReady &&
        prev_state != firmware::NetworkManager::State::kReady) {
        GetPublisher().OnConnect();
    }
    prev_state = state;

    // TODO: use ConfigApplier::PollIntervalMs() for per-sensor scheduling.
    uint32_t now = millis();
    if (state == firmware::NetworkManager::State::kReady &&
        (now - last_publish_ms) >= SENSOR_POLL_INTERVAL_MS) {
        GetPublisher().PublishReadings();
        last_publish_ms = now;
    }

    delay(100);
}
