// Sensorboard main loop — dynamic config variant.
//
// On boot: init I2C bus, load any persisted DeviceConfig from NVS, apply it
// (which instantiates and inits sensors from chip_type), then connect to MQTT.
// On config push: FirmwarePublisher calls ConfigApplier::Apply() which
// destroys old sensor instances and creates new ones from the incoming config.
//
// No sensors are compiled in — all sensor instances are factory-created by
// ConfigApplier from the DeviceConfig pushed over MQTT.
//
// The config file (elegoo_*_dynamic_config.cc) additionally provides:
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

    // Bus must be up before Apply() — it calls sensor->Init() which does I2C.
    if (!GetBus().Init(board::kSda, board::kScl).ok()) {
        PW_LOG_ERROR("I2C bus init failed");
    }

    // Load persisted config and instantiate sensors from chip_type entries.
    // On a fresh device with no NVS config, sensor list stays empty until
    // the first DeviceConfig is pushed over MQTT.
    {
        firmware_DeviceConfig stored = firmware_DeviceConfig_init_zero;
        if (GetConfigStore().Load(&stored).ok()) {
            GetConfigApplier().Apply(stored);
            PW_LOG_INFO("Dynamic config v%" PRIu64 " loaded: %zu sensors",
                        stored.version, GetSensors().size());
        } else {
            PW_LOG_INFO("No persisted config — waiting for DeviceConfig push");
        }
    }

    CheckSensorNames(GetSensors());

    for (firmware::ISensor* s : GetSensors()) {
        PW_LOG_INFO("Sensor ready: %s @ 0x%02x", s->name(), s->address());
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
