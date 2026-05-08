#include "firmware/sensor/sht3x.h"

#include <cstring>

#include "pw_log/log.h"
#include "pw_status/status.h"

namespace firmware {

// ── SHT3xDevice ──────────────────────────────────────────────────────────────

SHT3xDevice::SHT3xDevice(II2CBus& bus, uint8_t address, uint32_t (*clock_fn)())
    : bus_(bus), address_(address), clock_fn_(clock_fn) {}

pw::Status SHT3xDevice::Init() {
    pw::Status s = Trigger();
    if (s.ok()) init_ok_ = true;
    return s;
}

pw::Status SHT3xDevice::Trigger() {
    uint8_t cmd[2] = {kCmdMSB, kCmdLSB};
    pw::Status s = bus_.Write(address_, cmd, 2);
    if (s.ok()) {
        trigger_ms_ = clock_fn_();
    } else {
        PW_LOG_ERROR("SHT3x 0x%02x: trigger failed", address_);
    }
    return s;
}

void SHT3xDevice::Poll() {
    if (!init_ok_) return;
    uint32_t now = clock_fn_();
    if (now - trigger_ms_ < kMeasureTimeMs) return;

    uint8_t buf[6] = {};
    if (!bus_.Read(address_, buf, 6).ok()) {
        PW_LOG_WARN("SHT3x 0x%02x: read failed", address_);
        Trigger();
        return;
    }

    // buf[0..1]: raw temp, buf[2]: temp CRC (unchecked)
    // buf[3..4]: raw humi, buf[5]: humi CRC (unchecked)
    uint16_t raw_t = (uint16_t(buf[0]) << 8) | buf[1];
    uint16_t raw_h = (uint16_t(buf[3]) << 8) | buf[4];

    last_temp_c_   = -45.0f + 175.0f * (raw_t / 65535.0f);
    last_humi_pct_ = 100.0f * (raw_h / 65535.0f);
    temp_valid_ = true;
    humi_valid_ = true;

    Trigger();
}

float SHT3xDevice::ReadTemperature() {
    Poll();
    return last_temp_c_;
}

float SHT3xDevice::ReadHumidity() {
    Poll();
    return last_humi_pct_;
}

// ── SHT3xTemperature ─────────────────────────────────────────────────────────

SHT3xTemperature::SHT3xTemperature(SHT3xDevice& dev, const char* name)
    : dev_(dev) {
    strncpy(name_buf_, name, sizeof(name_buf_) - 1);
    name_buf_[sizeof(name_buf_) - 1] = '\0';
}

bool SHT3xTemperature::SetName(const char* name) {
    strncpy(name_buf_, name, sizeof(name_buf_) - 1);
    name_buf_[sizeof(name_buf_) - 1] = '\0';
    return true;
}

pw::Status SHT3xTemperature::Init() {
    return dev_.Init();
}

SensorReading SHT3xTemperature::Read() {
    float v = dev_.ReadTemperature();
    return dev_.temp_valid() ? SensorReading::Ok(v) : SensorReading::Invalid();
}

// ── SHT3xHumidity ────────────────────────────────────────────────────────────

SHT3xHumidity::SHT3xHumidity(SHT3xDevice& dev, const char* name)
    : dev_(dev) {
    strncpy(name_buf_, name, sizeof(name_buf_) - 1);
    name_buf_[sizeof(name_buf_) - 1] = '\0';
}

bool SHT3xHumidity::SetName(const char* name) {
    strncpy(name_buf_, name, sizeof(name_buf_) - 1);
    name_buf_[sizeof(name_buf_) - 1] = '\0';
    return true;
}

pw::Status SHT3xHumidity::Init() {
    return pw::OkStatus();  // Init is driven by SHT3xTemperature
}

SensorReading SHT3xHumidity::Read() {
    float v = dev_.ReadHumidity();
    return dev_.humi_valid() ? SensorReading::Ok(v) : SensorReading::Invalid();
}

}  // namespace firmware
