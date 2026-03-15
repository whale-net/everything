// Host-side unit tests for MQTTWriter.
//
// These run on the developer's machine: no ESP32, no MQTT broker, no Wi-Fi.
//   bazel test //firmware/mqtt:mqtt_writer_test

#include "firmware/mqtt/mqtt_writer.h"
#include "firmware/mqtt/mock_publisher.h"
#include "firmware/sensor/mock_sensor.h"
#include "pw_unit_test/framework.h"

namespace firmware {
namespace {

// ── Helper: build a writer with N fake sensors ───────────────────────────────

class MQTTWriterTest : public ::testing::Test {
 protected:
  testing::FakeSensor temp_{"temperature", 0x76, 23.5f};
  testing::FakeSensor humidity_{"humidity", 0x76, 61.2f};
  testing::FakeSensor soil_{"soil_moisture", 0x48, 42.0f};

  ISensor* sensors_[3] = {&temp_, &humidity_, &soil_};
  testing::FakePublisher publisher_;

  MQTTWriter writer_{pw::span<ISensor* const>(sensors_), "home/greenhouse",
                     &publisher_};
};

// ── InitAll ───────────────────────────────────────────────────────────────────

TEST_F(MQTTWriterTest, InitAllReturnsCountOfSuccessfulSensors) {
  EXPECT_EQ(writer_.InitAll(), 3);
}

TEST_F(MQTTWriterTest, InitAllSkipsFailingSensor) {
  temp_.set_init_status(pw::Status::Unavailable());
  EXPECT_EQ(writer_.InitAll(), 2);
}

// ── PublishAll ────────────────────────────────────────────────────────────────

TEST_F(MQTTWriterTest, PublishAllSendsOneMessagePerSensor) {
  writer_.InitAll();
  EXPECT_EQ(writer_.PublishAll(), 3);
  EXPECT_EQ(publisher_.messages().size(), 3u);
}

TEST_F(MQTTWriterTest, PublishAllFormatsTopicWithPrefix) {
  writer_.InitAll();
  writer_.PublishAll();

  EXPECT_STREQ(publisher_.messages()[0].topic, "home/greenhouse/temperature");
  EXPECT_STREQ(publisher_.messages()[1].topic, "home/greenhouse/humidity");
  EXPECT_STREQ(publisher_.messages()[2].topic, "home/greenhouse/soil_moisture");
}

TEST_F(MQTTWriterTest, PublishAllFormatsPayloadAsTwoDecimalFloat) {
  writer_.InitAll();
  writer_.PublishAll();

  EXPECT_STREQ(publisher_.messages()[0].payload, "23.50");
  EXPECT_STREQ(publisher_.messages()[1].payload, "61.20");
}

TEST_F(MQTTWriterTest, PublishAllSkipsUninitialised) {
  // Never call InitAll — all sensor_ok_ flags remain false.
  int published = writer_.PublishAll();
  EXPECT_EQ(published, 0);
  EXPECT_TRUE(publisher_.messages().empty());
}

TEST_F(MQTTWriterTest, PublishAllSkipsFailedInitSensor) {
  temp_.set_init_status(pw::Status::Unavailable());
  writer_.InitAll();
  writer_.PublishAll();

  // Only humidity and soil_moisture should appear.
  EXPECT_EQ(publisher_.messages().size(), 2u);
  EXPECT_STREQ(publisher_.messages()[0].topic, "home/greenhouse/humidity");
}

TEST_F(MQTTWriterTest, PublishAllHandlesDisconnectedPublisher) {
  publisher_.set_connected(false);
  writer_.InitAll();
  int published = writer_.PublishAll();
  EXPECT_EQ(published, 0);
}

TEST_F(MQTTWriterTest, PublishAllReflectsUpdatedSensorValues) {
  writer_.InitAll();
  temp_.set_value(99.9f);
  writer_.PublishAll();

  EXPECT_STREQ(publisher_.messages()[0].payload, "99.90");
}

}  // namespace
}  // namespace firmware
