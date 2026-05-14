#!/bin/bash
# Integration tests for the Go release_helper rewrite.
# Delegates to the shared test_cli_integration.sh with the Go binary.
set -euo pipefail

RUNFILES_DIR="${RUNFILES_DIR:-$0.runfiles}"

export RELEASE_HELPER_BIN="${RUNFILES_DIR}/_main/tools/release_helper_go/release_helper_go_/release_helper_go"
if [ ! -f "$RELEASE_HELPER_BIN" ]; then
    echo "ERROR: Cannot find Go release_helper binary: $RELEASE_HELPER_BIN" >&2
    exit 1
fi

exec "${RUNFILES_DIR}/_main/tools/release_helper/test_cli_integration.sh"
