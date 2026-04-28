// Unit tests for ConfigApplier.
//
// Three fixture groups:
//   DirectSensors    — sensors directly on root bus (no mux)
//   SingleMuxSensors — sensors behind one TCA9548A
//   ChainedMuxSensors — sensors behind two cascaded TCA9548As
//
//   bazel test //firmware/config:config_applier_test

#include "firmware/config/config_applier.h"

#include <cstring>

#include "firmware/proto/config.pb.h"
#include "firmware/sensor/mock_sensor.h"
#include "pw_span/span.h"
#include "pw_unit_test/framework.h"

namespace firmware {
namespace {

using testing::FakeSensor;

// ── Factory helper (avoids most-vexing parse) ─────────────────────────────────

template <size_t N>
ConfigApplier MakeApplier(ISensor* (&arr)[N]) {
    return ConfigApplier(pw::span<ISensor* const>(
        reinterpret_cast<ISensor* const*>(arr), N));
}

// ── Config builder helpers ────────────────────────────────────────────────────

firmware_SensorConfig MakeSensorConfig(uint8_t i2c_addr, const char* name,
                                        bool enabled = true,
                                        uint32_t poll_ms = 0) {
    firmware_SensorConfig sc = firmware_SensorConfig_init_zero;
    sc.i2c_address = i2c_addr;
    sc.enabled = enabled;
    sc.poll_interval_ms = poll_ms;
    strncpy(sc.name, name, sizeof(sc.name) - 1);
    return sc;
}

firmware_SensorConfig MakeMuxSensorConfig(uint8_t mux_addr, uint8_t mux_ch,
                                           uint8_t i2c_addr, const char* name,
                                           bool enabled = true) {
    firmware_SensorConfig sc = MakeSensorConfig(i2c_addr, name, enabled);
    sc.mux_path_count = 1;
    sc.mux_path[0].mux_address = mux_addr;
    sc.mux_path[0].mux_channel = mux_ch;
    return sc;
}

firmware_SensorConfig MakeChainedMuxSensorConfig(
    uint8_t outer_addr, uint8_t outer_ch,
    uint8_t inner_addr, uint8_t inner_ch,
    uint8_t i2c_addr, const char* name,
    bool enabled = true) {
    firmware_SensorConfig sc = MakeSensorConfig(i2c_addr, name, enabled);
    sc.mux_path_count = 2;
    sc.mux_path[0].mux_address = outer_addr;
    sc.mux_path[0].mux_channel = outer_ch;
    sc.mux_path[1].mux_address = inner_addr;
    sc.mux_path[1].mux_channel = inner_ch;
    return sc;
}

firmware_DeviceConfig MakeDeviceConfig(uint64_t version) {
    firmware_DeviceConfig cfg = firmware_DeviceConfig_init_zero;
    cfg.version = version;
    strncpy(cfg.device_id, "leaflab-test", sizeof(cfg.device_id) - 1);
    return cfg;
}

// ── DirectSensors — no mux ────────────────────────────────────────────────────

class DirectSensorsTest : public ::testing::Test {
 protected:
    void SetUp() override {
        sensors[0] = &light;
        sensors[1] = &temp;
        sensors[2] = &humid;
    }

    FakeSensor light{"light",     0x23, 100.0f};
    FakeSensor temp {"board-temp",0x44, 22.5f};
    FakeSensor humid{"board-humi",0x45, 55.0f};
    ISensor* sensors[3];
};

TEST_F(DirectSensorsTest, DefaultsBeforeApply) {
    auto applier = MakeApplier(sensors);
    EXPECT_TRUE(applier.IsEnabled(0));
    EXPECT_TRUE(applier.IsEnabled(1));
    EXPECT_EQ(applier.PollIntervalMs(0), static_cast<uint32_t>(0));
}

TEST_F(DirectSensorsTest, NameOverrideApplied) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    cfg.sensors[0] = MakeSensorConfig(0x23, "canopy-light");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(light.name(), "canopy-light");
    EXPECT_STREQ(temp.name(),  "board-temp");  // unchanged
}

TEST_F(DirectSensorsTest, SensorDisabled) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    cfg.sensors[0] = MakeSensorConfig(0x44, "board-temp", /*enabled=*/false);

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_TRUE(applier.IsEnabled(0));   // light: not in config → enabled
    EXPECT_FALSE(applier.IsEnabled(1));  // temp: explicitly disabled
    EXPECT_TRUE(applier.IsEnabled(2));   // humid: not in config → enabled
}

TEST_F(DirectSensorsTest, PollIntervalApplied) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    cfg.sensors[0] = MakeSensorConfig(0x23, "light", true, /*poll_ms=*/30000);

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_EQ(applier.PollIntervalMs(0), static_cast<uint32_t>(30000));
    EXPECT_EQ(applier.PollIntervalMs(1), static_cast<uint32_t>(0));  // unchanged
}

