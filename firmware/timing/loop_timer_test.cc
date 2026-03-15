// Host-side unit tests for LoopTimer.
//
// pw_chrono's host backend uses std::chrono::steady_clock, so these
// tests run in real (wall-clock) time.  All assertions use ranges
// tolerant of scheduler jitter.
//
//   bazel test //firmware/timing:loop_timer_test

#include "firmware/timing/loop_timer.h"

#include <chrono>
#include <thread>

#include "pw_unit_test/framework.h"

namespace firmware {
namespace {

TEST(LoopTimerTest, NotReadyImmediatelyAfterConstruction) {
  LoopTimer timer(
      pw::chrono::SystemClock::for_at_least(std::chrono::milliseconds(50)));
  EXPECT_FALSE(timer.IsReady());
}

TEST(LoopTimerTest, ReadyAfterPeriodElapses) {
  LoopTimer timer(
      pw::chrono::SystemClock::for_at_least(std::chrono::milliseconds(20)));
  std::this_thread::sleep_for(std::chrono::milliseconds(30));
  EXPECT_TRUE(timer.IsReady());
}

TEST(LoopTimerTest, NotReadyAgainAfterReset) {
  LoopTimer timer(
      pw::chrono::SystemClock::for_at_least(std::chrono::milliseconds(20)));
  std::this_thread::sleep_for(std::chrono::milliseconds(30));
  ASSERT_TRUE(timer.IsReady());
  timer.Reset();
  EXPECT_FALSE(timer.IsReady());
}

TEST(LoopTimerTest, TimeUntilReadyIsZeroWhenReady) {
  LoopTimer timer(
      pw::chrono::SystemClock::for_at_least(std::chrono::milliseconds(10)));
  std::this_thread::sleep_for(std::chrono::milliseconds(20));
  EXPECT_EQ(timer.TimeUntilReady(),
            pw::chrono::SystemClock::duration::zero());
}

TEST(LoopTimerTest, TimeUntilReadyIsPositiveBeforePeriod) {
  LoopTimer timer(
      pw::chrono::SystemClock::for_at_least(std::chrono::milliseconds(100)));
  EXPECT_GT(timer.TimeUntilReady(),
            pw::chrono::SystemClock::duration::zero());
}

}  // namespace
}  // namespace firmware
