#pragma once

// ConfigApplier — two operating modes:
//
// Factory (dynamic) mode: constructed with a root bus + millis function.
// Apply() destroys previously-created sensor/mux instances, then allocates
// new ones from chip_type and mux_path in each SensorConfig entry.  Mux bus
// instances are created on demand — no compile-time mux topology needed.
// sensors() returns the live list built by the last Apply().
//
// Legacy (wrapper) mode: constructed with an existing compile-time sensor span.
// Apply() only patches names/enabled/poll on the pre-existing sensors.
// sensors() returns the original span unchanged.
//
// Host-compilable: no Arduino dependency.

#include <cstddef>
#include <cstdint>
#include <new>

#include "firmware/i2c/i2c_bus.h"
#include "firmware/i2c/tca9548a_bus.h"
#include "firmware/proto/config.pb.h"
#include "firmware/sensor/bh1750.h"
#include "firmware/sensor/ccs811.h"
#include "firmware/sensor/sensor.h"
#include "firmware/sensor/sht3x.h"
#include "pw_span/span.h"

namespace firmware {

class ConfigApplier {
 public:
  // ── Factory (dynamic) constructor ─────────────────────────────────────────
  // root_bus:  root I2C bus for sensors with empty mux_path.
  // millis_fn: clock injected into sensor constructors.
  // TCA9548ABus instances are allocated from an internal pool as needed when
  // Apply() processes SensorConfig entries with non-empty mux_path.
  ConfigApplier(II2CBus* root_bus, uint32_t (*millis_fn)());

  // ── Legacy (wrapper) constructor ──────────────────────────────────────────
  explicit ConfigApplier(pw::span<ISensor* const> sensors);

  // Apply a DeviceConfig.
  // Factory path: reset sensor pools, instantiate from chip_type.
  // Legacy path:  patch name/enabled/poll on existing sensors.
  void Apply(const firmware_DeviceConfig& cfg);

  // Current sensor list.  Factory: built by Apply().  Legacy: original span.
  pw::span<ISensor* const> sensors() const {
    if (factory_mode_) {
      return pw::span<ISensor* const>(
          const_cast<ISensor**>(active_sensors_), active_count_);
    }
    return legacy_sensors_;
  }

  bool     IsEnabled(size_t index) const;
  uint32_t PollIntervalMs(size_t index) const;

 private:
  static constexpr size_t kMaxSensors  = 16;
  static constexpr size_t kMaxChips    = 8;
  static constexpr size_t kMaxMuxBuses = 16;  // supports up to 2 cascaded 8-ch muxes

  // ── Placement-new pool ────────────────────────────────────────────────────
  template <typename T, size_t N>
  struct Pool {
    alignas(T) unsigned char storage[N][sizeof(T)];
    size_t count = 0;

    template <typename... Args>
    T* Alloc(Args&&... args) {
      if (count >= N) return nullptr;
      T* p = reinterpret_cast<T*>(storage[count++]);
      new (p) T(static_cast<Args&&>(args)...);
      return p;
    }

    void Reset() {
      for (size_t i = 0; i < count; ++i)
        reinterpret_cast<T*>(storage[i])->~T();
      count = 0;
    }

    template <typename Pred>
    T* FindIf(Pred pred) {
      for (size_t i = 0; i < count; ++i) {
        T* p = reinterpret_cast<T*>(storage[i]);
        if (pred(p)) return p;
      }
      return nullptr;
    }
  };

  // ── Shared state ──────────────────────────────────────────────────────────
  bool     factory_mode_ = false;
  bool     enabled_[kMaxSensors];
  uint32_t poll_ms_[kMaxSensors];

  // ── Legacy mode ───────────────────────────────────────────────────────────
  pw::span<ISensor* const> legacy_sensors_;

  // ── Factory mode ──────────────────────────────────────────────────────────
  // MuxBusEntry wraps a TCA9548ABus with its identity so FindBus can deduplicate.
  struct MuxBusEntry {
    II2CBus*    parent;
    uint8_t     addr;
    uint8_t     channel;
    TCA9548ABus bus;
    MuxBusEntry(II2CBus& p, uint8_t a, uint8_t c) : parent(&p), addr(a), channel(c), bus(p, a, c) {}
  };

  II2CBus*  root_bus_   = nullptr;
  uint32_t (*millis_fn_)() = nullptr;

  ISensor* active_sensors_[kMaxSensors];
  size_t   active_count_ = 0;

  Pool<MuxBusEntry,      kMaxMuxBuses> mux_bus_pool_;
  Pool<BH1750Sensor,     kMaxChips>    bh1750_pool_;
  Pool<SHT3xDevice,      kMaxChips>    sht3x_dev_pool_;
  Pool<SHT3xTemperature, kMaxChips>    sht3x_temp_pool_;
  Pool<SHT3xHumidity,    kMaxChips>    sht3x_humi_pool_;
  Pool<CCS811Device,     kMaxChips>    ccs811_dev_pool_;
  Pool<CCS811eCO2,       kMaxChips>    ccs811_eco2_pool_;
  Pool<CCS811TVOC,       kMaxChips>    ccs811_tvoc_pool_;

  // ── Helpers ───────────────────────────────────────────────────────────────
  II2CBus* FindBus(const firmware_SensorConfig& sc);  // may alloc mux bus entries
  bool     PathsMatch(const ISensor* s, const firmware_SensorConfig& sc) const;
  bool     DevicePathsMatch(uint8_t addr, size_t mux_depth_val,
                            const firmware_SensorConfig& sc) const;
  void     AddSensor(ISensor* s, const firmware_SensorConfig& sc);
  void     ApplyFactory(const firmware_DeviceConfig& cfg);
  void     ApplyLegacy(const firmware_DeviceConfig& cfg);
};

}  // namespace firmware
