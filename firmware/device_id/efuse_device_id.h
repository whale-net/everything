#pragma once

// EfuseDeviceId — derives a unique device ID from the ESP32 eFuse base MAC.
//
// The eFuse base MAC (BLOCK0) is factory-burned and cannot be changed.
// All other MACs (WiFi STA, WiFi AP, BT) are derived from it, so this is
// the most fundamental unique identifier on the chip.
//
// Format: "<prefix>-<12 hex chars>"  e.g. "leaflab-a4cf12ab34cd"
// If prefix is empty or nullptr, format is just "<12 hex chars>".
//
// Get() is idempotent and safe to call before WiFi is initialised.

#include "firmware/device_id/device_id.h"

namespace firmware {

class EfuseDeviceId final : public IDeviceId {
 public:
  explicit EfuseDeviceId(const char* prefix = nullptr) : prefix_(prefix) {}

  // Reads eFuse MAC on first call and caches the result.
  const char* Get() const override;

 private:
  const char* prefix_;
  mutable char id_[32] = {};
  mutable bool loaded_  = false;
};

}  // namespace firmware
