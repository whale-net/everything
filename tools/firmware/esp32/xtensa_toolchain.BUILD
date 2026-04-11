"""Build file for the Xtensa ESP-ELF GCC archive (@xtensa_esp_elf_linux64).

The toolchain ships two binary families:
  xtensa-esp-elf-*    — generic multi-target Xtensa (big-endian default)
  xtensa-esp32-elf-*  — ESP32-specific (little-endian default, required)

We always use the xtensa-esp32-elf-* variants so the compiler produces
little-endian objects without needing extra flags.
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
    srcs = ["bin/xtensa-esp32-elf-gcc"],
)

filegroup(
    name = "g++",
    srcs = ["bin/xtensa-esp32-elf-g++"],
)

filegroup(
    name = "ar",
    srcs = ["bin/xtensa-esp32-elf-ar"],
)

filegroup(
    name = "ld",
    srcs = ["bin/xtensa-esp32-elf-ld"],
)

filegroup(
    name = "objcopy",
    srcs = ["bin/xtensa-esp32-elf-objcopy"],
)

filegroup(
    name = "strip",
    srcs = ["bin/xtensa-esp32-elf-strip"],
)

load("@rules_cc//cc:defs.bzl", "cc_library")

# GCC runtime + C++ standard library for ESP32 (little-endian Xtensa LX6).
# These must be inside the --start-group/--end-group block (added as cc_library
# deps, not -l linkopts) so the linker can resolve circular references with the
# ESP-IDF SDK libs (e.g. libnvs_flash references _Unwind_Resume from libstdc++,
# and eh_alloc in libstdc++ references getenv from the SDK's libc).
cc_library(
    name = "esp32_cxx_runtime",
    srcs = glob([
        # glob matches regardless of GCC version (e.g. 15.2.0 → 16.x on upgrade)
        "lib/gcc/xtensa-esp-elf/*/esp32/libgcc.a",
        "xtensa-esp-elf/lib/esp32/libstdc++.a",
        # newlib libc and libm for ESP32 — provide memcpy/memset/fprintf etc.
        # Use the plain esp32 variant (not no-rtti/psram) to match the
        # toolchain's default search path when -lc / -lm are passed.
        "xtensa-esp-elf/lib/esp32/libc.a",
        "xtensa-esp-elf/lib/esp32/libm.a",
    ]),
)
