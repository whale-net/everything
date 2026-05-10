// Host-side tests for the network_send demo.
//
// Tests the full application stack without hardware:
//
//   FakeSensor  →  MQTTWriter  →  [FakePublisher | NetworkPublisher]
//                                          ↓
//                                  NetworkManager  ←  platform stubs
//
// Two test layers:
//   1. Message formatting — MQTTWriter + FakeSensor + FakePublisher.
//      Verifies topic construction and payload format in isolation.
//   2. Full-stack integration — MQTTWriter + FakeSensor + NetworkPublisher
//      + NetworkManager driven by controllable platform stubs.
//      Verifies connect → ready → publish → disconnect → backoff → reconnect.
//
// Run:
//   bazel test //demo/network_send:network_send_test

#include <cstring>
#include <vector>

#include "firmware/mqtt/mock_publisher.h"
#include "firmware/mqtt/mqtt_writer.h"
#include "firmware/network/network_manager.h"
#include "firmware/network/network_publisher.h"
#include "firmware/sensor/mock_sensor.h"
#include "pw_status/status.h"
#include "pw_unit_test/framework.h"

// ── Platform stubs ────────────────────────────────────────────────────────────
// Satisfy the extern declarations in network_manager.cc.
// Controlled by globals so individual tests can set exact conditions.

namespace {
bool g_wifi_up   = false;
bool g_mqtt_up   = false;
bool g_mqtt_publish_ok = true;

struct CapturedPublish {
    char topic[128] = {};
    char payload[64] = {};
};
std::vector<CapturedPublish> g_all_publishes;

void reset_stubs() {
    g_wifi_up = false;
    g_mqtt_up = false;
    g_mqtt_publish_ok = true;
    g_all_publishes.clear();
}
}  // namespace

bool WiFiIsConnected() { return g_wifi_up; }
bool MQTTConnect(const char*, uint16_t, const char*, const char*, const char*,
                 const char*, const char*, bool) {
    return g_mqtt_up;
}
bool MQTTSubscribe(const char*) { return true; }
void MQTTSetCallback(void (*)(const char*, const uint8_t*, size_t)) {}
bool MQTTPublishBinary(const char*, const uint8_t*, size_t, bool) {
    return g_mqtt_publish_ok;
}
bool MQTTIsConnected() { return g_mqtt_up; }
bool MQTTPublish(const char* topic, const char* payload) {
    CapturedPublish msg{};
    std::strncpy(msg.topic,   topic,   sizeof(msg.topic)   - 1);
    std::strncpy(msg.payload, payload, sizeof(msg.payload) - 1);
    g_all_publishes.push_back(msg);
    return g_mqtt_publish_ok;
}
void WiFiConnect() {}
#include <chrono>
uint32_t PlatformNowMs() {
    return static_cast<uint32_t>(
        std::chrono::duration_cast<std::chrono::milliseconds>(
            std::chrono::steady_clock::now().time_since_epoch())
            .count());
}

// ── Helpers ───────────────────────────────────────────────────────────────────

namespace {

firmware::NetworkManager::Config TestConfig() {
    return {
        .ssid      = "test_ssid",
        .password  = "test_pass",
        .mqtt_host = "broker.local",
        .mqtt_port = 1883,
        .device_id = "esp32_test",
        .mqtt_user = nullptr,
        .mqtt_pass = nullptr,
    };
}

// Drive the network manager from kIdle to kReady in one step.
// Requires g_wifi_up = true and g_mqtt_up = true before calling.
void bring_up(firmware::NetworkManager& net) {
    net.Connect();
    net.Poll();  // PollConnecting: WiFi up + MQTT ok → kReady
}

}  // namespace

// ══ Layer 1: message formatting ═══════════════════════════════════════════════

