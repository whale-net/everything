#include "firmware/sensor/ccs811.h"

#include "pw_log/log.h"
#include "pw_status/status.h"

namespace firmware {

// ── CCS811Device ─────────────────────────────────────────────────────────────

CCS811Device::CCS811Device(II2CBus& bus, uint8_t address, uint32_t (*clock_fn)())
    : bus_(bus), address_(address), clock_fn_(clock_fn) {}

pw::Status CCS811Device::Init() {
    // Verify hardware ID (should be 0x81).
    uint8_t hw_id = 0;
    pw::Status s = bus_.ReadRegister(address_, kRegHwId, &hw_id, 1);
    if (!s.ok()) {
        PW_LOG_ERROR("CCS811 0x%02x: hardware ID read failed", address_);
        return s;
    }
    if (hw_id != 0x81) {
        PW_LOG_ERROR("CCS811 0x%02x: unexpected HW ID 0x%02x", address_, hw_id);
        return pw::Status::NotFound();
    }

    // Check app valid before starting.
    uint8_t status = 0;
    s = bus_.ReadRegister(address_, kRegStatus, &status, 1);
    if (!s.ok() || !(status & kStatusAppValid)) {
        PW_LOG_ERROR("CCS811 0x%02x: app not valid (status=0x%02x)", address_, status);
        return pw::Status::FailedPrecondition();
    }

    // Start app — write with no data to APP_START.
    s = bus_.Write(address_, &kRegAppStart, 1);
    if (!s.ok()) {
        PW_LOG_ERROR("CCS811 0x%02x: app start failed", address_);
        return s;
    }

    // Set drive mode 1: measure every 1 second.
    s = bus_.WriteRegister(address_, kRegMeasMode, &kMeasMode1, 1);
    if (!s.ok()) {
        PW_LOG_ERROR("CCS811 0x%02x: meas mode set failed", address_);
        return s;
    }

    init_ok_ = true;
    last_poll_ms_ = clock_fn_();
    PW_LOG_INFO("CCS811 0x%02x: ready", address_);
    return pw::OkStatus();
}

void CCS811Device::Poll() {
    if (!init_ok_) return;
    uint32_t now = clock_fn_();
    if (now - last_poll_ms_ < kPollIntervalMs) return;
    last_poll_ms_ = now;

    // Check data-ready bit.
    uint8_t status = 0;
    if (!bus_.ReadRegister(address_, kRegStatus, &status, 1).ok()) return;
    if (!(status & kStatusDataReady)) return;

    // Read 4 bytes from ALG_RESULT_DATA: [eCO2_H, eCO2_L, TVOC_H, TVOC_L]
    // (followed by status, error, raw — we only need the first 4).
    uint8_t buf[4] = {};
    if (!bus_.ReadRegister(address_, kRegAlgResult, buf, 4).ok()) {
        PW_LOG_WARN("CCS811 0x%02x: result read failed", address_);
        return;
    }

    last_eco2_ppm_ = static_cast<float>((uint16_t(buf[0]) << 8) | buf[1]);
    last_tvoc_ppb_ = static_cast<float>((uint16_t(buf[2]) << 8) | buf[3]);
    valid_ = true;
}

float CCS811Device::ReadECO2() {
    Poll();
    return last_eco2_ppm_;
}

float CCS811Device::ReadTVOC() {
    Poll();
    return last_tvoc_ppb_;
}

// ── CCS811eCO2 ───────────────────────────────────────────────────────────────

CCS811eCO2::CCS811eCO2(CCS811Device& dev, const char* name)
    : dev_(dev), name_(name) {}

pw::Status CCS811eCO2::Init() {
    return dev_.Init();
}

SensorReading CCS811eCO2::Read() {
    float v = dev_.ReadECO2();
    return dev_.eco2_valid() ? SensorReading::Ok(v) : SensorReading::Invalid();
}

// ── CCS811TVOC ───────────────────────────────────────────────────────────────

CCS811TVOC::CCS811TVOC(CCS811Device& dev, const char* name)
    : dev_(dev), name_(name) {}

pw::Status CCS811TVOC::Init() {
    return pw::OkStatus();  // Init is driven by CCS811eCO2
}

SensorReading CCS811TVOC::Read() {
    float v = dev_.ReadTVOC();
    return dev_.tvoc_valid() ? SensorReading::Ok(v) : SensorReading::Invalid();
}

}  // namespace firmware
