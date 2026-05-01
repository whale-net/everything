// ConfigApplier — matches config entries to sensors by hardware path.

#include "firmware/config/config_applier.h"

#include <cstring>

namespace firmware {

namespace {

static bool FitsInUint8(uint32_t v) { return v <= 0xFFu; }

// Returns true if the sensor matches the SensorConfig's hardware path and,
// when sensor_type is set (non-zero), also its sensor type.
// Out-of-range address/channel values never match.
bool PathsMatch(const ISensor* s, const firmware_SensorConfig& sc) {
    if (!FitsInUint8(sc.i2c_address)) return false;
    if (s->address() != static_cast<uint8_t>(sc.i2c_address)) return false;
    if (s->mux_depth() != static_cast<size_t>(sc.mux_path_count)) return false;
    for (size_t i = 0; i < s->mux_depth(); ++i) {
        if (!FitsInUint8(sc.mux_path[i].mux_address)) return false;
        if (!FitsInUint8(sc.mux_path[i].mux_channel)) return false;
        MuxHop hop = s->mux_hop(i);
        if (hop.address != static_cast<uint8_t>(sc.mux_path[i].mux_address))
            return false;
        if (hop.channel != static_cast<uint8_t>(sc.mux_path[i].mux_channel))
            return false;
    }
    // sensor_type = UNKNOWN (0) matches any type — used for single-ISensor chips.
    // Non-zero means the config entry targets a specific virtual sensor (e.g.
    // distinguishing SHT3x temperature from humidity at the same address).
    if (sc.sensor_type != firmware_SensorType_SENSOR_TYPE_UNKNOWN &&
        sc.sensor_type != s->type()) {
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
            if (sc.has_enabled) enabled_[i] = sc.enabled;
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
