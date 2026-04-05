#!/bin/bash
# Shared wrapper for all xtensa-esp32-elf-* tools.
#
# Usage: xtensa_wrapper.sh <tool> [--filter-host-flags] [args...]
#
# Uses a glob to locate the toolchain so that bzlmod canonical repo names
# (e.g. +_repo_rules2+xtensa_esp_elf_linux64) are handled transparently.
# From the package dir (tools/firmware/esp32/), three levels up reaches the
# execroot where external/ lives.
#
# --filter-host-flags: strips x86-only flags that Xtensa GCC doesn't understand.
# Pass this for gcc/g++ wrappers; omit for ar/ld/objcopy.

set -euo pipefail

TOOL="$1"; shift

FILTER=0
if [[ "${1:-}" == "--filter-host-flags" ]]; then
    FILTER=1
    shift
fi

XTENSA_BIN=("$(dirname "$0")/../../../external/"*xtensa_esp_elf_linux64/bin)
REAL_TOOL="${XTENSA_BIN[0]}/$TOOL"

if [[ "$FILTER" -eq 1 ]]; then
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
            *) ARGS+=("$arg") ;;
        esac
    done
    exec "$REAL_TOOL" "${ARGS[@]}"
else
    exec "$REAL_TOOL" "$@"
fi
