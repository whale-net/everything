#pragma once

// Non-blocking Wi-Fi + MQTT network state machine.
//
// State diagram:
//
//   ┌────────────────────────────────────────────────────────────┐
//   │                                                            │
//   ▼                                                            │
//  kIdle ──connect()──► kConnecting ──WiFi OK──► kReady ──lost──► kBackoff
//                             │                                      │
//                             └──────────── timeout ─────────────────┘
//                                                │
//                                     exponential delay expires
//                                                │
//                                            kConnecting
//
// Key design constraints:
//   - Poll() must return in < 1 ms (never blocks).
//   - No delay(), no busy-wait.
//   - Exponential backoff: 1 s → 2 s → 4 s → … capped at kMaxBackoffSeconds.
//   - WDT is fed externally by the caller between Poll() calls.
//
// Usage:
//   NetworkManager net(ssid, password, mqtt_host, mqtt_port, device_id);
//   net.Connect();
//
//   void loop() {
//     net.Poll();                        // drive the state machine
//     if (net.state() == NetworkManager::State::kReady) {
//       mqtt_client.loop();              // keep-alive, non-blocking
//       if (sensor_timer.IsReady()) {
//         net.Publish(topic, payload);
//         sensor_timer.Reset();
//       }
//     }
//     esp_task_wdt_reset();
//   }

#include <cstdint>

#include "pw_log/log.h"
#include "pw_status/status.h"

namespace firmware {

class NetworkManager {
 public:
  enum class State : uint8_t {
    kIdle,        // No connection attempt in progress.
    kConnecting,  // Wi-Fi association + DHCP + MQTT connect in progress.
    kReady,       // Wi-Fi and MQTT are both up; safe to publish.
    kBackoff,     // Connection failed; waiting before retry.
  };

  static constexpr uint32_t kMaxBackoffSeconds = 64;

  struct Config {
    const char* ssid;
    const char* password;
    const char* mqtt_host;
    uint16_t    mqtt_port;
    const char* device_id;          // Used as MQTT client ID (e.g. eFuse MAC string)
    const char* mqtt_user;          // nullptr = no auth
    const char* mqtt_pass;          // nullptr = no auth
    uint32_t connect_timeout_ms = 15'000;  // override in tests for fast timeout
  };

  explicit NetworkManager(const Config& config) : config_(config) {}

  // Override the device_id after construction.  Call before Connect() so the
  // kConnecting handler uses the updated value.
  void set_device_id(const char* id) { config_.device_id = id; }

  // Initiate a connection attempt.  No-op if already kConnecting or kReady.
  void Connect();

  // Drive the state machine.  Must be called every loop() iteration.
  // Returns current state after processing.
  State Poll();

  // Publish a payload to topic.  Returns pw::OkStatus() only when kReady.
  // Does NOT block; if not ready, caller should buffer or discard.
  pw::Status Publish(const char* topic, const char* payload);

  State state() const { return state_; }

  // Milliseconds spent in the current state (for diagnostics).
  uint32_t state_age_ms() const;

 private:
  void TransitionTo(State next);
  void PollConnecting();
  void PollReady();
  void PollBackoff();

  uint32_t NextBackoffMs() const;

  Config config_;
  State state_ = State::kIdle;
  uint32_t state_entered_ms_ = 0;  // PlatformNowMs() when state last changed.
  uint32_t backoff_attempt_ = 0;   // Increments on each consecutive failure.
};

// Returns human-readable state name for logging.
const char* StateToString(NetworkManager::State state);

}  // namespace firmware
