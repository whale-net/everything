"""Build file for the Arduino ESP32 core archive (@arduino_esp32).

Version: 3.3.7 (ESP-IDF v5.5.2).

Structure of esp32-core-3.3.7/:
  cores/esp32/         — Arduino framework C/C++ sources
  libraries/           — optional Arduino peripheral libraries
  variants/esp32/      — board variant headers (pins_arduino.h etc.)
  tools/partitions/    — partition table .bin files

The precompiled ESP-IDF SDK (headers, .a libs, linker scripts, bootloader)
lives in the companion archive @arduino_esp32_libs.

Key pitfall: cores/esp32/main.cpp calls setup() + loop().
It MUST be excluded from core_lib and added to the cc_binary srcs
directly, otherwise the linker cannot find user-defined setup()/loop().
"""

package(default_visibility = ["//visibility:public"])

filegroup(
    name = "partitions",
    srcs = glob(["tools/partitions/*.bin"]),
)

exports_files([
    "tools/gen_esp32part.py",
    "tools/partitions/max_app_4MB.csv",
])

# ── GCC 15 compatibility flags (applied to all Arduino core targets) ─────────
# GCC 15 is stricter about transitive headers, implicit conversions, and POSIX
# feature-test macros. These flags paper over upstream code that predates GCC 15.
_GCC15_COMPAT = [
    "-include", "stdint.h",               # stdint types not always transitively available
    "-Wno-error=int-conversion",           # promoted to hard errors in GCC 15
    "-Wno-error=incompatible-pointer-types",
    "-D_POSIX_TIMEOUTS",                   # enables pthread_mutex_timedlock in newlib
    "-w",                                  # suppress remaining upstream warnings
]

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
        "-std=gnu17",
    ] + _GCC15_COMPAT,
    defines = [
        "ARDUINO=10816",        # Arduino-ESP32 3.x API version
        "ARDUINO_ARCH_ESP32",
    ],
    includes = [
        "cores/esp32",
        "variants/esp32",
    ],
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
    # @@//tools/firmware/esp32:arduino_board_defines supplies ARDUINO_BOARD,
    # ARDUINO_VARIANT, and the board macro via select() on board constraint values.
    # Its defines propagate here because core_c_lib depends on it.
    deps = [
        "@arduino_esp32_libs//:sdk_lib",
        "@@//tools/firmware/esp32:arduino_board_defines",
    ],
)

# ── Arduino core C++ sources ─────────────────────────────────────────────────

cc_library(
    name = "core_lib",
    srcs = glob(
        ["cores/esp32/**/*.cpp"],
        exclude = ["cores/esp32/main.cpp"],
    ),
    # hdrs and includes intentionally omitted: core_c_lib already declares them
    # and exports them transitively via the dep graph.
    copts = [
        "-mlongcalls",
        "-ffunction-sections",
        "-fdata-sections",
        "-fstrict-volatile-bitfields",
        "-fno-exceptions",
        "-fno-rtti",
        "-Os",
        "-std=gnu++2b",
    ] + _GCC15_COMPAT,
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
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
    name = "Wire",
    srcs = glob(["libraries/Wire/src/**/*.cpp"]),
    hdrs = glob(["libraries/Wire/src/**/*.h"]),
    includes = ["libraries/Wire/src"],
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
    deps = [":core_lib"],
)

cc_library(
    name = "Network",
    srcs = glob(["libraries/Network/src/**/*.cpp"]),
    hdrs = glob(["libraries/Network/src/**/*.h"]),
    includes = ["libraries/Network/src"],
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
    deps = [":core_lib"],
)

cc_library(
    name = "WiFi",
    srcs = glob(["libraries/WiFi/src/**/*.cpp"]),
    hdrs = glob(["libraries/WiFi/src/**/*.h"]),
    includes = ["libraries/WiFi/src"],
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
    deps = [":core_lib", ":Network"],
)

cc_library(
    name = "Preferences",
    srcs = glob(["libraries/Preferences/src/**/*.cpp"]),
    hdrs = glob(["libraries/Preferences/src/**/*.h"]),
    includes = ["libraries/Preferences/src"],
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
    deps = [":core_lib"],
)
