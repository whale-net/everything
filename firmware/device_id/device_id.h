#pragma once

namespace firmware {

// Board-agnostic unique device identifier.
//
// Implementations:
//   EfuseDeviceId  — ESP32: reads base MAC from eFuse BLOCK0
//   FakeDeviceId   — fixed string for host-side tests
//
// Get() returns a stable, non-null, null-terminated string valid for the
// lifetime of the object. It is safe to call multiple times.
class IDeviceId {
 public:
  virtual ~IDeviceId() = default;
  virtual const char* Get() const = 0;
};

// ── Test double ──────────────────────────────────────────────────────────────

namespace testing {

class FakeDeviceId final : public IDeviceId {
 public:
  explicit FakeDeviceId(const char* id) : id_(id) {}
  const char* Get() const override { return id_; }

 private:
  const char* id_;
};

}  // namespace testing
}  // namespace firmware
