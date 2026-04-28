// ConfigApplier — matches config entries to sensors by hardware path.

#include "firmware/config/config_applier.h"

#include <cstring>

namespace firmware {

namespace {

// Returns true if the sensor's mux path matches the SensorConfig's mux_path
// and i2c_address exactly. Path comparison is depth-first, outer to inner.
bool PathsMatch(const ISensor* s, const firmware_SensorConfig& sc) {
    if (s->address() != static_cast<uint8_t>(sc.i2c_address)) return false;
    if (s->mux_depth() != static_cast<size_t>(sc.mux_path_count)) return false;
    for (size_t i = 0; i < s->mux_depth(); ++i) {
        MuxHop hop = s->mux_hop(i);
        if (hop.address != static_cast<uint8_t>(sc.mux_path[i].mux_address))
            return false;
        if (hop.channel != static_cast<uint8_t>(sc.mux_path[i].mux_channel))
            return false;
    }
    return true;
}

}  // namespace

ConfigApplier::ConfigApplier(pw::span<ISensor* const> sensors)
    : sensors_(sensors) {
    for (size_t i = 0; i < kMaxSensors; ++i) {
        enabled_[i] = true;
        poll_ms_[i] = 0;
    }
}

void ConfigApplier::Apply(const firmware_DeviceConfig& cfg) {
    // Reset to defaults before applying.
    for (size_t i = 0; i < kMaxSensors; ++i) {
        enabled_[i] = true;
        poll_ms_[i] = 0;
    }

    for (pb_size_t c = 0; c < cfg.sensors_count; ++c) {
        const firmware_SensorConfig& sc = cfg.sensors[c];
        for (size_t i = 0; i < sensors_.size() && i < kMaxSensors; ++i) {
            if (!PathsMatch(sensors_[i], sc)) continue;
            if (sc.name[0] != '\0') sensors_[i]->SetName(sc.name);
            enabled_[i] = sc.enabled;
            poll_ms_[i] = sc.poll_interval_ms;
            break;
        }
    }

    initialized_ = true;
}

bool ConfigApplier::IsEnabled(size_t index) const {
    if (!initialized_ || index >= kMaxSensors) return true;
    return enabled_[index];
}

uint32_t ConfigApplier::PollIntervalMs(size_t index) const {
    if (!initialized_ || index >= kMaxSensors) return 0;
    return poll_ms_[index];
}

}  // namespace firmware
