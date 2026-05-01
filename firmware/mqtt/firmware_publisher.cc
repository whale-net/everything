// FirmwarePublisher — proto serialisation + MQTT publish for firmware.

#include "firmware/mqtt/firmware_publisher.h"

#include <cinttypes>
#include <cstdio>
#include <cstring>

#include <Arduino.h>

#include "firmware/proto/config.pb.h"
#include "firmware/proto/firmware.pb.h"
#include "firmware/sensor/sensor.h"
#include "pb_decode.h"
#include "pb_encode.h"
#include "pw_log/log.h"
#include "pw_string/string_builder.h"

namespace firmware {

namespace {
constexpr size_t kTopicBufSize = 80;
}

FirmwarePublisher* FirmwarePublisher::instance_ = nullptr;

FirmwarePublisher::FirmwarePublisher(const IDeviceId& device_id,
                                     pw::span<ISensor* const> sensors,
                                     NetworkManager& net,
                                     ConfigStore& config_store,
                                     ConfigApplier& config_applier)
    : device_id_(device_id),
      sensors_(sensors),
      net_(net),
      config_store_(config_store),
      config_applier_(config_applier) {
    instance_ = this;
    net_.SetMessageCallback(&FirmwarePublisher::OnMQTTMessage);
}

void FirmwarePublisher::OnConnect() {
    // Subscribe to config push topic before announcing presence.
    pw::StringBuffer<kTopicBufSize> cfg_topic;
    cfg_topic << "leaflab/" << device_id_.Get() << "/config";
    if (!net_.Subscribe(cfg_topic.c_str()).ok()) {
        PW_LOG_WARN("FirmwarePublisher: failed to subscribe to config topic");
    }
    PublishStatus("online");
    PublishManifest();
}

void FirmwarePublisher::PublishReadings() {
    for (size_t i = 0; i < sensors_.size(); ++i) {
        if (!config_applier_.IsEnabled(i)) continue;

        ISensor* s = sensors_[i];
        SensorReading r = s->Read();
        if (!r.valid) continue;

        firmware_SensorReading msg = firmware_SensorReading_init_zero;
        msg.value     = r.value;
        msg.uptime_ms = static_cast<uint32_t>(millis());

        uint8_t buf[firmware_SensorReading_size];
        pb_ostream_t stream = pb_ostream_from_buffer(buf, sizeof(buf));
        if (!pb_encode(&stream, firmware_SensorReading_fields, &msg)) {
            PW_LOG_WARN("FirmwarePublisher: encode failed for '%s'", s->name());
            continue;
        }

        pw::StringBuffer<kTopicBufSize> topic;
        topic << "leaflab/" << device_id_.Get() << "/sensor/" << s->name();

        if (!net_.Publish(topic.c_str(), buf, stream.bytes_written).ok()) {
            PW_LOG_WARN("FirmwarePublisher: publish failed for '%s'", s->name());
        }
    }
}

void FirmwarePublisher::PublishManifest() {
    firmware_DeviceManifest manifest = firmware_DeviceManifest_init_zero;
    strncpy(manifest.device_id, device_id_.Get(), sizeof(manifest.device_id) - 1);

    pb_size_t n = 0;
    for (size_t i = 0; i < sensors_.size(); ++i) {
        if (n >= static_cast<pb_size_t>(sizeof(manifest.sensors) /
                                        sizeof(manifest.sensors[0]))) break;
        ISensor* s = sensors_[i];
        firmware_SensorDescriptor& desc = manifest.sensors[n++];
        strncpy(desc.name, s->name(), sizeof(desc.name) - 1);
        desc.type        = s->type();
        strncpy(desc.unit, UnitString(s->unit()), sizeof(desc.unit) - 1);
        desc.i2c_address = s->address();
        desc.mux_address = s->mux_address();
        desc.mux_channel = s->mux_channel();
        strncpy(desc.chip_model, s->chip_model(), sizeof(desc.chip_model) - 1);
    }
    manifest.sensors_count = n;

    uint8_t buf[firmware_DeviceManifest_size];
    pb_ostream_t stream = pb_ostream_from_buffer(buf, sizeof(buf));
    if (!pb_encode(&stream, firmware_DeviceManifest_fields, &manifest)) {
        PW_LOG_ERROR("FirmwarePublisher: manifest encode failed");
        return;
    }

    pw::StringBuffer<kTopicBufSize> topic;
    topic << "leaflab/" << device_id_.Get() << "/manifest";

    pw::Status st = net_.Publish(topic.c_str(), buf, stream.bytes_written,
                                  /*retained=*/true);
    if (st.ok()) {
        PW_LOG_INFO("FirmwarePublisher: manifest published (%zu bytes, %u sensors)",
                    stream.bytes_written, n);
    } else {
        PW_LOG_WARN("FirmwarePublisher: manifest publish failed");
    }
}

void FirmwarePublisher::PublishStatus(const char* status) {
    pw::StringBuffer<kTopicBufSize> topic;
    topic << "leaflab/" << device_id_.Get() << "/status";
    net_.Publish(topic.c_str(), status);
}

void FirmwarePublisher::OnMQTTMessage(const char* /*topic*/,
                                       const uint8_t* payload,
                                       size_t length) {
    // We only subscribe to one topic; topic discrimination not needed yet.
    if (instance_) instance_->HandleConfigMessage(payload, length);
}

void FirmwarePublisher::HandleConfigMessage(const uint8_t* payload,
                                             size_t length) {
    firmware_DeviceConfig cfg = firmware_DeviceConfig_init_zero;
    pb_istream_t stream = pb_istream_from_buffer(payload, length);
    if (!pb_decode(&stream, firmware_DeviceConfig_fields, &cfg)) {
        PW_LOG_WARN("FirmwarePublisher: DeviceConfig decode failed");
        return;
    }

    if (strncmp(cfg.device_id, device_id_.Get(),
                sizeof(cfg.device_id)) != 0) {
        PW_LOG_WARN("FirmwarePublisher: config device_id mismatch, ignoring");
        PublishConfigAck(cfg.version, false, "device_id_mismatch");
        return;
    }

    if (cfg.version <= config_store_.current_version()) {
        PW_LOG_WARN("FirmwarePublisher: stale config v%" PRIu64
                    " <= current v%" PRIu64 ", rejecting",
                    cfg.version, config_store_.current_version());
        PublishConfigAck(cfg.version, false, "stale_version");
        return;
    }

    // Reject configs that would cause MQTT topic collisions.
    for (pb_size_t i = 0; i < cfg.sensors_count; ++i) {
        if (cfg.sensors[i].name[0] == '\0') continue;
        for (pb_size_t j = i + 1; j < cfg.sensors_count; ++j) {
            if (strncmp(cfg.sensors[i].name, cfg.sensors[j].name,
                        sizeof(cfg.sensors[i].name)) == 0) {
                PW_LOG_WARN("FirmwarePublisher: duplicate sensor name '%s'",
                            cfg.sensors[i].name);
                PublishConfigAck(cfg.version, false, "duplicate_sensor_name");
                return;
            }
        }
    }

    config_applier_.Apply(cfg);

    if (!config_store_.Save(cfg).ok()) {
        PW_LOG_ERROR("FirmwarePublisher: failed to persist config to NVS");
        // Apply succeeded — continue anyway.
    }

    PublishManifest();
    PublishConfigAck(cfg.version, true, "");
    PW_LOG_INFO("FirmwarePublisher: config v%" PRIu64 " applied", cfg.version);
}

void FirmwarePublisher::PublishConfigAck(uint64_t version, bool accepted,
                                          const char* reason) {
    firmware_DeviceConfigAck ack = firmware_DeviceConfigAck_init_zero;
    strncpy(ack.device_id, device_id_.Get(), sizeof(ack.device_id) - 1);
    ack.applied_version = version;
    ack.accepted        = accepted;
    strncpy(ack.reason, reason, sizeof(ack.reason) - 1);

    uint8_t buf[firmware_DeviceConfigAck_size];
    pb_ostream_t stream = pb_ostream_from_buffer(buf, sizeof(buf));
    if (!pb_encode(&stream, firmware_DeviceConfigAck_fields, &ack)) {
        PW_LOG_ERROR("FirmwarePublisher: ack encode failed");
        return;
    }

    pw::StringBuffer<kTopicBufSize> topic;
    topic << "leaflab/" << device_id_.Get() << "/config/ack";
    net_.Publish(topic.c_str(), buf, stream.bytes_written);
}

}  // namespace firmware
