#!/bin/bash
# Sync OpenAPI Go clients from Bazel build outputs to workspace
# This makes them available for IDE autocomplete and type checking
#
# Note: This is OPTIONAL for local development only.
# Bazel builds work fine without syncing - files are generated on-demand.
# This script just copies the generated files to the workspace for IDE support.

set -e

WORKSPACE_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$WORKSPACE_ROOT"

echo "🔄 Syncing OpenAPI Go clients to workspace for IDE support..."
echo "   (Not required for Bazel builds - they generate on-demand)"
echo

# Function to sync a client from bazel-bin tree artifact
sync_client() {
    local name="$1"
    local bazel_target="$2"      # e.g., "//generated/go/manman:experience_api_srcs"
    local package_name="$3"       # e.g., "experience_api"
    local output_path="$4"        # e.g., "generated/go/manman/experience_api"
    
    echo "📦 $name"
    
    # Build the _srcs target which generates the tree artifact
    bazel build "$bazel_target" --show_result=0
    
    # The tree artifact is the directory itself in bazel-bin
    # Format: bazel-bin/{output_path}/
    local src_dir="bazel-bin/${output_path}"
    
    if [ ! -d "$src_dir" ]; then
        echo "  ❌ ERROR: Generated files not found at $src_dir"
        return 1
    fi
    
    # Create output directory in workspace
    mkdir -p "$output_path"
    
    # Remove existing files (they may be read-only from previous Bazel builds)
    rm -f "$output_path"/*.go 2>/dev/null || true
    
    # Copy generated .go files
    cp "$src_dir"/*.go "$output_path/" 2>/dev/null || {
        echo "  ❌ ERROR: No .go files found in $src_dir"
        return 1
    }
    
    # Make files writable (Bazel outputs are read-only)
    chmod +w "$output_path"/*.go 2>/dev/null || true
    
    # Create a minimal go.mod for IDE support if it doesn't exist
    if [ ! -f "$output_path/go.mod" ]; then
        cat > "$output_path/go.mod" <<EOF
module github.com/whale-net/everything/$output_path

go 1.23

require (
	github.com/google/uuid v1.6.0
	golang.org/x/oauth2 v0.23.0
)
EOF
    fi
    
    # Count files
    local file_count=$(ls -1 "$output_path"/*.go 2>/dev/null | wc -l)
    echo "  ✅ Synced $file_count files to $output_path/"
    echo
}

# ManMan Experience API
sync_client \
    "ManMan Experience API" \
    "//generated/go/manman:experience_api_srcs" \
    "experience_api" \
    "generated/go/manman/experience_api"

# Demo Hello FastAPI
sync_client \
    "Demo Hello FastAPI" \
    "//generated/go/demo:hello_fastapi_srcs" \
    "hello_fastapi" \
    "generated/go/demo/hello_fastapi"

echo "✨ All Go clients synced to workspace!"
echo
echo "💡 Your IDE should now have full autocomplete and type checking."
echo "   Remember: These files are gitignored and only for local dev."
echo "   Bazel builds generate them automatically - no sync needed."
echo
echo "📝 What was synced:"
echo "   - Generated .go files copied from bazel-bin/"
echo "   - go.mod files created for each package"
echo "   - go.work updated automatically (if it exists)"
