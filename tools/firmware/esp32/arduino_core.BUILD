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

# ── GCC 15 compatibility flags (applied to all Arduino core targets) ─────────
# GCC 15 is stricter about transitive headers, implicit conversions, and POSIX
# feature-test macros. These flags paper over upstream code that predates GCC 15.
# Arduino board/variant identity defines.
# ARDUINO_BOARD and ARDUINO_VARIANT are used by Arduino framework code (e.g.
# chip-debug-report.cpp) to embed board information in the build.
_ARDUINO_DEFINES = [
    "ARDUINO=10816",        # Arduino version 1.8.16 (Arduino-ESP32 3.x convention)
    "ARDUINO_ESP32_DEV",    # Board macro (generic ESP32 dev board)
    'ARDUINO_BOARD=\\"ESP32_DEV\\"',
    'ARDUINO_VARIANT=\\"esp32\\"',
    "ARDUINO_ARCH_ESP32",
]

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
    defines = _ARDUINO_DEFINES,
    includes = [
        "cores/esp32",
        "variants/esp32",
    ],
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
    deps = ["@arduino_esp32_libs//:sdk_lib"],
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
    name = "WiFi",
    srcs = glob(["libraries/WiFi/src/**/*.cpp"]),
    hdrs = glob(["libraries/WiFi/src/**/*.h"]),
    includes = ["libraries/WiFi/src"],
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
    deps = [":core_lib"],
)
