# Firmware — TOC

Board-agnostic C++ libraries for embedded sensor applications. All libraries compile and test on the host.

## Start Here

- [README.md](README.md) — ISensor, SensorReading, II2CBus, MQTTWriter, test doubles, adding sensors

## By Task

**Adding a sensor implementation:**
→ [README.md § Adding a New Sensor](README.md#adding-a-new-sensor) — pattern, BUILD targets, FakeI2CBus tests

**Understanding the sensor interface:**
→ [README.md § ISensor Interface](README.md#isensor-interface-firmwaresensorsensorh) — SensorReading struct, ISensor contract

**Using the I2C bus abstraction:**
→ [README.md § II2CBus Interface](README.md#ii2cbus-interface-firmwarei2ci2c_bush) — ArduinoI2CBus vs FakeI2CBus

**Writing tests with test doubles:**
→ [README.md § Test Doubles](README.md#test-doubles) — FakeSensor, FakeI2CBus, FakePublisher usage

**Understanding the MQTT pipeline:**
→ [README.md § MQTTWriter](README.md#mqttwriter-firmwaremqttmqtt_writerh)

## Related Docs

- [tools/firmware/README.md](../tools/firmware/README.md) — Build toolchain, esp32_firmware() macro, flashing
- [leaflab/sensorboard/README.md](../leaflab/sensorboard/README.md) — Worked example using these libraries
