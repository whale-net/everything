# Wrapper for platform-specific requirements
# This allows BUILD files to use requirement() without knowing which pip hub to use

def _get_pip_hub():
    """Determine which pip hub to use based on build platform"""
    # For now, default to dev dependencies for development builds
    # Container builds will use platform-specific dependencies via the oci.bzl system
    return "@pip_deps_dev//:requirements.bzl"

def requirement(name):
    """Get a requirement from the appropriate pip hub"""
    # Import the requirement function from the appropriate pip hub
    # This will be resolved at build time based on the platform
    return "@pip_deps_dev//pypi__" + name.replace("-", "_")

# Platform-specific requirement functions for explicit use
def requirement_amd64(name):
    """Get a requirement from the AMD64 pip hub"""
    return "@pip_deps_linux_amd64//pypi__" + name.replace("-", "_")

def requirement_arm64(name):
    """Get a requirement from the ARM64 pip hub"""
    return "@pip_deps_linux_arm64//pypi__" + name.replace("-", "_")

def requirement_dev(name):
    """Get a requirement from the dev pip hub"""
    return "@pip_deps_dev//pypi__" + name.replace("-", "_")