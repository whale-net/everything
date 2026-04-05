"""Build file for the Arduino ESP32 core archive (@arduino_esp32).

Version: 1.0.6 (simpler flag structure than 3.x; migrate after POC).

Key pitfall: cores/esp32/main.cpp calls setup() + loop().
It MUST be excluded from core_lib and added to the cc_binary srcs
directly, otherwise the linker cannot find user-defined setup()/loop().
"""

package(default_visibility = ["//visibility:public"])

# Export only the linker scripts referenced via $(execpath ...) in esp32.bzl.
# (Previously exported glob(["**"]) which was overly broad.)
exports_files(glob(["tools/sdk/ld/*.ld"]))

# ── Pre-compiled SDK static libraries ────────────────────────────────────────

filegroup(
    name = "bootloader",
    srcs = glob(["tools/sdk/bin/bootloader_*.bin"]),
)

# Precompiled ESP-IDF SDK static libraries, exposed as a cc_library so Bazel
# includes them in the --start-group/--end-group block (see cc_toolchain_config.bzl).
# This matches what Arduino's platform.txt does: link all SDK .a files in one group.
cc_library(
    name = "sdk_lib",
    srcs = glob(["tools/sdk/lib/*.a"]),
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
)

filegroup(
    name = "partitions",
    srcs = glob(["tools/partitions/*.bin"]),
)

# ── ESP-IDF SDK include paths ─────────────────────────────────────────────────
# The Arduino ESP32 core (1.0.6) requires headers from the pre-built ESP-IDF
# SDK bundled in tools/sdk/include/.  Two levels are needed:
#   1. tools/sdk/include          — for #include "freertos/FreeRTOS.h" style
#   2. tools/sdk/include/<subdir> — for bare #include "FreeRTOSConfig.h" inside
#                                   each SDK component's own headers

_SDK_INCLUDES = [
    "tools/sdk/include",
    "tools/sdk/include/app_trace",
    "tools/sdk/include/app_update",
    "tools/sdk/include/asio",
    "tools/sdk/include/bootloader_support",
    "tools/sdk/include/bt",
    "tools/sdk/include/coap",
    "tools/sdk/include/config",
    "tools/sdk/include/console",
    "tools/sdk/include/driver",
    "tools/sdk/include/efuse",
    "tools/sdk/include/esp-tls",
    "tools/sdk/include/esp32",
    "tools/sdk/include/esp_adc_cal",
    "tools/sdk/include/esp_event",
    "tools/sdk/include/esp_http_client",
    "tools/sdk/include/esp_http_server",
    "tools/sdk/include/esp_https_ota",
    "tools/sdk/include/esp_https_server",
    "tools/sdk/include/esp_ringbuf",
    "tools/sdk/include/esp_websocket_client",
    "tools/sdk/include/espcoredump",
    "tools/sdk/include/ethernet",
    "tools/sdk/include/fatfs",
    "tools/sdk/include/freertos",
    "tools/sdk/include/heap",
    "tools/sdk/include/log",
    "tools/sdk/include/lwip",
    "tools/sdk/include/mbedtls",
    "tools/sdk/include/mdns",
    "tools/sdk/include/mqtt",
    "tools/sdk/include/newlib",
    "tools/sdk/include/nghttp",
    "tools/sdk/include/nvs_flash",
    "tools/sdk/include/openssl",
    "tools/sdk/include/pthread",
    "tools/sdk/include/sdmmc",
    "tools/sdk/include/soc",
    "tools/sdk/include/spi_flash",
    "tools/sdk/include/spiffs",
    "tools/sdk/include/tcp_transport",
    "tools/sdk/include/tcpip_adapter",
    "tools/sdk/include/ulp",
    "tools/sdk/include/vfs",
    "tools/sdk/include/wear_levelling",
    "tools/sdk/include/wpa_supplicant",
    "tools/sdk/include/xtensa-debug-module",
]

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
        "tools/sdk/include/**/*.h",
    ]),
    copts = [
        "-mlongcalls",
        "-ffunction-sections",
        "-fdata-sections",
        "-fstrict-volatile-bitfields",
        "-Os",
        "-std=gnu11",
    ] + _GCC15_COMPAT,
    includes = [
        "cores/esp32",
        "variants/esp32",
    ] + _SDK_INCLUDES,
    target_compatible_with = [
        "@platforms//os:none",
        "@@//tools/firmware:cpu_xtensa",
    ],
)

# ── Arduino core C++ sources ─────────────────────────────────────────────────

cc_library(
    name = "core_lib",
    srcs = glob(
        ["cores/esp32/**/*.cpp"],
        exclude = ["cores/esp32/main.cpp"],
    ),
    # hdrs and includes are intentionally omitted: core_c_lib already declares
    # them and exports them transitively (Bazel propagates cc_library hdrs and
    # includes through the dep graph). Declaring them again would be redundant.
    copts = [
        "-mlongcalls",
        "-ffunction-sections",
        "-fdata-sections",
        "-fstrict-volatile-bitfields",
        "-fno-exceptions",
        "-fno-rtti",
        "-Os",
        "-std=gnu++11",
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
