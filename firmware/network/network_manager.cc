// NetworkManager implementation.
//
// Platform dependencies are injected via the build system:
//   - ESP32 target: WiFi.h + PubSubClient (real hardware)
//   - Host target:  stub implementations (see network_manager_stub.h)
//
// This file contains only the state-machine logic, which is
// fully testable on the host without hardware.

#include "firmware/network/network_manager.h"

#include <chrono>

#include "pw_log/log.h"

// Platform hooks — implemented in esp32_platform.cc on-device, or stubbed in
// host unit tests.  Declared at global scope so both targets can define them
// without namespace qualification.
extern bool WiFiIsConnected();
extern bool MQTTConnect(const char*, uint16_t, const char*, const char*,
                        const char*);
extern bool MQTTIsConnected();
extern bool MQTTPublish(const char*, const char*);

namespace firmware {

namespace {

// Connection attempt timeout before entering backoff.
constexpr auto kConnectTimeoutMs = std::chrono::milliseconds(15'000);

uint32_t NowMs() {
  // Converts pw_chrono time point to uint32_t milliseconds.
  // On host: uses std::chrono steady_clock backend.
  // On ESP32: uses FreeRTOS tick backend.
  auto duration = pw::chrono::SystemClock::now().time_since_epoch();
  return static_cast<uint32_t>(
      std::chrono::duration_cast<std::chrono::milliseconds>(duration).count());
}

}  // namespace

void NetworkManager::Connect() {
  if (state_ == State::kConnecting || state_ == State::kReady) return;
  TransitionTo(State::kConnecting);
}

NetworkManager::State NetworkManager::Poll() {
  switch (state_) {
    case State::kIdle:
      break;
    case State::kConnecting:
      PollConnecting();
      break;
    case State::kReady:
      PollReady();
      break;
    case State::kBackoff:
      PollBackoff();
      break;
  }
  return state_;
}

void NetworkManager::PollConnecting() {
  if (state_age_ms() > static_cast<uint32_t>(
          std::chrono::duration_cast<std::chrono::milliseconds>(
              kConnectTimeoutMs).count())) {
    PW_LOG_WARN("NetworkManager: connect timeout after %u ms, backing off",
                state_age_ms());
    backoff_attempt_++;
    TransitionTo(State::kBackoff);
    return;
  }

  if (!WiFiIsConnected()) return;  // Still waiting for DHCP.

  bool mqtt_ok = MQTTConnect(config_.mqtt_host, config_.mqtt_port,
                              config_.device_id,
                              config_.mqtt_user, config_.mqtt_pass);
  if (!mqtt_ok) return;  // MQTT handshake still in progress.

  PW_LOG_INFO("NetworkManager: connected (attempt %u)", backoff_attempt_ + 1);
  backoff_attempt_ = 0;
  TransitionTo(State::kReady);
}

void NetworkManager::PollReady() {
  if (!WiFiIsConnected() || !MQTTIsConnected()) {
    PW_LOG_WARN("NetworkManager: connection lost, backing off");
    backoff_attempt_++;
    TransitionTo(State::kBackoff);
  }
}

void NetworkManager::PollBackoff() {
  if (state_age_ms() >= NextBackoffMs()) {
    PW_LOG_INFO("NetworkManager: backoff complete, retrying (attempt %u)",
                backoff_attempt_ + 1);
    TransitionTo(State::kConnecting);
  }
}

pw::Status NetworkManager::Publish(const char* topic, const char* payload) {
  if (state_ != State::kReady) {
    PW_LOG_DEBUG("NetworkManager: publish skipped, state=%s",
                 StateToString(state_));
    return pw::Status::Unavailable();
  }
  if (!MQTTPublish(topic, payload)) {
    return pw::Status::Internal();
  }
  return pw::OkStatus();
}

uint32_t NetworkManager::state_age_ms() const {
  auto now = pw::chrono::SystemClock::now();
  auto elapsed = now - state_entered_;
  return static_cast<uint32_t>(
      std::chrono::duration_cast<std::chrono::milliseconds>(elapsed).count());
}

void NetworkManager::TransitionTo(State next) {
  PW_LOG_DEBUG("NetworkManager: %s → %s",
               StateToString(state_), StateToString(next));
  state_ = next;
  state_entered_ = pw::chrono::SystemClock::now();
}

uint32_t NetworkManager::NextBackoffMs() const {
  // Exponential backoff: 1 s * 2^attempt, capped at kMaxBackoffSeconds.
  uint32_t seconds = 1u << backoff_attempt_;
  if (seconds > kMaxBackoffSeconds) seconds = kMaxBackoffSeconds;
  return seconds * 1000u;
}

const char* StateToString(NetworkManager::State state) {
  switch (state) {
    case NetworkManager::State::kIdle:       return "kIdle";
    case NetworkManager::State::kConnecting: return "kConnecting";
    case NetworkManager::State::kReady:      return "kReady";
    case NetworkManager::State::kBackoff:    return "kBackoff";
  }
  return "kUnknown";
}

}  // namespace firmware
