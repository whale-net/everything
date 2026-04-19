// LeafLabPublisher — proto serialisation + MQTT publish for LeafLab firmware.

#include "firmware/mqtt/leaflab_publisher.h"

#include <cstdio>
#include <cstring>

#include <Arduino.h>

#include "firmware/proto/firmware.pb.h"
#include "pb_encode.h"
#include "pw_log/log.h"
#include "pw_string/string_builder.h"

namespace firmware {

namespace {
constexpr size_t kTopicBufSize = 80;
}

LeafLabPublisher::LeafLabPublisher(const IDeviceId& device_id,
                                   pw::span<ISensor* const> sensors,
                                   NetworkManager& net)
    : device_id_(device_id), sensors_(sensors), net_(net) {}

void LeafLabPublisher::OnConnect() {
  PublishStatus("online");
  PublishManifest();
}

void LeafLabPublisher::PublishReadings() {
  for (ISensor* s : sensors_) {
    SensorReading r = s->Read();
    if (!r.valid) continue;

    firmware_SensorReading msg = firmware_SensorReading_init_zero;
    msg.value     = r.value;
    msg.uptime_ms = static_cast<uint32_t>(millis());

    uint8_t buf[firmware_SensorReading_size];
    pb_ostream_t stream = pb_ostream_from_buffer(buf, sizeof(buf));
    if (!pb_encode(&stream, firmware_SensorReading_fields, &msg)) {
      PW_LOG_WARN("LeafLabPublisher: encode failed for '%s'", s->name());
      continue;
    }

    pw::StringBuffer<kTopicBufSize> topic;
    topic << "leaflab/" << device_id_.Get() << "/sensor/" << s->name();

    pw::Status st = net_.Publish(topic.c_str(), buf, stream.bytes_written);
    if (!st.ok()) {
      PW_LOG_WARN("LeafLabPublisher: publish failed for '%s'", s->name());
    }
  }
}

void LeafLabPublisher::PublishManifest() {
  firmware_DeviceManifest manifest = firmware_DeviceManifest_init_zero;

  strncpy(manifest.device_id, device_id_.Get(), sizeof(manifest.device_id) - 1);

  pb_size_t n = 0;
  for (ISensor* s : sensors_) {
    if (n >= static_cast<pb_size_t>(sizeof(manifest.sensors) /
                                    sizeof(manifest.sensors[0]))) break;
    firmware_SensorDescriptor& desc = manifest.sensors[n++];
    strncpy(desc.name, s->name(), sizeof(desc.name) - 1);
    desc.type = s->type();
    strncpy(desc.unit, s->unit(), sizeof(desc.unit) - 1);
  }
  manifest.sensors_count = n;

  uint8_t buf[firmware_DeviceManifest_size];
  pb_ostream_t stream = pb_ostream_from_buffer(buf, sizeof(buf));
  if (!pb_encode(&stream, firmware_DeviceManifest_fields, &manifest)) {
    PW_LOG_ERROR("LeafLabPublisher: manifest encode failed");
    return;
  }

  pw::StringBuffer<kTopicBufSize> topic;
  topic << "leaflab/" << device_id_.Get() << "/manifest";

  pw::Status st = net_.Publish(topic.c_str(), buf, stream.bytes_written,
                                /*retained=*/true);
  if (st.ok()) {
    PW_LOG_INFO("LeafLabPublisher: manifest published (%zu bytes, %u sensors)",
                stream.bytes_written, n);
  } else {
    PW_LOG_WARN("LeafLabPublisher: manifest publish failed");
  }
}

void LeafLabPublisher::PublishStatus(const char* status) {
  pw::StringBuffer<kTopicBufSize> topic;
  topic << "leaflab/" << device_id_.Get() << "/status";
  net_.Publish(topic.c_str(), status);
}

}  // namespace firmware
