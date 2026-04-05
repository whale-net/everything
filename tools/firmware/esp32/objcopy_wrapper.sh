#!/bin/bash
# Xtensa ESP32 objcopy wrapper.

set -euo pipefail

XTENSA_BIN=("$(dirname "$0")/../../../external/"*xtensa_esp_elf_linux64/bin)
exec "${XTENSA_BIN[0]}/xtensa-esp32-elf-objcopy" "$@"
