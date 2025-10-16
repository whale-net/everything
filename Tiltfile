# Everything Monorepo - Root Tiltfile
# 
# This is a minimal root Tiltfile that provides shared utilities.
# Individual domains should have their own Tiltfile that can be run standalone.
#
# Usage:
#   - For ManMan development: cd manman && tilt up
#   - For other domains: cd <domain> && tilt up
#
# This root Tiltfile is intentionally minimal - domains are self-contained.

load('ext://dotenv', 'dotenv')

# Load environment variables
dotenv()

print("� Everything Monorepo - Root Tiltfile")
print("")
print("This is a minimal root configuration.")
print("Individual domains have their own Tiltfiles:")
print("")
print("  📦 ManMan:  cd manman && tilt up")
print("  📦 FCM:     cd friendly_computing_machine && tilt up")
print("")
print("Each domain Tiltfile manages its own:")
print("  - Dependencies (postgres, rabbitmq, etc.)")
print("  - Bazel image builds")
print("  - Helm chart deployments")
print("  - Port forwarding and access")
print("")
print("💡 Navigate to a domain directory and run 'tilt up' to start.")
