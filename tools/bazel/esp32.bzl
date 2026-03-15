"""ESP32 firmware build macro.

Usage:
    load("//tools/bazel:esp32.bzl", "esp32_firmware")

    esp32_firmware(
        name = "blink",
        srcs = ["blink.cc"],
        deps = ["@pigweed//pw_log"],
        flash_config = ESP32_DIO_80M,  # optional; defaults to DIO 80 MHz
    )

This generates:
  {name}_lib  — cc_library with your sources + Arduino core + board_pins
  {name}_elf  — cc_binary (ELF, inspect with readelf / objdump)
  {name}_bin  — genrule → flashable .bin (via esptool elf2image)
  flash       — sh_binary: bazel run //{pkg}:flash -- /dev/ttyUSB0
"""

load("@rules_cc//cc:defs.bzl", "cc_binary", "cc_library")
load("//tools/firmware:flash.bzl", "flash_firmware")
load("//tools/firmware/esp32:flash_config.bzl", "ESP32_DIO_80M")

ESP32_COMPAT = [
    "@platforms//os:none",
    "//tools/firmware:cpu_xtensa",
]

def esp32_firmware(name, srcs, deps = [], copts = [], flash_config = None, **kwargs):
    """Creates {name}_lib, {name}_elf, {name}_bin, and :flash targets.

    Args:
        name:         Target name prefix.
        srcs:         Source files (.cc / .cpp) containing setup() and loop().
        deps:         Additional cc_library dependencies.
        copts:        Extra compiler options forwarded to cc_library.
        flash_config: A flash config struct from tools/firmware/esp32/flash_config.bzl.
                      Defaults to ESP32_DIO_80M (DIO 80 MHz, 4 MB flash).
        **kwargs:     Forwarded to cc_library (e.g. visibility, includes).
    """
    if flash_config == None:
        flash_config = ESP32_DIO_80M

    cc_library(
        name = name + "_lib",
        srcs = srcs,
        copts = copts,
        target_compatible_with = ESP32_COMPAT,
        deps = deps + [
            "@arduino_esp32//:core_lib",
            "//tools/firmware:board_pins",
        ],
        **kwargs
    )

    cc_binary(
        name = name + "_elf",
        srcs = ["@arduino_esp32//:main_cpp"],
        linkopts = [
            "-Wl,--gc-sections",
            "-Wl,-EL",
            "-nostdlib",
        ],
        target_compatible_with = ESP32_COMPAT,
        deps = [
            ":" + name + "_lib",
            "@arduino_esp32//:core_c_lib",
            "@arduino_esp32//:core_lib",
        ],
    )

    native.genrule(
        name = name + "_bin",
        srcs = [":" + name + "_elf"],
        outs = [name + ".bin"],
        cmd = " ".join([
            "$(location //tools/firmware/esp32:esptool_wrapper)",
            "--chip esp32 elf2image",
            "--flash_mode dio",
            "--flash_freq 80m",
            "--flash_size 4MB",
            "-o $@",
            "$<",
        ]),
        target_compatible_with = ESP32_COMPAT,
        tools = ["//tools/firmware/esp32:esptool_wrapper"],
    )

    flash_firmware(
        name = "flash",
        firmware_name = name,
        board_config = flash_config,
    )