TEST_F(DirectSensorsTest, WrongAddressDoesNotMatch) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    cfg.sensors[0] = MakeSensorConfig(0x99, "bogus");  // no sensor at 0x99

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(light.name(), "light");
    EXPECT_STREQ(temp.name(),  "board-temp");
    EXPECT_TRUE(applier.IsEnabled(0));
    EXPECT_TRUE(applier.IsEnabled(1));
}

TEST_F(DirectSensorsTest, EmptyNameDoesNotOverride) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    cfg.sensors[0] = MakeSensorConfig(0x23, "");  // empty name

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(light.name(), "light");  // unchanged
}

TEST_F(DirectSensorsTest, MultipleOverridesInOneConfig) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 3;
    cfg.sensors[0] = MakeSensorConfig(0x23, "renamed-light");
    cfg.sensors[1] = MakeSensorConfig(0x44, "renamed-temp", /*enabled=*/false);
    cfg.sensors[2] = MakeSensorConfig(0x45, "renamed-humi", true, /*poll_ms=*/10000);

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(light.name(), "renamed-light");
    EXPECT_STREQ(temp.name(),  "renamed-temp");
    EXPECT_STREQ(humid.name(), "renamed-humi");
    EXPECT_TRUE(applier.IsEnabled(0));
    EXPECT_FALSE(applier.IsEnabled(1));
    EXPECT_TRUE(applier.IsEnabled(2));
    EXPECT_EQ(applier.PollIntervalMs(2), static_cast<uint32_t>(10000));
}

TEST_F(DirectSensorsTest, ApplyResetsPreviousState) {
    auto applier = MakeApplier(sensors);

    // First apply — disable temp.
    auto cfg1 = MakeDeviceConfig(1);
    cfg1.sensors_count = 1;
    cfg1.sensors[0] = MakeSensorConfig(0x44, "temp", /*enabled=*/false);
    applier.Apply(cfg1);
    EXPECT_FALSE(applier.IsEnabled(1));

    // Second apply — config no longer disables temp.
    auto cfg2 = MakeDeviceConfig(2);
    cfg2.sensors_count = 0;
    applier.Apply(cfg2);
    EXPECT_TRUE(applier.IsEnabled(1));  // reset to default
}

// ── SingleMuxSensors — one TCA9548A ──────────────────────────────────────────
//
// Layout: root bus → TCA9548A @ 0x70
//           channel 0: BH1750 @ 0x23
//           channel 5: SHT3x  @ 0x44

class SingleMuxSensorsTest : public ::testing::Test {
 protected:
    void SetUp() override {
        MuxHop ch0[] = {{0x70, 0}};
        MuxHop ch5[] = {{0x70, 5}};
        bh1750.set_mux_path(ch0);
        sht3x.set_mux_path(ch5);

        sensors[0] = &bh1750;
        sensors[1] = &sht3x;
    }

    FakeSensor bh1750{"max-light", 0x23, 800.0f};
    FakeSensor sht3x {"board-temp",0x44, 21.0f};
    ISensor* sensors[2];
};

TEST_F(SingleMuxSensorsTest, MatchByMuxPathAndAddress) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    cfg.sensors[0] = MakeMuxSensorConfig(0x70, 0, 0x23, "canopy");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "canopy");
    EXPECT_STREQ(sht3x.name(), "board-temp");  // not in config
}

TEST_F(SingleMuxSensorsTest, WrongChannelDoesNotMatch) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    // Correct address 0x23 but wrong channel (2 instead of 0).
    cfg.sensors[0] = MakeMuxSensorConfig(0x70, 2, 0x23, "wrong-channel");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "max-light");  // unchanged
}

TEST_F(SingleMuxSensorsTest, WrongMuxAddressDoesNotMatch) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    // Correct address + channel but wrong mux address.
    cfg.sensors[0] = MakeMuxSensorConfig(0x71, 0, 0x23, "wrong-mux");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "max-light");  // unchanged
}

TEST_F(SingleMuxSensorsTest, DirectConfigDoesNotMatchMuxSensor) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    // Config entry has no mux path (depth 0) but sensor has depth 1.
    cfg.sensors[0] = MakeSensorConfig(0x23, "no-mux-match");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "max-light");  // unchanged
}

TEST_F(SingleMuxSensorsTest, BothChannelsMatched) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 2;
    cfg.sensors[0] = MakeMuxSensorConfig(0x70, 0, 0x23, "light-new");
    cfg.sensors[1] = MakeMuxSensorConfig(0x70, 5, 0x44, "temp-new",
                                          /*enabled=*/false);

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "light-new");
    EXPECT_STREQ(sht3x.name(),  "temp-new");
    EXPECT_TRUE(applier.IsEnabled(0));
    EXPECT_FALSE(applier.IsEnabled(1));
}