TEST(MessageFormatting, TopicIsPrefix_slash_SensorName) {
    firmware::testing::FakeSensor sensor("thermistor", 0, 22.5f);
    firmware::ISensor* const sensors[] = {&sensor};
    firmware::testing::FakePublisher publisher;
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    writer.PublishAll();

    ASSERT_EQ(publisher.messages().size(), 1u);
    EXPECT_STREQ(publisher.messages()[0].topic, "home/sensors/thermistor");
}

TEST(MessageFormatting, PayloadIsFormattedFloat) {
    firmware::testing::FakeSensor sensor("thermistor", 0, 22.5f);
    firmware::ISensor* const sensors[] = {&sensor};
    firmware::testing::FakePublisher publisher;
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    writer.PublishAll();

    ASSERT_EQ(publisher.messages().size(), 1u);
    // Payload must contain the sensor reading as a formatted number.
    const char* payload = publisher.messages()[0].payload;
    float parsed = std::atof(payload);
    EXPECT_FLOAT_EQ(parsed, 22.5f);
}

TEST(MessageFormatting, MultipleSensorsAllPublished) {
    firmware::testing::FakeSensor temp("temperature", 0, 21.0f);
    firmware::testing::FakeSensor humidity("humidity",   1, 55.0f);
    firmware::ISensor* const sensors[] = {&temp, &humidity};
    firmware::testing::FakePublisher publisher;
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    int count = writer.PublishAll();

    EXPECT_EQ(count, 2);
    ASSERT_EQ(publisher.messages().size(), 2u);
    EXPECT_STREQ(publisher.messages()[0].topic, "home/sensors/temperature");
    EXPECT_STREQ(publisher.messages()[1].topic, "home/sensors/humidity");
}

TEST(MessageFormatting, PublishSkippedWhenPublisherUnavailable) {
    firmware::testing::FakeSensor sensor("thermistor", 0, 22.5f);
    firmware::ISensor* const sensors[] = {&sensor};
    firmware::testing::FakePublisher publisher(/*connected=*/false);
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    int count = writer.PublishAll();

    EXPECT_EQ(count, 0);
    EXPECT_TRUE(publisher.messages().empty());
}

TEST(MessageFormatting, SensorValueChangesReflectedInNextPublish) {
    firmware::testing::FakeSensor sensor("thermistor", 0, 20.0f);
    firmware::ISensor* const sensors[] = {&sensor};
    firmware::testing::FakePublisher publisher;
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    writer.PublishAll();
    sensor.set_value(30.0f);
    writer.PublishAll();

    ASSERT_EQ(publisher.messages().size(), 2u);
    float first  = std::atof(publisher.messages()[0].payload);
    float second = std::atof(publisher.messages()[1].payload);
    EXPECT_FLOAT_EQ(first,  20.0f);
    EXPECT_FLOAT_EQ(second, 30.0f);
}

// ══ Layer 2: full-stack integration ═══════════════════════════════════════════

TEST(FullStack, IdleNetworkPreventsPublish) {
    reset_stubs();
    auto cfg = TestConfig();
    firmware::NetworkManager net(cfg);
    firmware::NetworkPublisher publisher(net);

    firmware::testing::FakeSensor sensor("thermistor", 0, 25.0f);
    firmware::ISensor* const sensors[] = {&sensor};
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    int count = writer.PublishAll();

    EXPECT_EQ(count, 0);
    EXPECT_TRUE(g_all_publishes.empty());
}

TEST(FullStack, ConnectThenPublishReachesNetwork) {
    reset_stubs();
    g_wifi_up = true;
    g_mqtt_up = true;

    auto cfg = TestConfig();
    firmware::NetworkManager net(cfg);
    firmware::NetworkPublisher publisher(net);

    firmware::testing::FakeSensor sensor("thermistor", 0, 23.5f);
    firmware::ISensor* const sensors[] = {&sensor};
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    bring_up(net);
    ASSERT_EQ(net.state(), firmware::NetworkManager::State::kReady);

    int count = writer.PublishAll();

    ASSERT_EQ(count, 1);
    ASSERT_EQ(g_all_publishes.size(), 1u);
    EXPECT_STREQ(g_all_publishes[0].topic, "home/sensors/thermistor");
    float published_value = std::atof(g_all_publishes[0].payload);
    EXPECT_FLOAT_EQ(published_value, 23.5f);
}

