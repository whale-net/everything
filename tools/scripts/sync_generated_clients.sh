#!/bin/bash
# Sync generated OpenAPI clients from Bazel build to local directory
# This allows local development with the generated clients (e.g., for IDE autocomplete)
#
# Discovers all openapi_client targets using Bazel query and syncs them to local directories.
# Supports both Python clients (generated/py/) and Go clients (generated/go/)

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

# Build all generated clients
echo -e "${YELLOW}Building generated clients...${NC}"
cd "$REPO_ROOT"

# Build everything under //generated/py and //generated/go
bazel build //generated/py/... //generated/go/...

echo
echo -e "${YELLOW}Syncing clients to local directory...${NC}"
echo

# Find all generated client directories in bazel-bin/generated/
# Look for directories that contain typical client structure
total_synced=0
python_synced=0
go_synced=0

# Sync Python clients from generated/py/
echo -e "${BLUE}Syncing Python clients...${NC}"

# Sync py/__init__.py if it exists
if [ -f "$REPO_ROOT/bazel-bin/generated/py/__init__.py" ]; then
    mkdir -p "$REPO_ROOT/generated/py"
    rm -f "$REPO_ROOT/generated/py/__init__.py"
    cp "$REPO_ROOT/bazel-bin/generated/py/__init__.py" "$REPO_ROOT/generated/py/"
    chmod u+w "$REPO_ROOT/generated/py/__init__.py"
    echo -e "${GREEN}✓${NC} Synced generated/py/__init__.py"
fi

# Find all namespace directories under generated/py/ in bazel-bin
for namespace_dir in "$REPO_ROOT/bazel-bin/generated/py"/*; do
    if [ ! -d "$namespace_dir" ]; then
        continue
    fi
    
    namespace=$(basename "$namespace_dir")
    
    # Skip if it's not a valid namespace directory
    if [[ "$namespace" == "_"* ]] || [[ "$namespace" == "."* ]]; then
        continue
    fi
    
    # Sync namespace __init__.py if it exists
    if [ -f "$namespace_dir/__init__.py" ]; then
        mkdir -p "$REPO_ROOT/generated/py/$namespace"
        rm -f "$REPO_ROOT/generated/py/$namespace/__init__.py"
        cp "$namespace_dir/__init__.py" "$REPO_ROOT/generated/py/$namespace/"
        chmod u+w "$REPO_ROOT/generated/py/$namespace/__init__.py"
        echo -e "${GREEN}✓${NC} Synced generated/py/$namespace/__init__.py"
    fi
    
    # Find all client directories in this namespace
    # Look for directories that have typical OpenAPI client structure
    for client_dir in "$namespace_dir"/*; do
        if [ ! -d "$client_dir" ]; then
            continue
        fi
        
        client_name=$(basename "$client_dir")
        
        # Skip if it's not a valid client directory (check for api or models subdirs)
        if [ ! -d "$client_dir/api" ] && [ ! -d "$client_dir/models" ]; then
            continue
        fi
        
        # This looks like a generated OpenAPI client - sync it
        dest_dir="$REPO_ROOT/generated/py/$namespace"
        mkdir -p "$dest_dir"
        
        # Remove old version if exists (make writable first)
        if [ -d "$dest_dir/$client_name" ]; then
            chmod -R u+w "$dest_dir/$client_name" 2>/dev/null || true
            rm -rf "$dest_dir/$client_name"
        fi
        
        # Copy new version
        cp -r "$client_dir" "$dest_dir/"
        chmod -R u+w "$dest_dir/$client_name"
        
        # Count files
        file_count=$(find "$dest_dir/$client_name" -type f -name "*.py" 2>/dev/null | wc -l)
        echo -e "${GREEN}✓${NC} Synced py/$namespace/$client_name ($file_count Python files)"
        ((python_synced++))
        ((total_synced++))
    done
done

# Sync Go clients from generated/go/
echo
echo -e "${BLUE}Syncing Go clients...${NC}"

# Find all namespace directories under generated/go/ in bazel-bin
for namespace_dir in "$REPO_ROOT/bazel-bin/generated/go"/*; do
    if [ ! -d "$namespace_dir" ]; then
        continue
    fi
    
    namespace=$(basename "$namespace_dir")
    
    # Skip if it's not a valid namespace directory
    if [[ "$namespace" == "_"* ]] || [[ "$namespace" == "."* ]]; then
        continue
    fi
    
    # Find all client directories in this namespace
    # Look for directories that have .go files
    for client_dir in "$namespace_dir"/*; do
        if [ ! -d "$client_dir" ]; then
            continue
        fi
        
        client_name=$(basename "$client_dir")
        
        # Check if it has .go files
        go_file_count=$(find "$client_dir" -maxdepth 1 -type f -name "*.go" 2>/dev/null | wc -l)
        if [ "$go_file_count" -eq 0 ]; then
            continue
        fi
        
        # This looks like a generated Go client - sync it
        dest_dir="$REPO_ROOT/generated/go/$namespace"
        mkdir -p "$dest_dir"
        
        # Remove old version if exists (make writable first)
        if [ -d "$dest_dir/$client_name" ]; then
            chmod -R u+w "$dest_dir/$client_name" 2>/dev/null || true
            rm -rf "$dest_dir/$client_name"
        fi
        
        # Copy new version
        cp -r "$client_dir" "$dest_dir/"
        chmod -R u+w "$dest_dir/$client_name"
        
        # Count files
        file_count=$(find "$dest_dir/$client_name" -type f -name "*.go" 2>/dev/null | wc -l)
        echo -e "${GREEN}✓${NC} Synced go/$namespace/$client_name ($file_count Go files)"
        ((go_synced++))
        ((total_synced++))
    done
done

echo
echo -e "${GREEN}Sync complete!${NC}"
echo
echo "Synced $total_synced client(s) to local directories:"
echo "  - Python: $python_synced client(s) under generated/py/"
echo "  - Go: $go_synced client(s) under generated/go/"
echo
echo "You can now import them in your IDE with autocomplete support."
echo "  Python: from generated.py.NAMESPACE.CLIENT_NAME import DefaultApi"
echo "  Go: import \"github.com/whale-net/everything/generated/go/NAMESPACE/CLIENT_NAME\""
echo

exit 0
