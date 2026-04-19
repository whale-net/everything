// Host-side unit tests for the NetworkManager state machine.
//
// All Wi-Fi / MQTT platform calls are stubbed out below so these tests
// run instantly on the host, exercising only the state-machine logic.
//
//   bazel test //firmware/network:network_manager_test

#include "firmware/network/network_manager.h"
#include "pw_unit_test/framework.h"

// ── Platform stubs (host-side only) ──────────────────────────────────────────
//
// The real ESP32 implementations live in firmware/network/esp32_platform.cc.
// These stubs are compiled in only when building the host test target.

namespace {
bool g_wifi_connected = false;
bool g_mqtt_connected = false;
bool g_mqtt_publish_ok = true;
}  // namespace

// Declared extern in network_manager.cc
#include <chrono>
uint32_t PlatformNowMs() {
  return static_cast<uint32_t>(
      std::chrono::duration_cast<std::chrono::milliseconds>(
          std::chrono::steady_clock::now().time_since_epoch())
          .count());
}
bool WiFiIsConnected() { return g_wifi_connected; }
bool MQTTConnect(const char*, uint16_t, const char*, const char*, const char*,
                 const char*, const char*) {
  return g_mqtt_connected;
}
bool MQTTIsConnected() { return g_mqtt_connected; }
bool MQTTPublish(const char*, const char*) { return g_mqtt_publish_ok; }
bool MQTTPublishBinary(const char*, const uint8_t*, size_t, bool) {
  return g_mqtt_publish_ok;
}
void WiFiConnect() {}

// ── Test fixture ──────────────────────────────────────────────────────────────

namespace firmware {
namespace {

NetworkManager::Config TestConfig() {
  return {
      .ssid = "test_ssid",
      .password = "test_pass",
      .mqtt_host = "broker.local",
      .mqtt_port = 1883,
      .device_id = "esp32_test_aa:bb:cc",
      .mqtt_user = nullptr,
      .mqtt_pass = nullptr,
      .connect_timeout_ms = 15'000,
  };
}

class NetworkManagerTest : public ::testing::Test {
 protected:
  void SetUp() override {
    g_wifi_connected = false;
    g_mqtt_connected = false;
    g_mqtt_publish_ok = true;
  }
};

// ── Tests ─────────────────────────────────────────────────────────────────────

TEST_F(NetworkManagerTest, InitialStateIsIdle) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  EXPECT_EQ(nm.state(), NetworkManager::State::kIdle);
}

TEST_F(NetworkManagerTest, ConnectTransitionsToConnecting) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  EXPECT_EQ(nm.state(), NetworkManager::State::kConnecting);
}

TEST_F(NetworkManagerTest, ConnectIsNoopWhenAlreadyConnecting) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Connect();  // Second call: should not crash or reset state.
  EXPECT_EQ(nm.state(), NetworkManager::State::kConnecting);
}

TEST_F(NetworkManagerTest, PollTransitionsToReadyWhenBothUp) {
  g_wifi_connected = true;
  g_mqtt_connected = true;

  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Poll();  // PollConnecting: both up → kReady

  EXPECT_EQ(nm.state(), NetworkManager::State::kReady);
}

TEST_F(NetworkManagerTest, PollStaysConnectingWhenWiFiDown) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Poll();

  EXPECT_EQ(nm.state(), NetworkManager::State::kConnecting);
}

TEST_F(NetworkManagerTest, PublishFailsWhenNotReady) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  pw::Status s = nm.Publish("home/temp", "23.5");
  EXPECT_EQ(s, pw::Status::Unavailable());
}

TEST_F(NetworkManagerTest, PublishSucceedsWhenReady) {
  g_wifi_connected = true;
  g_mqtt_connected = true;

  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Poll();  // → kReady

  pw::Status s = nm.Publish("home/temp", "23.5");
  EXPECT_EQ(s, pw::OkStatus());
}

TEST_F(NetworkManagerTest, StateToStringCoversAllStates) {
  EXPECT_STREQ(StateToString(NetworkManager::State::kIdle),       "kIdle");
  EXPECT_STREQ(StateToString(NetworkManager::State::kConnecting), "kConnecting");
  EXPECT_STREQ(StateToString(NetworkManager::State::kReady),      "kReady");
  EXPECT_STREQ(StateToString(NetworkManager::State::kBackoff),    "kBackoff");
}

TEST_F(NetworkManagerTest, ConnectTimeoutTransitionsToBackoff) {
  auto cfg = TestConfig();
  cfg.connect_timeout_ms = 0;  // expire immediately
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Poll();  // state_age_ms() > 0 → backoff
  EXPECT_EQ(nm.state(), NetworkManager::State::kBackoff);
}

}  // namespace
}  // namespace firmware
