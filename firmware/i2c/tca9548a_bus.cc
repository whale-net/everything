#include "firmware/i2c/tca9548a_bus.h"

#include <cstdint>

#include "pw_status/status.h"

namespace firmware {

TCA9548ABus::TCA9548ABus(II2CBus& parent, uint8_t mux_address, uint8_t channel)
    : parent_(parent),
      mux_address_(mux_address),
      channel_(channel),
      channel_mask_(static_cast<uint8_t>(1u << channel)) {}

pw::Status TCA9548ABus::Init(uint8_t /*sda_pin*/, uint8_t /*scl_pin*/) {
    return pw::OkStatus();
}

pw::Status TCA9548ABus::SelectChannel() {
    return parent_.Write(mux_address_, &channel_mask_, 1);
}

pw::Status TCA9548ABus::Write(uint8_t address,
                              const uint8_t* data,
                              size_t len) {
    pw::Status s = SelectChannel();
    if (!s.ok()) return s;
    return parent_.Write(address, data, len);
}

pw::Status TCA9548ABus::Read(uint8_t address, uint8_t* buf, size_t len) {
    pw::Status s = SelectChannel();
    if (!s.ok()) return s;
    return parent_.Read(address, buf, len);
}

pw::Status TCA9548ABus::ReadRegister(uint8_t address,
                                     uint8_t reg,
                                     uint8_t* buf,
                                     size_t len) {
    pw::Status s = SelectChannel();
    if (!s.ok()) return s;
    return parent_.ReadRegister(address, reg, buf, len);
}

pw::Status TCA9548ABus::WriteRegister(uint8_t address,
                                      uint8_t reg,
                                      const uint8_t* data,
                                      size_t len) {
    pw::Status s = SelectChannel();
    if (!s.ok()) return s;
    return parent_.WriteRegister(address, reg, data, len);
}

}  // namespace firmware
