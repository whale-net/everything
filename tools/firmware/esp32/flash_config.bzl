"""ESP32 flash configurations (board-agnostic flash params for Xtensa ESP32).

Import the appropriate struct in your board's BUILD.bazel and pass it to
flash_firmware() via tools/bazel/esp32.bzl's esp32_firmware() macro.
"""

# Flash config for DIO 80 MHz, 4 MB flash — suitable for most ESP32 dev boards
# including ELEGOO, HiLetgo NodeMCU-32S, DOIT DevKit v1.
ESP32_DIO_80M = struct(
    tool = "esptool",
    chip = "esp32",
    baud = 921600,
    before = "default_reset",
    after = "hard_reset",
    write_flash_args = "--flash_mode dio --flash_freq 80m --flash_size 4MB",
    app_offset = "0x10000",
    # Runfile-root-relative paths for segments flashed before the app binary.
    # The bash runfiles library resolves the `arduino_esp32` prefix via the
    # repo mapping, so these paths are stable regardless of canonical name.
    pre_segments = [
        ("0x1000", "arduino_esp32/tools/sdk/bin/bootloader_dio_80m.bin"),
        ("0x8000", "arduino_esp32/tools/partitions/default.bin"),
    ],
    # Labels for those same files — needed as data deps in the sh_binary.
    pre_segment_labels = [
        "@arduino_esp32//:bootloader",
        "@arduino_esp32//:partitions",
    ],
    # Label of the esptool py_binary.
    esptool = "//tools/firmware/esp32:esptool_wrapper",
    esptool_runfile = "_main/tools/firmware/esp32/esptool_wrapper",
)

# QIO 80 MHz variant — faster but requires all 4 data pins; use for modules
# with full quad-IO support (e.g. ESP-WROOM-32).
ESP32_QIO_80M = struct(
    tool = "esptool",
    chip = "esp32",
    baud = 921600,
    before = "default_reset",
    after = "hard_reset",
    write_flash_args = "--flash_mode qio --flash_freq 80m --flash_size 4MB",
    app_offset = "0x10000",
    pre_segments = [
        ("0x1000", "arduino_esp32/tools/sdk/bin/bootloader_qio_80m.bin"),
        ("0x8000", "arduino_esp32/tools/partitions/default.bin"),
    ],
    pre_segment_labels = [
        "@arduino_esp32//:bootloader",
        "@arduino_esp32//:partitions",
    ],
    esptool = "//tools/firmware/esp32:esptool_wrapper",
    esptool_runfile = "_main/tools/firmware/esp32/esptool_wrapper",
)
