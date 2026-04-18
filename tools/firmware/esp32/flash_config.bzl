"""ESP32 flash configurations (board-agnostic flash params for Xtensa ESP32).

Import the appropriate struct in your board's BUILD.bazel and pass it to
flash_firmware() via tools/bazel/esp32.bzl's esp32_firmware() macro.

Each pre_segments entry is a struct with:
  addr   — flash offset string (e.g. "0x1000")
  label  — Bazel label of the genrule/target producing the binary
  output — filename of the genrule output (e.g. "bootloader_dio_80m.bin")
flash.bzl derives the rlocation path from label + output automatically.
"""

def _seg(addr, label, output):
    return struct(addr = addr, label = label, output = output)

# Flash config for DIO 80 MHz, 4 MB flash — suitable for most ESP32 dev boards
# including ELEGOO, HiLetgo NodeMCU-32S, DOIT DevKit v1.
ESP32_DIO_80M = struct(
    tool = "esptool",
    chip = "esp32",
    baud = 921600,
    before = "default-reset",
    after = "hard-reset",
    write_flash_args = "--flash-mode dio --flash-freq 80m --flash-size 4MB",
    app_offset = "0x10000",
    pre_segments = [
        _seg("0x1000", "//tools/firmware/esp32:bootloader_dio_80m_bin", "bootloader_dio_80m.bin"),
        _seg("0x8000", "//tools/firmware/esp32:max_app_4MB_bin", "max_app_4MB.bin"),
    ],
    esptool = "//tools/firmware/esp32:esptool_wrapper",
)

# QIO 80 MHz variant — faster but requires all 4 data pins; use for modules
# with full quad-IO support (e.g. ESP-WROOM-32).
ESP32_QIO_80M = struct(
    tool = "esptool",
    chip = "esp32",
    baud = 921600,
    before = "default-reset",
    after = "hard-reset",
    write_flash_args = "--flash-mode qio --flash-freq 80m --flash-size 4MB",
    app_offset = "0x10000",
    pre_segments = [
        _seg("0x1000", "//tools/firmware/esp32:bootloader_qio_80m_bin", "bootloader_qio_80m.bin"),
        _seg("0x8000", "//tools/firmware/esp32:max_app_4MB_bin", "max_app_4MB.bin"),
    ],
    esptool = "//tools/firmware/esp32:esptool_wrapper",
)
