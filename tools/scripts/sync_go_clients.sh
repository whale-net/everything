#!/bin/bash
# Sync OpenAPI Go clients from Bazel build outputs to workspace
# This makes them available for IDE autocomplete and normal Bazel builds

set -e

WORKSPACE_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$WORKSPACE_ROOT"

echo "🔄 Syncing OpenAPI Go clients..."
echo

# Function to sync a client
sync_client() {
    local name="$1"
    local bazel_target="$2"
    local output_path="$3"
    local bazel_output_dir="$4"
    
    echo "📦 $name"
    
    # Build the tar
    bazel build "$bazel_target" --show_result=0
    
    # Create output directory
    mkdir -p "$output_path"
    
    # Extract from bazel-bin
    tar -xf "$bazel_output_dir" -C "$output_path"
    
    echo "  ✅ Synced to $output_path/"
    echo
}

# ManMan Experience API
sync_client \
    "ManMan Experience API" \
    "//generated/go/manman:experience_api_tar" \
    "generated/go/manman/experience_api" \
    "bazel-bin/generated/go/manman/experience-api.tar"

# Demo Hello FastAPI
sync_client \
    "Demo Hello FastAPI" \
    "//generated/go/demo:hello_fastapi_tar" \
    "generated/go/demo/hello_fastapi" \
    "bazel-bin/generated/go/demo/hello_fastapi.tar"

echo "✨ All Go clients synced to workspace!"
echo
echo "You can now use them with go_library:"
echo '  go_library('
echo '      name = "experience_api",'
echo '      srcs = glob(["experience_api/*.go"], exclude=["experience_api/*_test.go"]),'
echo '      importpath = "github.com/whale-net/everything/generated/go/manman/experience_api",'
echo '  )'