TEST(FullStack, NetworkDropCausesMissedPublish) {
    reset_stubs();
    g_wifi_up = true;
    g_mqtt_up = true;

    auto cfg = TestConfig();
    firmware::NetworkManager net(cfg);
    firmware::NetworkPublisher publisher(net);

    firmware::testing::FakeSensor sensor("thermistor", 0, 25.0f);
    firmware::ISensor* const sensors[] = {&sensor};
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    bring_up(net);
    ASSERT_EQ(net.state(), firmware::NetworkManager::State::kReady);

    // Simulate link loss.
    g_wifi_up = false;
    g_mqtt_up = false;
    net.Poll();  // PollReady: WiFi gone → kBackoff

    ASSERT_EQ(net.state(), firmware::NetworkManager::State::kBackoff);

    g_all_publishes.clear();
    int count = writer.PublishAll();  // should not publish

    EXPECT_EQ(count, 0);
    EXPECT_TRUE(g_all_publishes.empty());
}

TEST(FullStack, MultipleSensorsAllPublishedWhenReady) {
    reset_stubs();
    g_wifi_up = true;
    g_mqtt_up = true;

    auto cfg = TestConfig();
    firmware::NetworkManager net(cfg);
    firmware::NetworkPublisher publisher(net);

    firmware::testing::FakeSensor temp("temperature", 0, 21.0f);
    firmware::testing::FakeSensor humidity("humidity",   1, 60.0f);
    firmware::ISensor* const sensors[] = {&temp, &humidity};
    firmware::MQTTWriter writer(sensors, "home/sensors", &publisher);
    writer.InitAll();

    bring_up(net);
    int count = writer.PublishAll();

    ASSERT_EQ(count, 2);
    ASSERT_EQ(g_all_publishes.size(), 2u);
    EXPECT_STREQ(g_all_publishes[0].topic, "home/sensors/temperature");
    EXPECT_STREQ(g_all_publishes[1].topic, "home/sensors/humidity");
    EXPECT_FLOAT_EQ(std::atof(g_all_publishes[0].payload), 21.0f);
    EXPECT_FLOAT_EQ(std::atof(g_all_publishes[1].payload), 60.0f);
}

TEST(FullStack, PublishFailureReturnsInternalError) {
    reset_stubs();
    g_wifi_up = true;
    g_mqtt_up = true;
    g_mqtt_publish_ok = false;  // broker rejects the publish

    auto cfg = TestConfig();
    firmware::NetworkManager net(cfg);

    bring_up(net);
    pw::Status s = net.Publish("home/sensors/thermistor", "22.5");

    EXPECT_EQ(s, pw::Status::Internal());
}

TEST(FullStack, PublishBeforeConnectReturnsUnavailable) {
    reset_stubs();
    auto cfg = TestConfig();
    firmware::NetworkManager net(cfg);

    pw::Status s = net.Publish("home/sensors/thermistor", "22.5");
    EXPECT_EQ(s, pw::Status::Unavailable());
}

TEST(FullStack, ConnectIsIdempotentWhenAlreadyReady) {
    reset_stubs();
    g_wifi_up = true;
    g_mqtt_up = true;

    auto cfg = TestConfig();
    firmware::NetworkManager net(cfg);
    bring_up(net);
    ASSERT_EQ(net.state(), firmware::NetworkManager::State::kReady);

    // Calling Connect() again while kReady must not reset the state.
    net.Connect();
    EXPECT_EQ(net.state(), firmware::NetworkManager::State::kReady);
}
