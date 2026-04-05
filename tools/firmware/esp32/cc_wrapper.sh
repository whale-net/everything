#!/bin/bash
exec "$(dirname "$0")/xtensa_wrapper.sh" xtensa-esp32-elf-gcc --filter-host-flags "$@"
