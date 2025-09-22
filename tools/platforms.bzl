# Platform definitions for cross-compilation support

def define_platforms():
    """Define custom platforms for cross-compilation support."""
    
    # Linux x86_64 platform for container builds - simplified to x86_64 only
    native.platform(
        name = "linux_x86_64",
        constraint_values = [
            "@platforms//os:linux",
            "@platforms//cpu:x86_64",
        ],
    )

    # macOS x86_64 platform for local development
    native.platform(
        name = "macos_x86_64",
        constraint_values = [
            "@platforms//os:macos",
            "@platforms//cpu:x86_64",
        ],
    )

    # Linux ARM64 platform for container builds
    native.platform(
        name = "linux_arm64",
        constraint_values = [
            "@platforms//os:linux",
            "@platforms//cpu:arm64",
        ],
    )

    # macOS ARM64 platform for local development
    native.platform(
        name = "macos_arm64",
        constraint_values = [
            "@platforms//os:macos",
            "@platforms//cpu:arm64",
        ],
    )