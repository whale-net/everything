// ThermistorSensor — IAdc-backed implementation.
// All ADC calls are delegated to the injected IAdc*, keeping this file
// free of Arduino dependencies and host-compilable.

#include "firmware/sensor/thermistor.h"

#include <cmath>

#include "pw_status/status.h"

namespace firmware {

pw::Status ThermistorSensor::Init() {
    pw::Status s = adc_->Init(pin_);
    if (s.ok()) {
        cfg_.adc_max = adc_->max_value();
    }
    return s;
}

SensorReading ThermistorSensor::Read() {
    int raw = adc_->Read(pin_);
    float temp = thermistor::adc_to_celsius(raw, cfg_);
    if (!std::isnan(temp)) {
        last_value_ = temp;
        valid_ = true;
    }
    return valid_ ? SensorReading::Ok(last_value_) : SensorReading::Invalid();
}

}  // namespace firmware
