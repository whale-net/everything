#!/bin/bash
# Xtensa ESP32 C++ compiler wrapper. Mirrors cc_wrapper.sh.

set -euo pipefail

XTENSA_BIN=("$(dirname "$0")/../../../external/"*xtensa_esp_elf_linux64/bin)
REAL_GPP="${XTENSA_BIN[0]}/xtensa-esp32-elf-g++"

ARGS=()
for arg in "$@"; do
    case "$arg" in
        -mfpmath=*|-msse*|-msse2*|-msse3*|-mssse3*|-msse4*|-mavx*) ;;
        -march=*|-mtune=*) ;;
        -fstack-clash-protection|-fcf-protection*) ;;
        -mindirect-branch=*|-mfunction-return=*|-mharden-sls=*) ;;
        *) ARGS+=("$arg") ;;
    esac
done

exec "$REAL_GPP" "${ARGS[@]}"