// ── ChainedMuxSensors — two cascaded TCA9548As ───────────────────────────────
//
// Layout:
//   root bus
//   └── TCA9548A @ 0x70, channel 3   (outer mux)
//       ├── TCA9548A @ 0x71, channel 1  (inner mux)
//       │   ├── BH1750  @ 0x23
//       │   └── CCS811  @ 0x5A
//       └── TCA9548A @ 0x71, channel 2
//           └── SHT3x   @ 0x44

class ChainedMuxSensorsTest : public ::testing::Test {
 protected:
    void SetUp() override {
        MuxHop path_bh1750[] = {{0x70, 3}, {0x71, 1}};
        MuxHop path_ccs811[] = {{0x70, 3}, {0x71, 1}};
        MuxHop path_sht3x[]  = {{0x70, 3}, {0x71, 2}};

        bh1750.set_mux_path(path_bh1750);
        ccs811.set_mux_path(path_ccs811);
        sht3x.set_mux_path(path_sht3x);

        sensors[0] = &bh1750;
        sensors[1] = &ccs811;
        sensors[2] = &sht3x;
    }

    FakeSensor bh1750{"deep-light", 0x23, 500.0f};
    FakeSensor ccs811{"deep-eco2",  0x5A, 420.0f};
    FakeSensor sht3x {"deep-temp",  0x44, 20.0f};
    ISensor* sensors[3];
};

TEST_F(ChainedMuxSensorsTest, FullPathMatchAppliesName) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    cfg.sensors[0] = MakeChainedMuxSensorConfig(
        0x70, 3, 0x71, 1, 0x23, "canopy-deep-light");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "canopy-deep-light");
    EXPECT_STREQ(ccs811.name(), "deep-eco2");  // same path but different address
    EXPECT_STREQ(sht3x.name(),  "deep-temp");  // different inner channel
}

TEST_F(ChainedMuxSensorsTest, SameInnerMuxDifferentChannelDistinguished) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 2;
    cfg.sensors[0] = MakeChainedMuxSensorConfig(
        0x70, 3, 0x71, 1, 0x23, "light-named");
    cfg.sensors[1] = MakeChainedMuxSensorConfig(
        0x70, 3, 0x71, 2, 0x44, "temp-named");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "light-named");
    EXPECT_STREQ(sht3x.name(),  "temp-named");
    EXPECT_STREQ(ccs811.name(), "deep-eco2");  // unchanged
}

TEST_F(ChainedMuxSensorsTest, ShallowConfigDoesNotMatchDeepSensor) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    // Only one hop — depth mismatch with our depth-2 sensors.
    cfg.sensors[0] = MakeMuxSensorConfig(0x70, 3, 0x23, "shallow-miss");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "deep-light");  // unchanged
}

TEST_F(ChainedMuxSensorsTest, WrongOuterHopDoesNotMatch) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 1;
    // Inner hop correct but outer mux channel wrong (4 vs 3).
    cfg.sensors[0] = MakeChainedMuxSensorConfig(
        0x70, 4, 0x71, 1, 0x23, "outer-wrong");

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "deep-light");  // unchanged
}

TEST_F(ChainedMuxSensorsTest, AllThreeSensorsConfigured) {
    auto cfg = MakeDeviceConfig(1);
    cfg.sensors_count = 3;
    cfg.sensors[0] = MakeChainedMuxSensorConfig(
        0x70, 3, 0x71, 1, 0x23, "r-light");
    cfg.sensors[1] = MakeChainedMuxSensorConfig(
        0x70, 3, 0x71, 1, 0x5A, "r-eco2", /*enabled=*/false);
    cfg.sensors[2] = MakeChainedMuxSensorConfig(
        0x70, 3, 0x71, 2, 0x44, "r-temp");
    cfg.sensors[2].poll_interval_ms = 5000;

    auto applier = MakeApplier(sensors);
    applier.Apply(cfg);

    EXPECT_STREQ(bh1750.name(), "r-light");
    EXPECT_STREQ(ccs811.name(), "r-eco2");
    EXPECT_STREQ(sht3x.name(),  "r-temp");
    EXPECT_TRUE(applier.IsEnabled(0));
    EXPECT_FALSE(applier.IsEnabled(1));
    EXPECT_TRUE(applier.IsEnabled(2));
    EXPECT_EQ(applier.PollIntervalMs(2), static_cast<uint32_t>(5000));
}

}  // namespace
}  // namespace firmware
