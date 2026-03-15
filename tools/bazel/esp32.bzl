"""ESP32 firmware build macro.

Usage:
    load("//tools/bazel:esp32.bzl", "esp32_firmware")

    esp32_firmware(
        name = "blink",
        srcs = ["blink.cc"],
        deps = ["@pigweed//pw_log"],
    )

This generates three targets:
  {name}_lib  — cc_library with your sources + Arduino core
  {name}_elf  — cc_binary (ELF suitable for inspection / upload)
  {name}_bin  — genrule that runs esptool elf2image → flashable .bin
"""

load("@rules_cc//cc:defs.bzl", "cc_binary", "cc_library")

ESP32_COMPAT = [
    "@platforms//os:none",
    "//tools/firmware:cpu_xtensa",
]

def esp32_firmware(name, srcs, deps = [], copts = [], **kwargs):
    """Creates {name}_lib, {name}_elf, and {name}_bin targets.

    Args:
        name: Target name prefix.
        srcs: Source files (.cc / .cpp) containing setup() and loop().
        deps: Additional cc_library dependencies.
        copts: Extra compiler options forwarded to cc_library.
        **kwargs: Forwarded to cc_library (e.g. visibility, includes).
    """

    cc_library(
        name = name + "_lib",
        srcs = srcs,
        copts = copts,
        target_compatible_with = ESP32_COMPAT,
        deps = deps + ["@arduino_esp32//:core_lib"],
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
