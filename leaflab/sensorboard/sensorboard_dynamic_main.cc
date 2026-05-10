// Sensorboard main loop — dynamic config variant.
//
// Reset lifecycle:
//   - Power cycle / normal boot: load NVS config → apply → connect.
//   - MQTT config push: queued in callback, applied from loop() (I2C-safe).
//   - MQTT "reset" command: soft restart, config preserved.
//   - MQTT "factory_reset" command: clear NVS config → restart (blank slate).
//   - GPIO 0 (BOOT button, active-low): soft restart.
//
// No sensors are compiled in — all sensor instances are factory-created by
// ConfigApplier from the DeviceConfig pushed over MQTT.

#include <Arduino.h>
#include <esp_system.h>

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

// GPIO 0 is the BOOT button on most ESP32 boards (active-low).
static constexpr int kResetPin = 0;

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

static void DoReset(firmware::FirmwarePublisher::PendingReset reset) {
    GetPublisher().PublishOffline();
    delay(200);
    if (reset == firmware::FirmwarePublisher::PendingReset::kFactory) {
        PW_LOG_INFO("Factory reset: clearing NVS config");
        GetConfigStore().Clear();
        delay(100);
    }
    PW_LOG_INFO("Restarting...");
    delay(100);
    esp_restart();
}

void setup() {
    Serial.begin(115200);
    pinMode(kResetPin, INPUT_PULLUP);

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

    // Apply queued config and handle reset requests. Safe to do I2C here.
    auto reset = GetPublisher().ProcessPending();
    if (reset != firmware::FirmwarePublisher::PendingReset::kNone) {
        DoReset(reset);
        return;  // unreachable, but keeps the compiler happy
    }

    // GPIO 0 (BOOT button, active-low): soft reset on long press.
    if (digitalRead(kResetPin) == LOW) {
        delay(50);  // debounce
        if (digitalRead(kResetPin) == LOW) {
            PW_LOG_INFO("GPIO reset button held");
            DoReset(firmware::FirmwarePublisher::PendingReset::kSoft);
        }
    }

    // TODO: use ConfigApplier::PollIntervalMs() for per-sensor scheduling.
    uint32_t now = millis();
    if (state == firmware::NetworkManager::State::kReady &&
        (now - last_publish_ms) >= SENSOR_POLL_INTERVAL_MS) {
        GetPublisher().PublishReadings();
        last_publish_ms = now;
    }

    delay(100);
}
