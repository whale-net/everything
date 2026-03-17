#pragma once

// NetworkPublisher — IPublisher adapter for NetworkManager.
//
// Bridges MQTTWriter (which publishes through IPublisher) with NetworkManager
// (which exposes Publish()). Only succeeds when NetworkManager is in kReady.
//
// Usage in firmware sketch:
//
//   NetworkManager         net(cfg);
//   NetworkPublisher       publisher(net);
//   ISensor*               sensors[] = { &thermistor };
//   MQTTWriter             writer(sensors, "home/sensors", &publisher);
//
//   void setup() { net.Connect(); writer.InitAll(); }
//   void loop()  { net.Poll(); writer.PublishAll(); }

#include "firmware/mqtt/mqtt_writer.h"
#include "firmware/network/network_manager.h"
#include "pw_status/status.h"

namespace firmware {

class NetworkPublisher final : public IPublisher {
 public:
  explicit NetworkPublisher(NetworkManager& net) : net_(net) {}

  pw::Status Publish(const char* topic, const char* payload) override {
      return net_.Publish(topic, payload);
  }

 private:
  NetworkManager& net_;
};

}  // namespace firmware
