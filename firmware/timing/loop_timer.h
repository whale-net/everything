#pragma once

// Non-blocking fixed-period loop timer using pw_chrono.
//
// Replaces the naive pattern:
//
//   void loop() {
//     ReadSensors();
//     PublishMQTT();
//     delay(30000);      // ← WRONG: blocks Wi-Fi keep-alive, OTA, WDT
//   }
//
// Correct pattern (what this class enables):
//
//   LoopTimer timer(pw::chrono::SystemClock::for_at_least(
//                       std::chrono::seconds(30)));
//
//   void loop() {
//     if (timer.IsReady()) {
//       ReadSensors();
//       PublishMQTT();
//       timer.Reset();
//     }
//     // Wi-Fi stack, MQTT keep-alive, and WDT feed run every loop() pass.
//     mqtt_client.loop();   // ~0ms, non-blocking
//     esp_task_wdt_reset(); // feed watchdog
//   }
//
// Why delay() kills the Wi-Fi stack on ESP32:
//   The Arduino ESP32 core runs the TCP/IP stack (lwIP) and Wi-Fi driver
//   in a separate FreeRTOS task.  delay() calls vTaskDelay(), which yields
//   the current task for the specified duration.  During that window the
//   Wi-Fi task *does* run, BUT: the PubSubClient MQTT library's keep-alive
//   is driven by client.loop() which runs in YOUR task.  If loop() is
//   blocked for 30 seconds, keep-alive pings are not sent, and the broker
//   disconnects you after its keep-alive timeout (~15–60 s depending on
//   broker config).  Additionally, the hardware WDT (if enabled) will
//   reset the chip if loop() doesn't return within its timeout window.

#include <chrono>

#include "pw_chrono/system_clock.h"

namespace firmware {

class LoopTimer {
 public:
  explicit LoopTimer(pw::chrono::SystemClock::duration period)
      : period_(period),
        next_tick_(pw::chrono::SystemClock::now() + period) {}

  // Returns true once per period.  Cheap to call every loop() pass.
  bool IsReady() const {
    return pw::chrono::SystemClock::now() >= next_tick_;
  }

  // Call after acting on IsReady() == true.
  void Reset() {
    next_tick_ = pw::chrono::SystemClock::now() + period_;
  }

  // How long until the next tick.  Useful for debug logging.
  pw::chrono::SystemClock::duration TimeUntilReady() const {
    auto now = pw::chrono::SystemClock::now();
    if (now >= next_tick_) return pw::chrono::SystemClock::duration::zero();
    return next_tick_ - now;
  }

 private:
  pw::chrono::SystemClock::duration period_;
  pw::chrono::SystemClock::time_point next_tick_;
};

}  // namespace firmware
