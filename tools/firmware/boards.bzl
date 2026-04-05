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

def firmware_board(name, cpu_constraint, os_constraint = "@platforms//os:none", board_constraint = None):
    """Declares a Bazel platform for a firmware board target.

    Args:
        name: Board identifier (e.g. "elegoo_esp32", "pico_w").
        cpu_constraint: CPU constraint_value target
            (e.g. "//tools/firmware:cpu_xtensa").
        os_constraint: OS constraint; defaults to bare-metal (no OS).
        board_constraint: Optional board identity constraint_value
            (e.g. "//tools/firmware:board_elegoo_esp32").  Required for
            board_pins_registry() select() to resolve to the correct pins target.
    """
    constraint_values = [os_constraint, cpu_constraint]
    if board_constraint != None:
        constraint_values.append(board_constraint)
    native.platform(
        name = name,
        constraint_values = constraint_values,
    )
