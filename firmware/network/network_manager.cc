// NetworkManager implementation.
//
// Platform dependencies are injected via the build system:
//   - ESP32 target: WiFi.h + PubSubClient (real hardware)
//   - Host target:  stub implementations (see network_manager_stub.h)
//
// This file contains only the state-machine logic, which is
// fully testable on the host without hardware.

#include "firmware/network/network_manager.h"

#include "pw_log/log.h"

// Platform hooks — implemented in esp32_platform.cc on-device, or stubbed in
// host unit tests.  Declared at global scope so both targets can define them
// without namespace qualification.
extern bool WiFiIsConnected();
extern bool MQTTConnect(const char* host, uint16_t port, const char* id,
                        const char* user, const char* pass,
                        const char* lwt_topic, const char* lwt_payload);
extern bool MQTTIsConnected();
extern bool MQTTPublish(const char* topic, const char* payload);
extern bool MQTTPublishBinary(const char* topic, const uint8_t* data,
                               size_t len, bool retained);
// Called when transitioning to kConnecting to (re-)initiate the Wi-Fi
// association.  On ESP32 with setAutoReconnect(true), this is a no-op for
// the initial connect; the hook exists so the state machine can explicitly
// trigger reconnection without coupling to WiFi.h.
extern void WiFiConnect();
// Drive the PubSubClient keep-alive.  Called by the application loop every
// pass (not by NetworkManager itself).
extern void MQTTLoop();
// Monotonic millisecond clock.
// On ESP32: returns millis(). On host tests: returns std::chrono wall time.
extern uint32_t PlatformNowMs();

namespace firmware {

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
  if (state_age_ms() >= config_.connect_timeout_ms) {
    PW_LOG_WARN("NetworkManager: connect timeout after %u ms, backing off",
                state_age_ms());
    backoff_attempt_++;
    TransitionTo(State::kBackoff);
    return;
  }

  if (!WiFiIsConnected()) return;  // Still waiting for DHCP.

  // Skip MQTT entirely if no broker is configured — WiFi-only mode.
  bool mqtt_host_set = config_.mqtt_host != nullptr && config_.mqtt_host[0] != '\0';
  if (mqtt_host_set) {
    bool mqtt_ok = MQTTConnect(config_.mqtt_host, config_.mqtt_port,
                                config_.device_id,
                                config_.mqtt_user, config_.mqtt_pass,
                                config_.lwt_topic, config_.lwt_payload);
    if (!mqtt_ok) return;  // MQTT handshake still in progress.
  }

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

pw::Status NetworkManager::Publish(const char* topic, const uint8_t* data,
                                    size_t len, bool retained) {
  if (state_ != State::kReady) {
    PW_LOG_DEBUG("NetworkManager: publish skipped, state=%s",
                 StateToString(state_));
    return pw::Status::Unavailable();
  }
  if (!MQTTPublishBinary(topic, data, len, retained)) {
    return pw::Status::Internal();
  }
  return pw::OkStatus();
}

uint32_t NetworkManager::state_age_ms() const {
  return PlatformNowMs() - state_entered_ms_;
}

void NetworkManager::TransitionTo(State next) {
  PW_LOG_DEBUG("NetworkManager: %s → %s",
               StateToString(state_), StateToString(next));
  state_ = next;
  state_entered_ms_ = PlatformNowMs();
  if (next == State::kConnecting) {
    WiFiConnect();
  }
}

uint32_t NetworkManager::NextBackoffMs() const {
  // Exponential backoff: 1 s * 2^attempt, capped at kMaxBackoffSeconds.
  // Cap shift at 31 to prevent UB when backoff_attempt_ >= 32.
  uint32_t shift = backoff_attempt_ < 31u ? backoff_attempt_ : 31u;
  uint32_t seconds = 1u << shift;
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
