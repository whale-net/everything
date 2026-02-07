#!/bin/bash
# Build all required Docker images for ManManV2 local development
#
# Usage: ./scripts/build-images.sh [OPTIONS]
#
# Options:
#   --platform=PLATFORM   Target platform (linux/amd64 or linux/arm64)
#                        Default: auto-detected
#   --skip-test-server   Skip building test game server
#   --help               Show this help message

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
PLATFORM=""
SKIP_TEST_SERVER=false

# Parse arguments
for arg in "$@"; do
  case $arg in
    --platform=*)
      PLATFORM="${arg#*=}"
      shift
      ;;
    --skip-test-server)
      SKIP_TEST_SERVER=true
      shift
      ;;
    --help)
      head -n 10 "$0" | tail -n +2 | sed 's/^# //'
      exit 0
      ;;
    *)
      echo -e "${RED}Unknown option: $arg${NC}"
      echo "Run with --help for usage information"
      exit 1
      ;;
  esac
done

# Detect platform if not specified
if [ -z "$PLATFORM" ]; then
  ARCH=$(uname -m)
  if [ "$ARCH" = "arm64" ] || [ "$ARCH" = "aarch64" ]; then
    PLATFORM="linux/arm64"
    BAZEL_PLATFORM="//tools:linux_arm64"
  else
    PLATFORM="linux/amd64"
    BAZEL_PLATFORM="//tools:linux_x86_64"
  fi
  echo -e "${BLUE}Auto-detected platform: $PLATFORM${NC}"
else
  case $PLATFORM in
    linux/amd64)
      BAZEL_PLATFORM="//tools:linux_x86_64"
      ;;
    linux/arm64)
      BAZEL_PLATFORM="//tools:linux_arm64"
      ;;
    *)
      echo -e "${RED}Invalid platform: $PLATFORM${NC}"
      echo "Supported platforms: linux/amd64, linux/arm64"
      exit 1
      ;;
  esac
fi

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  ManManV2 Image Builder${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${BLUE}Platform:${NC} $PLATFORM"
echo -e "${BLUE}Bazel Platform:${NC} $BAZEL_PLATFORM"
echo ""

# Change to repo root (script is in manman-v2/scripts/)
cd "$(dirname "$0")/../.."

# Build function with error handling
build_image() {
  local name=$1
  local target=$2

  echo -e "${YELLOW}Building $name...${NC}"
  if bazel run "$target" --platforms="$BAZEL_PLATFORM"; then
    echo -e "${GREEN}✓ $name built successfully${NC}"
    return 0
  else
    echo -e "${RED}✗ Failed to build $name${NC}"
    return 1
  fi
}

# Track failures
FAILED_BUILDS=()

# Build wrapper image (required for host manager)
echo ""
echo -e "${BLUE}━━━ Building Wrapper Image ━━━${NC}"
if ! build_image "manmanv2-wrapper" "//manman/wrapper:manmanv2-wrapper_image_load"; then
  FAILED_BUILDS+=("wrapper")
fi

# Build API image (for control plane)
echo ""
echo -e "${BLUE}━━━ Building API Image ━━━${NC}"
if ! build_image "manmanv2-api" "//manman/api:manmanv2-api_image_load"; then
  FAILED_BUILDS+=("api")
fi

# Build processor image (for control plane)
echo ""
echo -e "${BLUE}━━━ Building Processor Image ━━━${NC}"
if ! build_image "manmanv2-processor" "//manman/processor:manmanv2-processor_image_load"; then
  FAILED_BUILDS+=("processor")
fi

# Build test game server (optional)
if [ "$SKIP_TEST_SERVER" = false ]; then
  echo ""
  echo -e "${BLUE}━━━ Building Test Game Server ━━━${NC}"
  if docker build \
    -t manmanv2-test-game-server:latest \
    -f manman/wrapper/testdata/Dockerfile \
    manman/wrapper/testdata/; then
    echo -e "${GREEN}✓ Test game server built successfully${NC}"
  else
    echo -e "${RED}✗ Failed to build test game server${NC}"
    FAILED_BUILDS+=("test-game-server")
  fi
else
  echo ""
  echo -e "${YELLOW}Skipping test game server (--skip-test-server)${NC}"
fi

# Summary
echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Build Summary${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════${NC}"

if [ ${#FAILED_BUILDS[@]} -eq 0 ]; then
  echo -e "${GREEN}All images built successfully!${NC}"
  echo ""
  echo -e "${BLUE}Verify images:${NC}"
  echo "  docker images | grep manmanv2"
  echo ""
  echo -e "${BLUE}Next steps:${NC}"
  echo "  1. tilt up                        # Start control plane"
  echo "  2. bazel run //manman/host:host   # Run host manager"
  exit 0
else
  echo -e "${RED}Failed to build: ${FAILED_BUILDS[*]}${NC}"
  echo ""
  echo -e "${YELLOW}Troubleshooting:${NC}"
  echo "  - Run 'bazel clean' and try again"
  echo "  - Check Bazel logs with --verbose_failures flag"
  echo "  - Ensure Docker daemon is running"
  exit 1
fi
