#!/bin/bash
# Split Python runfiles into dependency and application layers for container images

set -euo pipefail

if [ "$#" -ne 6 ]; then
    echo "Usage: $0 <unstripped_tar> <deps_tar> <app_tar> <runfiles_dir> <python_version_pattern> <strip_script>" >&2
    exit 1
fi

UNSTRIPPED_TAR="$1"
DEPS_TAR="$2"
APP_TAR="$3"
RUNFILES_DIR="$4"
PYTHON_VERSION_PATTERN="$5"
STRIP_SCRIPT="$6"

TMPDIR=$(mktemp -d)
EXTRACT_DIR="$TMPDIR/extract"
DEPS_LAYER_DIR="$TMPDIR/deps_layer"
APP_LAYER_DIR="$TMPDIR/app_layer"

mkdir -p "$EXTRACT_DIR" "$DEPS_LAYER_DIR" "$APP_LAYER_DIR"

tar -xf "$UNSTRIPPED_TAR" -C "$EXTRACT_DIR"

# Trim the Python runtime to keep layers lean
"$STRIP_SCRIPT" "$EXTRACT_DIR/app" || true

# Start from the full app tree and peel dependencies outward
cp -a "$EXTRACT_DIR/app" "$APP_LAYER_DIR/"

move_matches() {
    local pattern="$1"
    shopt -s nullglob
    local sources=("$APP_LAYER_DIR/app/$RUNFILES_DIR"/$pattern)
    shopt -u nullglob

    if [ ${#sources[@]} -eq 0 ]; then
        return
    fi

    mkdir -p "$DEPS_LAYER_DIR/app/$RUNFILES_DIR"
    mv "${sources[@]}" "$DEPS_LAYER_DIR/app/$RUNFILES_DIR/"
}

move_matches "rules_pycross++lock_repos+pypi*"
move_matches "rules_python++python+python_${PYTHON_VERSION_PATTERN}_*"

if find "$DEPS_LAYER_DIR" -mindepth 1 -print -quit >/dev/null 2>&1; then
    tar -cf "$DEPS_TAR" -C "$DEPS_LAYER_DIR" .
else
    tar -cf "$DEPS_TAR" --files-from /dev/null
fi

tar -cf "$APP_TAR" -C "$APP_LAYER_DIR" .

rm -rf "$TMPDIR"
