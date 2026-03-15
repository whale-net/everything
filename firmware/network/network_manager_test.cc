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
bool WiFiIsConnected() { return g_wifi_connected; }
bool MQTTConnect(const char*, uint16_t, const char*, const char*,
                 const char*) {
  return g_mqtt_connected;
}
bool MQTTIsConnected() { return g_mqtt_connected; }
bool MQTTPublish(const char*, const char*) { return g_mqtt_publish_ok; }

// ── Tests ─────────────────────────────────────────────────────────────────────

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
  };
}

TEST(NetworkManagerTest, InitialStateIsIdle) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  EXPECT_EQ(nm.state(), NetworkManager::State::kIdle);
}

TEST(NetworkManagerTest, ConnectTransitionsToConnecting) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  EXPECT_EQ(nm.state(), NetworkManager::State::kConnecting);
}

TEST(NetworkManagerTest, ConnectIsNoopWhenAlreadyConnecting) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Connect();  // Second call: should not crash or reset state.
  EXPECT_EQ(nm.state(), NetworkManager::State::kConnecting);
}

TEST(NetworkManagerTest, PollTransitionsToReadyWhenBothUp) {
  g_wifi_connected = true;
  g_mqtt_connected = true;

  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Poll();  // PollConnecting: both up → kReady

  EXPECT_EQ(nm.state(), NetworkManager::State::kReady);

  g_wifi_connected = false;
  g_mqtt_connected = false;
}

TEST(NetworkManagerTest, PollStaysConnectingWhenWiFiDown) {
  g_wifi_connected = false;
  g_mqtt_connected = false;

  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Poll();

  EXPECT_EQ(nm.state(), NetworkManager::State::kConnecting);
}

TEST(NetworkManagerTest, PublishFailsWhenNotReady) {
  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  pw::Status s = nm.Publish("home/temp", "23.5");
  EXPECT_EQ(s, pw::Status::Unavailable());
}

TEST(NetworkManagerTest, PublishSucceedsWhenReady) {
  g_wifi_connected = true;
  g_mqtt_connected = true;
  g_mqtt_publish_ok = true;

  auto cfg = TestConfig();
  NetworkManager nm(cfg);
  nm.Connect();
  nm.Poll();  // → kReady

  pw::Status s = nm.Publish("home/temp", "23.5");
  EXPECT_EQ(s, pw::OkStatus());

  g_wifi_connected = false;
  g_mqtt_connected = false;
}

TEST(NetworkManagerTest, StateToStringCoversAllStates) {
  EXPECT_STREQ(StateToString(NetworkManager::State::kIdle),       "kIdle");
  EXPECT_STREQ(StateToString(NetworkManager::State::kConnecting), "kConnecting");
  EXPECT_STREQ(StateToString(NetworkManager::State::kReady),      "kReady");
  EXPECT_STREQ(StateToString(NetworkManager::State::kBackoff),    "kBackoff");
}

}  // namespace
}  // namespace firmware
