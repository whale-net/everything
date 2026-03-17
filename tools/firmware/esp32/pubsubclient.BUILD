"""Build file for the PubSubClient library (@pubsubclient).

Version: 2.8 — MQTT client library for Arduino.
"""

package(default_visibility = ["//visibility:public"])

cc_library(
    name = "pubsubclient",
    srcs = glob(["src/**/*.cpp"]),
    hdrs = glob(["src/**/*.h"]),
    includes = ["src"],
    target_compatible_with = [
        "@platforms//os:none",
        "//tools/firmware:cpu_xtensa",
    ],
    deps = ["@arduino_esp32//:core_lib"],
)
