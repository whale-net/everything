"""
Root Tiltfile for Everything Monorepo

This is the main Tiltfile that orchestrates local development for the everything monorepo.
It provides flexible configuration to run individual apps or entire domains.

Usage:
  tilt up                    # Start all enabled services
  tilt up demo               # Start only demo apps
  tilt up manman             # Start only manman apps
  tilt down                  # Stop all services

Configuration:
  Use .env file to configure which services to enable/disable
  See .env.example for available options
"""

load('ext://namespace', 'namespace_create')
load('ext://dotenv', 'dotenv')

# Load environment variables
dotenv()

# Global configuration
enable_demo = os.getenv('TILT_ENABLE_DEMO', 'true').lower() == 'true'
enable_manman = os.getenv('TILT_ENABLE_MANMAN', 'false').lower() == 'true'

print("=" * 60)
print("Everything Monorepo - Local Development Environment")
print("=" * 60)
print("Enabled domains:")
print("  Demo apps:  {}".format("✓" if enable_demo else "✗"))
print("  Manman:     {}".format("✓" if enable_manman else "✗"))
print("=" * 60)

# Load domain-specific Tiltfiles
if enable_demo:
    print("\n📦 Loading demo apps...")
    load_dynamic('demo/Tiltfile')

if enable_manman:
    print("\n📦 Loading manman services...")
    # Manman already has its own Tiltfile
    load_dynamic('manman/Tiltfile')

print("\n✅ Tilt configuration loaded successfully")
print("Run 'tilt up' to start your development environment\n")
