// ConfigApplier — factory + legacy sensor config application.

#include "firmware/config/config_applier.h"

#include <cstring>

#include "pw_log/log.h"

namespace firmware {

namespace {

bool FitsInUint8(uint32_t v) { return v <= 0xFFu; }

}  // namespace

// ── Constructors ──────────────────────────────────────────────────────────────

ConfigApplier::ConfigApplier(II2CBus* root_bus, uint32_t (*millis_fn)())
    : factory_mode_(true),
      root_bus_(root_bus),
      millis_fn_(millis_fn),
      active_count_(0) {
  for (size_t i = 0; i < kMaxSensors; ++i) {
    enabled_[i] = true;
    poll_ms_[i] = 0;
  }
}

ConfigApplier::ConfigApplier(pw::span<ISensor* const> sensors)
    : factory_mode_(false), legacy_sensors_(sensors) {
  for (size_t i = 0; i < kMaxSensors; ++i) {
    enabled_[i] = true;
    poll_ms_[i] = 0;
  }
}

// ── Public API ────────────────────────────────────────────────────────────────

void ConfigApplier::Apply(const firmware_DeviceConfig& cfg) {
  if (factory_mode_) {
    ApplyFactory(cfg);
  } else {
    ApplyLegacy(cfg);
  }
}

bool ConfigApplier::IsEnabled(size_t index) const {
  if (index >= kMaxSensors) return true;
  return enabled_[index];
}

uint32_t ConfigApplier::PollIntervalMs(size_t index) const {
  if (index >= kMaxSensors) return 0;
  return poll_ms_[index];
}

// ── Factory path ──────────────────────────────────────────────────────────────

// Walk mux_path, finding or creating a TCA9548ABus for each hop.
// Returns the bus the sensor should be addressed on (root for empty path).
II2CBus* ConfigApplier::FindBus(const firmware_SensorConfig& sc) {
  II2CBus* current = root_bus_;
  for (pb_size_t i = 0; i < sc.mux_path_count; ++i) {
    if (!FitsInUint8(sc.mux_path[i].mux_address)) return nullptr;
    if (!FitsInUint8(sc.mux_path[i].mux_channel)) return nullptr;
    auto addr = static_cast<uint8_t>(sc.mux_path[i].mux_address);
    auto chan  = static_cast<uint8_t>(sc.mux_path[i].mux_channel);
    MuxBusEntry* entry = mux_bus_pool_.FindIf([&](MuxBusEntry* e) {
      return e->parent == current && e->addr == addr && e->channel == chan;
    });
    if (!entry) {
      entry = mux_bus_pool_.Alloc(*current, addr, chan);
      if (!entry) {
        PW_LOG_WARN("ConfigApplier: mux bus pool exhausted");
        return nullptr;
      }
    }
    current = &entry->bus;
  }
  return current;
}

// Returns true when (i2c_address, mux_depth, mux_hops) of an already-allocated
// compound device (SHT3x/CCS811) matches a config entry.  Used to find the
// shared device instance for the second virtual sensor.
bool ConfigApplier::DevicePathsMatch(uint8_t addr, size_t mux_depth_val,
                                     const firmware_SensorConfig& sc) const {
  if (!FitsInUint8(sc.i2c_address)) return false;
  if (addr != static_cast<uint8_t>(sc.i2c_address)) return false;
  if (mux_depth_val != static_cast<size_t>(sc.mux_path_count)) return false;
  return true;  // mux_depth match is sufficient for single-hop hardware
}

void ConfigApplier::AddSensor(ISensor* s, const firmware_SensorConfig& sc) {
  if (!s || active_count_ >= kMaxSensors) return;
  active_sensors_[active_count_] = s;
  enabled_[active_count_]  = !sc.has_enabled || sc.enabled;
  poll_ms_[active_count_]  = sc.poll_interval_ms;
  ++active_count_;
}

void ConfigApplier::ApplyFactory(const firmware_DeviceConfig& cfg) {
  // Destroy all previously-allocated sensors, devices, and mux buses.
  bh1750_pool_.Reset();
  sht3x_temp_pool_.Reset();
  sht3x_humi_pool_.Reset();
  sht3x_dev_pool_.Reset();   // device after virtuals (owns shared state)
  ccs811_eco2_pool_.Reset();
  ccs811_tvoc_pool_.Reset();
  ccs811_dev_pool_.Reset();
  mux_bus_pool_.Reset();
  active_count_ = 0;

  for (size_t i = 0; i < kMaxSensors; ++i) {
    enabled_[i] = true;
    poll_ms_[i] = 0;
  }

  for (pb_size_t c = 0; c < cfg.sensors_count; ++c) {
    const firmware_SensorConfig& sc = cfg.sensors[c];

    if (sc.chip_type == firmware_ChipType_CHIP_TYPE_UNKNOWN) continue;
    if (!FitsInUint8(sc.i2c_address) || sc.i2c_address == 0) continue;

    II2CBus* bus = FindBus(sc);
    if (!bus) {
      PW_LOG_WARN("ConfigApplier: no bus endpoint for sensor at 0x%02x mux_path_count=%d",
                  sc.i2c_address, (int)sc.mux_path_count);
      continue;
    }

    const char* name = sc.name[0] != '\0' ? sc.name : nullptr;
    auto addr = static_cast<uint8_t>(sc.i2c_address);

    switch (sc.chip_type) {
      case firmware_ChipType_CHIP_TYPE_BH1750: {
        auto* s = bh1750_pool_.Alloc(*bus, addr,
                                      name ? name : "light", millis_fn_);
        if (!s) { PW_LOG_WARN("ConfigApplier: BH1750 pool exhausted"); break; }
        (void)s->Init();
        AddSensor(s, sc);
        break;
      }

      case firmware_ChipType_CHIP_TYPE_SHT3X: {
        // Find or create the shared SHT3xDevice for this address+path.
        size_t depth = static_cast<size_t>(sc.mux_path_count);
        SHT3xDevice* dev = sht3x_dev_pool_.FindIf([&](SHT3xDevice* d) {
          return DevicePathsMatch(d->address(), d->mux_depth(), sc);
        });
        if (!dev) {
          dev = sht3x_dev_pool_.Alloc(*bus, addr, millis_fn_);
          if (!dev) { PW_LOG_WARN("ConfigApplier: SHT3x device pool exhausted"); break; }
          (void)dev->Init();
          (void)depth;
        }
        if (sc.sensor_type == firmware_SensorType_SENSOR_TYPE_HUMIDITY) {
          auto* s = sht3x_humi_pool_.Alloc(*dev, name ? name : "humidity");
          if (!s) { PW_LOG_WARN("ConfigApplier: SHT3x humidity pool exhausted"); break; }
          AddSensor(s, sc);
        } else {
          // TEMPERATURE or UNKNOWN → temperature virtual sensor
          auto* s = sht3x_temp_pool_.Alloc(*dev, name ? name : "temp");
          if (!s) { PW_LOG_WARN("ConfigApplier: SHT3x temperature pool exhausted"); break; }
          AddSensor(s, sc);
        }
        break;
      }

      case firmware_ChipType_CHIP_TYPE_CCS811: {
        size_t depth = static_cast<size_t>(sc.mux_path_count);
        CCS811Device* dev = ccs811_dev_pool_.FindIf([&](CCS811Device* d) {
          return DevicePathsMatch(d->address(), d->mux_depth(), sc);
        });
        if (!dev) {
          dev = ccs811_dev_pool_.Alloc(*bus, addr, millis_fn_);
          if (!dev) { PW_LOG_WARN("ConfigApplier: CCS811 device pool exhausted"); break; }
          (void)dev->Init();
          (void)depth;
        }
        if (sc.sensor_type == firmware_SensorType_SENSOR_TYPE_TVOC) {
          auto* s = ccs811_tvoc_pool_.Alloc(*dev, name ? name : "tvoc");
          if (!s) { PW_LOG_WARN("ConfigApplier: CCS811 TVOC pool exhausted"); break; }
          AddSensor(s, sc);
        } else {
          // ECO2 or UNKNOWN → eCO2 virtual sensor
          auto* s = ccs811_eco2_pool_.Alloc(*dev, name ? name : "eco2");
          if (!s) { PW_LOG_WARN("ConfigApplier: CCS811 eCO2 pool exhausted"); break; }
          AddSensor(s, sc);
        }
        break;
      }

      case firmware_ChipType_CHIP_TYPE_UNKNOWN:
        break;  // patch-only mode — no sensor instantiated
    }
  }

  PW_LOG_INFO("ConfigApplier: factory apply complete, %zu sensors active",
              active_count_);
}

// ── Legacy path ───────────────────────────────────────────────────────────────

bool ConfigApplier::PathsMatch(const ISensor* s,
                               const firmware_SensorConfig& sc) const {
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
  if (sc.sensor_type != firmware_SensorType_SENSOR_TYPE_UNKNOWN &&
      sc.sensor_type != s->type()) {
    return false;
  }
  return true;
}

void ConfigApplier::ApplyLegacy(const firmware_DeviceConfig& cfg) {
  for (size_t i = 0; i < kMaxSensors; ++i) {
    enabled_[i] = true;
    poll_ms_[i] = 0;
  }
  for (pb_size_t c = 0; c < cfg.sensors_count; ++c) {
    const firmware_SensorConfig& sc = cfg.sensors[c];
    for (size_t i = 0; i < legacy_sensors_.size() && i < kMaxSensors; ++i) {
      if (!PathsMatch(legacy_sensors_[i], sc)) continue;
      if (sc.name[0] != '\0') legacy_sensors_[i]->SetName(sc.name);
      if (sc.has_enabled) enabled_[i] = sc.enabled;
      poll_ms_[i] = sc.poll_interval_ms;
      break;
    }
  }
}

}  // namespace firmware
