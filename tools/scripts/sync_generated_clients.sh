#!/bin/bash
# Sync generated OpenAPI clients from Bazel build to local directory
# This allows local development with the generated clients (e.g., for IDE autocomplete)
#
# Discovers all openapi_client targets using Bazel query and syncs them to local directories.

# Don't exit on error - we want to sync as many clients as possible
set +e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${BLUE}Syncing Generated OpenAPI Clients${NC}"
echo "=================================="
echo

# Discover all openapi_client targets using Bazel query
echo -e "${YELLOW}Discovering OpenAPI client targets...${NC}"
cd "$REPO_ROOT"

# Query for all targets with "openapi_client_rule" in their kind
# This finds targets like //generated/manman:experience_api
CLIENT_TARGETS=$(bazel query 'kind("openapi_client_rule", //generated/...)' 2>/dev/null || echo "")

if [ -z "$CLIENT_TARGETS" ]; then
    echo -e "${YELLOW}⚠${NC}  No OpenAPI client targets found"
    exit 0
fi

# Build all discovered clients
echo -e "${YELLOW}Building OpenAPI clients...${NC}"
bazel build //generated/...

echo
echo -e "${YELLOW}Syncing clients to local directory...${NC}"
echo

# Sync top-level __init__.py if it exists
if [ -f "$REPO_ROOT/bazel-bin/generated/__init__.py" ]; then
    rm -f "$REPO_ROOT/generated/__init__.py"
    cp "$REPO_ROOT/bazel-bin/generated/__init__.py" "$REPO_ROOT/generated/"
    chmod u+w "$REPO_ROOT/generated/__init__.py"
    echo -e "${GREEN}✓${NC} Synced generated/__init__.py"
fi

# Process each client target
total_synced=0
for target in $CLIENT_TARGETS; do
    # Extract package and target name from //generated/namespace:target
    package=$(echo "$target" | sed 's|//||' | sed 's|:.*||')
    client_name=$(echo "$target" | sed 's|.*:||')
    
    # Source directory (Bazel build output)
    BAZEL_BIN_DIR="$REPO_ROOT/bazel-bin/$package"
    SOURCE_CLIENT="$BAZEL_BIN_DIR/$client_name"
    
    # Destination directory
    DEST_DIR="$REPO_ROOT/$package"
    
    # Create destination directory if needed
    mkdir -p "$DEST_DIR"
    
    # Sync namespace __init__.py if it exists and hasn't been synced yet
    NAMESPACE_INIT="$BAZEL_BIN_DIR/__init__.py"
    if [ -f "$NAMESPACE_INIT" ] && [ ! -f "$DEST_DIR/__init__.py" ]; then
        rm -f "$DEST_DIR/__init__.py"
        cp "$NAMESPACE_INIT" "$DEST_DIR/"
        chmod u+w "$DEST_DIR/__init__.py"
        namespace=$(basename "$package")
        echo -e "${GREEN}✓${NC} Synced generated/$namespace/__init__.py"
    fi
    
    # Sync the client
    if [ -d "$SOURCE_CLIENT" ]; then
        # Remove old version if exists (make writable first)
        if [ -d "$DEST_DIR/$client_name" ]; then
            chmod -R u+w "$DEST_DIR/$client_name" 2>/dev/null || true
            rm -rf "$DEST_DIR/$client_name"
        fi
        
        # Copy new version
        cp -r "$SOURCE_CLIENT" "$DEST_DIR/"
        chmod -R u+w "$DEST_DIR/$client_name"
        
        # Count files
        file_count=$(find "$DEST_DIR/$client_name" -type f -name "*.py" 2>/dev/null | wc -l)
        echo -e "${GREEN}✓${NC} Synced $client_name ($file_count Python files)"
        ((total_synced++))
    else
        echo -e "${YELLOW}⚠${NC}  Skipped $client_name (not found at $SOURCE_CLIENT)"
    fi
done

echo
echo -e "${GREEN}Sync complete!${NC}"
echo
echo "Synced $total_synced client(s) to local directories under generated/"
echo
echo "You can now import them in your IDE with autocomplete support."
echo "Example: from generated.NAMESPACE.CLIENT_NAME import DefaultApi"
echo

exit 0
