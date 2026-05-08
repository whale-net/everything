#pragma once

// ConfigStore — persists a DeviceConfig proto to/from NVS.
//
// NVS namespace: "config"  key: "device_cfg"  (raw nanopb bytes)
// Exactly mirrors the NVSCredentials pattern in firmware/credentials/.
//
// NVS stores binary nanopb encoding (compact, fast on constrained MCU).
// The server side converts to JSON before storing in the DB.

#include "firmware/proto/config.pb.h"
#include "pw_status/status.h"

namespace firmware {

class ConfigStore {
 public:
  // Load DeviceConfig from NVS into *out.
  // Returns NotFound() if no config has been stored yet.
  // On success, version_ is set from the loaded proto.
  pw::Status Load(firmware_DeviceConfig* out);

  // Persist cfg to NVS, overwriting any previous config.
  pw::Status Save(const firmware_DeviceConfig& cfg);

  // Version of the last successfully loaded or saved config.
  // Returns 0 if neither Load nor Save has succeeded this boot.
  uint64_t current_version() const { return version_; }

 private:
  uint64_t version_ = 0;
};

}  // namespace firmware
