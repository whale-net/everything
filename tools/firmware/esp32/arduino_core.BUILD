"""Build file for the Arduino ESP32 core archive (@arduino_esp32).

Version: 1.0.6 (simpler flag structure than 3.x; migrate after POC).

Key pitfall: cores/esp32/main.cpp calls setup() + loop().
It MUST be excluded from core_lib and added to the cc_binary srcs
directly, otherwise the linker cannot find user-defined setup()/loop().
"""

package(default_visibility = ["//visibility:public"])

exports_files(glob(["**"]))

# ── Pre-compiled SDK static libraries ────────────────────────────────────────

filegroup(
    name = "sdk_libs",
    srcs = glob(["tools/sdk/lib/*.a"]),
)

filegroup(
    name = "bootloader",
    srcs = glob(["tools/sdk/bin/bootloader_*.bin"]),
)

filegroup(
    name = "partitions",
    srcs = glob(["tools/partitions/*.bin"]),
)

# ── Arduino core C sources ───────────────────────────────────────────────────

cc_library(
    name = "core_c_lib",
    srcs = glob(
        ["cores/esp32/**/*.c"],
        exclude = ["cores/esp32/main.cpp"],
    ),
    hdrs = glob([
        "cores/esp32/**/*.h",
        "variants/esp32/**/*.h",
    ]),
    copts = [
        "-mlongcalls",
        "-ffunction-sections",
        "-fdata-sections",
        "-fstrict-volatile-bitfields",
        "-Os",
        "-std=gnu11",
        "-w",  # suppress upstream warnings
    ],
    includes = [
        "cores/esp32",
        "variants/esp32",
    ],
    target_compatible_with = [
        "@platforms//os:none",
        "//tools/firmware:cpu_xtensa",
    ],
)

# ── Arduino core C++ sources ─────────────────────────────────────────────────

cc_library(
    name = "core_lib",
    srcs = glob(
        ["cores/esp32/**/*.cpp"],
        exclude = ["cores/esp32/main.cpp"],
    ),
    hdrs = glob([
        "cores/esp32/**/*.h",
        "variants/esp32/**/*.h",
    ]),
    copts = [
        "-mlongcalls",
        "-ffunction-sections",
        "-fdata-sections",
        "-fstrict-volatile-bitfields",
        "-fno-exceptions",
        "-fno-rtti",
        "-Os",
        "-std=gnu++11",
        "-w",  # suppress upstream warnings
    ],
    includes = [
        "cores/esp32",
        "variants/esp32",
    ],
    target_compatible_with = [
        "@platforms//os:none",
        "//tools/firmware:cpu_xtensa",
    ],
    deps = [":core_c_lib"],
)

# ── main.cpp entry point — add to cc_binary srcs, not deps ──────────────────
# Linking after user's setup()/loop() requires it to appear as a source,
# not as a pre-compiled dependency.

filegroup(
    name = "main_cpp",
    srcs = ["cores/esp32/main.cpp"],
)

# ── Optional peripheral libraries (add as needed) ────────────────────────────

cc_library(
    name = "WiFi",
    srcs = glob(["libraries/WiFi/src/**/*.cpp"]),
    hdrs = glob(["libraries/WiFi/src/**/*.h"]),
    includes = ["libraries/WiFi/src"],
    target_compatible_with = [
        "@platforms//os:none",
        "//tools/firmware:cpu_xtensa",
    ],
    deps = [":core_lib"],
)
