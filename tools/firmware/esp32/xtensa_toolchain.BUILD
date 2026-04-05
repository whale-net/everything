"""Build file for the Xtensa ESP-ELF GCC archive (@xtensa_esp_elf_linux64).

Binary prefix in the 15.x toolchain: xtensa-esp-elf-
Verify after extraction: ls bin/
"""

package(default_visibility = ["//visibility:public"])

exports_files(glob(["**"]))

filegroup(
    name = "all_files",
    srcs = glob([
        "bin/**",
        "xtensa-esp-elf/**",
        "libexec/**",
        "lib/gcc/**",
    ]),
)

filegroup(
    name = "gcc",
    srcs = ["bin/xtensa-esp-elf-gcc"],
)

filegroup(
    name = "g++",
    srcs = ["bin/xtensa-esp-elf-g++"],
)

filegroup(
    name = "ar",
    srcs = ["bin/xtensa-esp-elf-ar"],
)

filegroup(
    name = "ld",
    srcs = ["bin/xtensa-esp-elf-ld"],
)

filegroup(
    name = "objcopy",
    srcs = ["bin/xtensa-esp-elf-objcopy"],
)

filegroup(
    name = "strip",
    srcs = ["bin/xtensa-esp-elf-strip"],
)

load("@rules_cc//cc:defs.bzl", "cc_library")

# GCC runtime + C++ standard library for ESP32 (little-endian Xtensa LX6).
# These must be inside the --start-group/--end-group block (added as cc_library
# deps, not -l linkopts) so the linker can resolve circular references with the
# ESP-IDF SDK libs (e.g. libnvs_flash references _Unwind_Resume from libstdc++,
# and eh_alloc in libstdc++ references getenv from the SDK's libc).
cc_library(
    name = "esp32_cxx_runtime",
    srcs = [
        "lib/gcc/xtensa-esp-elf/15.2.0/esp32/libgcc.a",
        "xtensa-esp-elf/lib/esp32/libstdc++.a",
    ],
)
