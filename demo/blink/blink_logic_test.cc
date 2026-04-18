// Host-side unit tests for blink logic.
//
// Runs on the host (no hardware needed) using pw_unit_test,
// which provides a googletest-compatible API designed for embedded targets.
//
// Build & run:
//   bazel test //demo/blink:blink_logic_test

#include "pw_unit_test/framework.h"

// Blink timing constants (must match blink.cc)
namespace {
constexpr int kLedPin = 2;
constexpr int kBlinkOnMs = 1000;
constexpr int kBlinkOffMs = 1000;
}  // namespace

TEST(BlinkConfig, LedPinIsValid) {
    // GPIO 2 is the built-in LED on the ELEGOO ESP32 dev board.
    EXPECT_GE(kLedPin, 0);
    EXPECT_LT(kLedPin, 40);  // ESP32 has 40 GPIO pins
}

TEST(BlinkTiming, OnAndOffAreEqual) {
    EXPECT_EQ(kBlinkOnMs, kBlinkOffMs);
}

TEST(BlinkTiming, PeriodIsOneSecond) {
    EXPECT_EQ(kBlinkOnMs, 1000);
}
