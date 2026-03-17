#pragma once

// PinRole — canonical enum of every logical pin role in the firmware system.
//
// This is the single source of truth for pin names. It must stay in sync with:
//   - REQUIRED_PINS / OPTIONAL_PINS in tools/firmware/pins.bzl
//   - board_pins() calls in each board's BUILD.bazel
//
// When adding a new peripheral pin:
//   1. Add a value to PinRole here (before kCount).
//   2. Add the name to OPTIONAL_PINS (or REQUIRED_PINS) in tools/firmware/pins.bzl.
//   3. Add the GPIO number to every board's board_pins() call that supports it;
//      boards that don't have the peripheral declare it as -1 automatically.
//
// Board-specific GPIO numbers come from the generated board_pins.h:
//   #include "board_pins.h"
//   int gpio = board::kSda;          // constexpr int for this board
//   bool ok  = BOARD_PIN_SDA_SUPPORTED;  // 1 if wired, 0 if not

#include <cstdint>

namespace firmware {

enum class PinRole : uint8_t {
    // ── Required (every board must declare) ──────────────────────────────────
    kLed = 0,   // onboard indicator LED

    // ── Optional (boards declare what they physically support) ────────────────
    kSda,       // I2C data
    kScl,       // I2C clock
    kRx2,       // UART2 RX
    kTx2,       // UART2 TX
    kAdc0,      // primary analog input (e.g. thermistor voltage divider)

    // ── Sentinel — always last ────────────────────────────────────────────────
    kCount,
};

// GPIO value used by board_pins() for a pin that is not wired on a board.
inline constexpr int kGpioInvalid = -1;

// Human-readable name for a PinRole (useful in log messages).
// Returns "unknown" for values outside the enum range.
inline const char* PinRoleName(PinRole role) {
    switch (role) {
        case PinRole::kLed:   return "Led";
        case PinRole::kSda:   return "Sda";
        case PinRole::kScl:   return "Scl";
        case PinRole::kRx2:   return "Rx2";
        case PinRole::kTx2:   return "Tx2";
        case PinRole::kAdc0:  return "Adc0";
        default:              return "unknown";
    }
}

}  // namespace firmware
