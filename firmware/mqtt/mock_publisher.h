#pragma once

// Host-side mock for IPublisher.
//
// Captures every Publish() call so tests can assert on topic/payload pairs
// without a real MQTT broker or network stack.

#include <cstring>
#include <vector>

#include "firmware/mqtt/mqtt_writer.h"
#include "pw_status/status.h"

namespace firmware {
namespace testing {

struct PublishedMessage {
  char topic[128];
  char payload[64];
};

class FakePublisher final : public IPublisher {
 public:
  // Configure whether Publish() succeeds or simulates a disconnected broker.
  explicit FakePublisher(bool connected = true) : connected_(connected) {}

  pw::Status Publish(const char* topic, const char* payload) override {
    if (!connected_) return pw::Status::Unavailable();
    PublishedMessage msg{};
    std::strncpy(msg.topic, topic, sizeof(msg.topic) - 1);
    std::strncpy(msg.payload, payload, sizeof(msg.payload) - 1);
    messages_.push_back(msg);
    return pw::OkStatus();
  }

  const std::vector<PublishedMessage>& messages() const { return messages_; }
  void set_connected(bool c) { connected_ = c; }
  void clear() { messages_.clear(); }

 private:
  bool connected_;
  std::vector<PublishedMessage> messages_;
};

}  // namespace testing
}  // namespace firmware
