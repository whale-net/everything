#!/bin/bash
# Flash the blink firmware to an ELEGOO ESP32 via CP2102 USB-UART.
#
# Usage:
#   bazel run //demo/blink:flash -- /dev/ttyUSB0
#
# WSL2 USB setup (one-time, Windows PowerShell admin):
#   usbipd list
#   usbipd attach --wsl --busid <ID>
# Then in WSL2:
#   ls /dev/ttyUSB*
#   sudo chmod 666 /dev/ttyUSB0   # or: sudo usermod -aG dialout $USER

set -euo pipefail

PORT="${1:-/dev/ttyUSB0}"

# Resolve runfile paths using the Bazel runfiles library.
# shellcheck source=/dev/null
RUNFILES_DIR="${RUNFILES_DIR:-$0.runfiles}"
source "${RUNFILES_DIR}/_main/external/bazel_tools/tools/bash/runfiles/runfiles.bash" 2>/dev/null \
    || source "$(dirname "$0")/../bazel_tools/tools/bash/runfiles/runfiles.bash"

ESPTOOL="$(rlocation _main/tools/firmware/esp32/esptool_wrapper)"
BIN="$(rlocation _main/demo/blink/blink.bin)"
BOOTLOADER="$(rlocation _main/external/arduino_esp32/tools/sdk/bin/bootloader_dio_80m.bin)"
PARTITIONS="$(rlocation _main/demo/blink/blink.partitions.bin)"

echo "Flashing to ${PORT}..."
python3 "$ESPTOOL" \
    --port "$PORT" \
    --chip esp32 \
    --baud 921600 \
    --before default_reset \
    --after hard_reset \
    write_flash \
    --flash_mode dio \
    --flash_freq 80m \
    --flash_size 4MB \
    0x1000  "$BOOTLOADER" \
    0x8000  "$PARTITIONS" \
    0x10000 "$BIN"

echo "Flash complete. Press RESET on the board."
echo "Monitor: screen ${PORT} 115200"
