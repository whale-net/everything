"""Build file for the Xtensa ESP-ELF GCC archive (@xtensa_esp_elf_linux64).

Binary prefix in the 15.x toolchain: xtensa-esp-elf-
Verify after extraction: ls bin/
"""

package(default_visibility = ["//visibility:public"])

exports_files(glob(["**"]))

filegroup(
    name = "all_files",
    srcs = glob([
        "bin/**",
        "xtensa-esp-elf/**",
        "libexec/**",
        "lib/gcc/**",
    ]),
)

filegroup(
    name = "gcc",
    srcs = ["bin/xtensa-esp-elf-gcc"],
)

filegroup(
    name = "g++",
    srcs = ["bin/xtensa-esp-elf-g++"],
)

filegroup(
    name = "ar",
    srcs = ["bin/xtensa-esp-elf-ar"],
)

filegroup(
    name = "ld",
    srcs = ["bin/xtensa-esp-elf-ld"],
)

filegroup(
    name = "objcopy",
    srcs = ["bin/xtensa-esp-elf-objcopy"],
)

filegroup(
    name = "strip",
    srcs = ["bin/xtensa-esp-elf-strip"],
)
