"""Board declaration macro.

Adding a new board = calling firmware_board() once in its BUILD.bazel.

Example:
    # tools/firmware/pico/BUILD.bazel
    load("//tools/firmware:boards.bzl", "firmware_board")
    firmware_board(
        name = "pico_w",
        cpu_constraint = "//tools/firmware:cpu_armv6m",
    )
"""

def firmware_board(name, cpu_constraint, os_constraint = "@platforms//os:none"):
    """Declares a Bazel platform for a firmware board target.

    Args:
        name: Board identifier (e.g. "elegoo_esp32", "pico_w").
        cpu_constraint: CPU constraint_value target
            (e.g. "//tools/firmware:cpu_xtensa").
        os_constraint: OS constraint; defaults to bare-metal (no OS).
    """
    native.platform(
        name = name,
        constraint_values = [
            os_constraint,
            cpu_constraint,
        ],
    )
