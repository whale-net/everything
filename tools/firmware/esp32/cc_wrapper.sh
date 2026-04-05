#!/bin/bash
# Xtensa ESP32 C compiler wrapper.
#
# Required because --incompatible_strict_action_env is set in .bazelrc.
# Strips host-specific flags that xtensa-esp-elf-gcc doesn't understand.
#
# Uses a glob to locate the toolchain so that bzlmod canonical repo names
# (e.g. +_repo_rules2+xtensa_esp_elf_linux64) are handled transparently.
# From the package dir (tools/firmware/esp32/), three levels up reaches the
# execroot root where external/ lives.

set -euo pipefail

XTENSA_BIN=("$(dirname "$0")/../../../external/"*xtensa_esp_elf_linux64/bin)
REAL_GCC="${XTENSA_BIN[0]}/xtensa-esp32-elf-gcc"

ARGS=()
for arg in "$@"; do
    case "$arg" in
        # x86-only math flags
        -mfpmath=*|-msse*|-msse2*|-msse3*|-mssse3*|-msse4*|-mavx*) ;;
        # x86 architecture / tuning flags
        -march=*|-mtune=*) ;;
        # Linux kernel hardening flags not supported by Xtensa GCC
        -fstack-clash-protection|-fcf-protection*) ;;
        # Unused spectre mitigation flags
        -mindirect-branch=*|-mfunction-return=*|-mharden-sls=*) ;;
        # Everything else passes through
        *) ARGS+=("$arg") ;;
    esac
done

exec "$REAL_GCC" "${ARGS[@]}"
