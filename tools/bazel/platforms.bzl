# Platform definitions for cross-compilation support
#
# NOTE: These platform definitions are available but not required for normal builds.
# The multiplatform image system uses oci_image_index which automatically handles
# platform selection, and pycross automatically selects the correct Python wheels
# for each platform.
#
# These platforms are provided for advanced use cases where explicit platform
# targeting is needed (e.g., in tests or for debugging specific platform builds).

def define_platforms():
    """Define custom platforms for cross-compilation support.
    
    These platforms are available but optional. The build system automatically
    handles multi-platform builds without requiring explicit platform flags.
    """
    
    # Linux AMD64 - primary container platform
    native.platform(
        name = "linux_x86_64",
        constraint_values = [
            "@platforms//os:linux",
            "@platforms//cpu:x86_64",
        ],
    )

    # Linux ARM64 - secondary container platform
    native.platform(
        name = "linux_arm64",
        constraint_values = [
            "@platforms//os:linux",
            "@platforms//cpu:arm64",
        ],
    )

    # macOS platforms for local development (optional)
    native.platform(
        name = "macos_x86_64",
        constraint_values = [
            "@platforms//os:macos",
            "@platforms//cpu:x86_64",
        ],
    )

    native.platform(
        name = "macos_arm64",
        constraint_values = [
            "@platforms//os:macos",
            "@platforms//cpu:arm64",
        ],
    )
