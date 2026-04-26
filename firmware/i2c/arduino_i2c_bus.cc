// ArduinoI2CBus — Wire-based I2C implementation.
// This file calls Arduino Wire APIs; only compiled for device targets.

#include "firmware/i2c/arduino_i2c_bus.h"

#include <Wire.h>
#include <cstdint>

#include "pw_status/status.h"

namespace firmware {

namespace {

// Wire.endTransmission() return codes:
//   0 — success
//   1 — data too long for transmit buffer
//   2 — NACK received on address byte
//   3 — NACK received on data byte
//   4 — other error
//   5 — timeout
pw::Status WireStatusToStatus(uint8_t wire_result) {
    switch (wire_result) {
        case 0: return pw::OkStatus();
        case 1: return pw::Status::ResourceExhausted();  // buffer full
        case 2: return pw::Status::NotFound();           // NACK on address
        case 3: return pw::Status::DataLoss();           // NACK on data
        case 5: return pw::Status::DeadlineExceeded();   // timeout
        default: return pw::Status::Unknown();
    }
}

}  // namespace

pw::Status ArduinoI2CBus::Init(uint8_t sda_pin, uint8_t scl_pin) {
    sda_pin_ = sda_pin;
    scl_pin_ = scl_pin;
    Wire.begin(static_cast<int>(sda_pin), static_cast<int>(scl_pin));
    return pw::OkStatus();
}

void ArduinoI2CBus::Recover() {
    Wire.end();
    Wire.begin(static_cast<int>(sda_pin_), static_cast<int>(scl_pin_));
}

pw::Status ArduinoI2CBus::Write(uint8_t address,
                                const uint8_t* data,
                                size_t len) {
    auto attempt = [&]() -> pw::Status {
        Wire.beginTransmission(address);
        Wire.write(data, len);
        return WireStatusToStatus(Wire.endTransmission(/*sendStop=*/true));
    };
    pw::Status s = attempt();
    if (!s.ok()) {
        Recover();
        s = attempt();
    }
    return s;
}

pw::Status ArduinoI2CBus::Read(uint8_t address, uint8_t* buf, size_t len) {
    auto attempt = [&]() -> pw::Status {
        size_t received = Wire.requestFrom(address, static_cast<size_t>(len),
                                           /*sendStop=*/true);
        if (received != len) {
            return pw::Status::Unavailable();
        }
        for (size_t i = 0; i < len; i++) {
            int b = Wire.read();
            if (b < 0) {
                return pw::Status::DataLoss();
            }
            buf[i] = static_cast<uint8_t>(b);
        }
        return pw::OkStatus();
    };
    pw::Status s = attempt();
    if (!s.ok()) {
        Recover();
        s = attempt();
    }
    return s;
}

pw::Status ArduinoI2CBus::ReadRegister(uint8_t address,
                                       uint8_t reg,
                                       uint8_t* buf,
                                       size_t len) {
    // Write the register address with sendStop=false to issue a repeated start.
    // The bus is held between the address write and the data read, as required
    // by most I2C sensors.
    Wire.beginTransmission(address);
    Wire.write(reg);
    pw::Status s =
        WireStatusToStatus(Wire.endTransmission(/*sendStop=*/false));
    if (!s.ok()) {
        Recover();
        Wire.beginTransmission(address);
        Wire.write(reg);
        s = WireStatusToStatus(Wire.endTransmission(/*sendStop=*/false));
        if (!s.ok()) return s;
    }
    return Read(address, buf, len);
}

pw::Status ArduinoI2CBus::WriteRegister(uint8_t address,
                                        uint8_t reg,
                                        const uint8_t* data,
                                        size_t len) {
    auto attempt = [&]() -> pw::Status {
        Wire.beginTransmission(address);
        Wire.write(reg);
        Wire.write(data, len);
        return WireStatusToStatus(Wire.endTransmission(/*sendStop=*/true));
    };
    pw::Status s = attempt();
    if (!s.ok()) {
        Recover();
        s = attempt();
    }
    return s;
}

}  // namespace firmware
