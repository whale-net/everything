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

def esp32_firmware(name, srcs, deps = [], copts = [], flash_config = None, flash_name = "flash", **kwargs):
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

    # Linker scripts define ESP32 memory layout (Arduino ESP32 3.x / ESP-IDF v5.5.2).
    # Must be in additional_linker_inputs so Bazel stages them in the sandbox
    # before the link action runs; the -T flags below reference them via $(execpath).
    # Order matches flags/ld_scripts in the esp32-libs archive.
    _ldscripts = [
        "@arduino_esp32_libs//:ld/esp32.rom.redefined.ld",
        "@arduino_esp32_libs//:ld/esp32.peripherals.ld",
        "@arduino_esp32_libs//:ld/esp32.rom.ld",
        "@arduino_esp32_libs//:ld/esp32.rom.api.ld",
        "@arduino_esp32_libs//:ld/esp32.rom.libgcc.ld",
        "@arduino_esp32_libs//:ld/esp32.rom.newlib-data.ld",
        "@arduino_esp32_libs//:ld/esp32.rom.syscalls.ld",
        "@arduino_esp32_libs//:ld/memory.ld",
        "@arduino_esp32_libs//:ld/sections.ld",
    ]

    cc_binary(
        name = name + "_elf",
        srcs = ["@arduino_esp32//:main_cpp"],
        additional_linker_inputs = _ldscripts,
        linkopts = (
            # -nostdlib / --gc-sections / -static / -EL are set by the toolchain's
            # default_link_flags_feature (cc_toolchain_config.bzl) — not repeated here.
            # Derive -T flags from _ldscripts to avoid maintaining a parallel list.
            [flag for ld in _ldscripts for flag in ["-T", "$(execpath {})".format(ld)]] + [
            # Linker defines and options — derived from flags/ld_flags in the esp32-libs archive.
            "-Wl,--defsym=IDF_TARGET_ESP32=0",
            "-Wl,--no-warn-rwx-segments",
            "-Wl,--orphan-handling=warn",
            "-Wl,--warn-common",
            "-Wl,--wrap=log_printf",
            "-Wl,--wrap=longjmp",
            # Force-include entry points — derived verbatim from flags/ld_flags.
            "-u", "nvs_sec_provider_include_impl",
            "-u", "ld_include_hli_vectors_bt",
            "-u", "_Z5setupv",       # setup() — Arduino entry point
            "-u", "_Z4loopv",        # loop()  — Arduino entry point
            "-u", "esp_app_desc",
            "-u", "esp_efuse_startup_include_func",
            "-u", "ld_include_highint_hdl",
            "-u", "start_app",
            "-u", "start_app_other_cores",
            "-u", "__ubsan_include",
            "-u", "esp_system_include_startup_funcs",
            "-u", "__assert_func",
            "-u", "esp_dport_access_reg_read",
            "-u", "esp_security_init_include_impl",
            "-Wl,--undefined=FreeRTOS_openocd_params",
            "-u", "app_main",
            "-u", "esp_libc_include_heap_impl",
            "-u", "esp_libc_include_reent_syscalls_impl",
            "-u", "esp_libc_include_syscalls_impl",
            "-u", "esp_libc_include_pthread_impl",
            "-u", "esp_libc_include_assert_impl",
            "-u", "esp_libc_include_getentropy_impl",
            "-u", "esp_libc_include_init_funcs",
            "-u", "esp_libc_init_funcs",
            "-u", "pthread_include_pthread_impl",
            "-u", "pthread_include_pthread_cond_var_impl",
            "-u", "pthread_include_pthread_local_storage_impl",
            "-u", "pthread_include_pthread_rwlock_impl",
            "-u", "pthread_include_pthread_semaphore_impl",
            "-u", "__cxa_guard_dummy",
            "-u", "__cxx_init_dummy",
            "-u", "esp_timer_init_include_func",
            "-u", "uart_vfs_include_dev_init",
            "-u", "include_esp_phy_override",
            "-u", "esp_vfs_include_console_register",
            "-u", "vfs_include_syscalls_impl",
            "-u", "esp_vfs_include_nullfs_register",
            "-u", "esp_system_include_coredump_init",
        ]),
        target_compatible_with = ESP32_COMPAT,
        deps = [
            ":" + name + "_lib",
            "@arduino_esp32//:core_lib",
            # Precompiled ESP-IDF SDK libs (inside --start-group via toolchain feature)
            "@arduino_esp32_libs//:sdk_lib",
            # GCC runtime + libstdc++ as deps so they're inside --start-group,
            # letting the linker resolve circular refs with SDK's libc/nvs_flash.
            "@xtensa_esp_elf_linux64//:esp32_cxx_runtime",
        ],
    )

    native.genrule(
        name = name + "_bin",
        srcs = [":" + name + "_elf"],
        outs = [name + ".bin"],
        cmd = " ".join([
            "$(location //tools/firmware/esp32:esptool_wrapper)",
            "--chip esp32 elf2image",
            flash_config.write_flash_args,
            "-o $@",
            "$<",
        ]),
        target_compatible_with = ESP32_COMPAT,
        tools = ["//tools/firmware/esp32:esptool_wrapper"],
    )

    flash_firmware(
        name = flash_name,
        firmware_name = name,
        board_config = flash_config,
    )
