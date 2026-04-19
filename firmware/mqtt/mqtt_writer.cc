#include "firmware/mqtt/mqtt_writer.h"

#include "firmware/sensor/sensor.h"
#include "pw_assert/check.h"
#include "pw_log/log.h"
#include "pw_string/string_builder.h"

namespace firmware {

namespace {
// Stack-allocated buffers — no heap, no fragmentation.
constexpr size_t kTopicBufSize = 128;
constexpr size_t kPayloadBufSize = 32;
}  // namespace

int MQTTWriter::InitAll() {
  PW_DCHECK(sensors_.size() <= MQTTWriter::kMaxSensors,
            "Too many sensors: %zu (max %zu)",
            sensors_.size(), MQTTWriter::kMaxSensors);
  int ok_count = 0;
  for (size_t i = 0; i < sensors_.size() && i < kMaxSensors; ++i) {
    pw::Status s = sensors_[i]->Init();
    sensor_ok_[i] = s.ok();
    if (s.ok()) {
      PW_LOG_INFO("MQTTWriter: sensor '%s' @ 0x%02x initialised",
                  sensors_[i]->name(), sensors_[i]->address());
      ok_count++;
    } else {
      PW_LOG_WARN("MQTTWriter: sensor '%s' @ 0x%02x init failed: %s",
                  sensors_[i]->name(), sensors_[i]->address(),
                  pw_StatusString(s));
    }
  }
  return ok_count;
}

int MQTTWriter::PublishAll() {
  int published = 0;
  for (size_t i = 0; i < sensors_.size() && i < kMaxSensors; ++i) {
    if (!sensor_ok_[i]) continue;

    SensorReading reading = sensors_[i]->Read();
    if (!reading.valid) {
      PW_LOG_WARN("MQTTWriter: '%s' has no valid reading, skipping",
                  sensors_[i]->name());
      continue;
    }

    // Format topic: "<prefix>/<sensor_name>"
    pw::StringBuffer<kTopicBufSize> topic;
    topic << topic_prefix_ << "/" << sensors_[i]->name();

    // Format payload: plain float, 2 decimal places, zero heap allocation.
    pw::StringBuffer<kPayloadBufSize> payload;
    payload.Format("%.2f", static_cast<double>(reading.value));

    pw::Status s = publisher_->Publish(topic.c_str(), payload.c_str());
    if (s.ok()) {
      published++;
    } else {
      PW_LOG_WARN("MQTTWriter: publish failed for '%s': %s",
                  sensors_[i]->name(), pw_StatusString(s));
    }
  }
  return published;
}

}  // namespace firmware
