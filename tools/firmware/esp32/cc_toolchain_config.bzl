"""Xtensa ESP32 cc_toolchain_config rule.

Targets the Espressif crosstool-NG GCC 15.2 toolchain
(xtensa-esp-elf-*).  Compile / link flags derived from the Arduino
ESP32 1.0.6 platform.txt.

References:
  - github.com/simonhorlick/bazel_esp32 (flag lists + structure)
  - Bazel cc_toolchain_config API documentation
"""

load(
    "@bazel_tools//tools/cpp:cc_toolchain_config_lib.bzl",
    "action_config",
    "feature",
    "flag_group",
    "flag_set",
    "tool",
    "tool_path",
    "variable_with_value",
    "with_feature_set",
)
load("@bazel_tools//tools/build_defs/cc:action_names.bzl", "ACTION_NAMES")

# ── Toolchain binary paths ───────────────────────────────────────────────────
# Tool paths are relative to the cc_toolchain package (tools/firmware/esp32/).
# We use shell wrappers rather than direct binaries because:
#   1. bzlmod canonical repo names (e.g. +_repo_rules2+xtensa_esp_elf_linux64)
#      can't be referenced by a stable relative path from here.
#   2. The gcc/g++ wrappers also strip x86-only flags that Xtensa GCC rejects.
# The wrappers use a glob (external/*xtensa_esp_elf_linux64/bin) to locate
# the toolchain regardless of the bzlmod canonical prefix.

_GCC     = "cc_wrapper.sh"
_GPP     = "gpp_wrapper.sh"
_AR      = "ar_wrapper.sh"
_LD      = "ld_wrapper.sh"
_OBJCOPY = "objcopy_wrapper.sh"
_STRIP   = "/bin/false"
_CPP     = "cc_wrapper.sh"   # gcc -E handles preprocessing
_OBJDUMP = "/bin/false"
_NM      = "/bin/false"

# ── Compiler / linker flags from Arduino 1.0.6 platform.txt ────────────────

_COMPILE_FLAGS_C = [
    "-mlongcalls",
    "-ffunction-sections",
    "-fdata-sections",
    "-fstrict-volatile-bitfields",
    "-Os",
    # No -std= here: Arduino core sets -std=gnu11 in its own copts.
    "-MMD",
    "-c",
]

_COMPILE_FLAGS_CXX = [
    "-mlongcalls",
    "-ffunction-sections",
    "-fdata-sections",
    "-fstrict-volatile-bitfields",
    "-fno-exceptions",
    "-fno-rtti",
    "-Os",
    # No -std= here: Arduino core sets -std=gnu++11 in its own copts, and
    # Pigweed requires C++17. The toolchain default (GCC 15 = gnu++17) is used
    # when no explicit standard is specified by the target.
    "-MMD",
    "-c",
]

_LINK_FLAGS = [
    "-nostdlib",
    "-Wl,--gc-sections",
    "-Wl,-static",
    "-mlongcalls",
    "-Wl,-EL",
]

# ── Built-in include directories ─────────────────────────────────────────────

_CXX_BUILTIN_INCLUDE_DIRECTORIES = [
    # Whitelist the entire filesystem so Bazel doesn't reject implicit headers
    # pulled in by xtensa-esp-elf-gcc or the Arduino ESP32 core.  The toolchain
    # is hermetic (sha256-pinned http_archive), so this doesn't compromise
    # reproducibility.  A tighter list would require embedding the bzlmod
    # canonical repo name, which is not stable across MODULE.bazel changes.
    "/",
]

# ── Action sets (all C/C++ compile + link actions) ───────────────────────────

_ALL_COMPILE_ACTIONS = [
    ACTION_NAMES.c_compile,
    ACTION_NAMES.cpp_compile,
    ACTION_NAMES.linkstamp_compile,
    ACTION_NAMES.assemble,
    ACTION_NAMES.preprocess_assemble,
    ACTION_NAMES.cpp_header_parsing,
    ACTION_NAMES.cpp_module_compile,
    ACTION_NAMES.cpp_module_codegen,
    ACTION_NAMES.clif_match,
    ACTION_NAMES.lto_backend,
]

_ALL_LINK_ACTIONS = [
    ACTION_NAMES.cpp_link_executable,
    ACTION_NAMES.cpp_link_dynamic_library,
    ACTION_NAMES.cpp_link_nodeps_dynamic_library,
]

def _impl(ctx):
    # ── Tool paths ──────────────────────────────────────────────────────────
    tool_paths = [
        tool_path(name = "gcc",     path = _GCC),
        tool_path(name = "g++",     path = _GPP),
        tool_path(name = "cpp",     path = _CPP),
        tool_path(name = "ar",      path = _AR),
        tool_path(name = "ld",      path = _LD),
        tool_path(name = "objcopy", path = _OBJCOPY),
        tool_path(name = "strip",   path = _STRIP),
        tool_path(name = "objdump", path = _OBJDUMP),
        tool_path(name = "nm",      path = _NM),
        # gcov not used for embedded, but the field must be present
        tool_path(name = "gcov",    path = "/bin/false"),
        tool_path(name = "dwp",     path = "/bin/false"),
        tool_path(name = "llvm-profdata", path = "/bin/false"),
    ]

    # ── Features ────────────────────────────────────────────────────────────

    default_compile_flags_feature = feature(
        name = "default_compile_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = [ACTION_NAMES.c_compile],
                flag_groups = [flag_group(flags = _COMPILE_FLAGS_C)],
            ),
            flag_set(
                actions = [
                    ACTION_NAMES.cpp_compile,
                    ACTION_NAMES.cpp_header_parsing,
                    ACTION_NAMES.cpp_module_compile,
                    ACTION_NAMES.cpp_module_codegen,
                ],
                flag_groups = [flag_group(flags = _COMPILE_FLAGS_CXX)],
            ),
        ],
    )

    default_link_flags_feature = feature(
        name = "default_link_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_LINK_ACTIONS,
                flag_groups = [flag_group(flags = _LINK_FLAGS)],
            ),
        ],
    )

    # Supports #include <header> with absolute paths in the sandbox
    supports_pic_feature = feature(name = "supports_pic", enabled = False)
    supports_dynamic_linker_feature = feature(name = "supports_dynamic_linker", enabled = False)

    # Required by Bazel's C++ rules to emit dependency files
    dependency_file_feature = feature(
        name = "dependency_file",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_COMPILE_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["-MD", "-MF", "%{dependency_file}"],
                        expand_if_available = "dependency_file",
                    ),
                ],
            ),
        ],
    )

    output_execpath_flags_feature = feature(
        name = "output_execpath_flags",
        flag_sets = [
            flag_set(
                actions = _ALL_LINK_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["-o", "%{output_execpath}"],
                        expand_if_available = "output_execpath",
                    ),
                ],
            ),
        ],
    )

    libraries_to_link_feature = feature(
        name = "libraries_to_link",
        flag_sets = [
            flag_set(
                actions = _ALL_LINK_ACTIONS,
                flag_groups = [
                    # Wrap all libraries in --start-group/--end-group so the ESP32
                    # Arduino core, user code, and SDK libs can be linked regardless
                    # of order. Arduino's platform.txt does the same thing; the SDK
                    # has circular deps and the core references SDK symbols.
                    flag_group(
                        flag_groups = [
                            flag_group(flags = ["-Wl,--start-group"]),
                            flag_group(
                                iterate_over = "libraries_to_link",
                                flag_groups = [
                                    flag_group(
                                        flags = ["-Wl,--start-lib"],
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "object_file_group",
                                        ),
                                    ),
                                    flag_group(
                                        flags = ["%{libraries_to_link.object_files}"],
                                        iterate_over = "libraries_to_link.object_files",
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "object_file_group",
                                        ),
                                    ),
                                    flag_group(
                                        flags = ["-Wl,--end-lib"],
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "object_file_group",
                                        ),
                                    ),
                                    flag_group(
                                        flags = ["%{libraries_to_link.name}"],
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "object_file",
                                        ),
                                    ),
                                    flag_group(
                                        flags = ["%{libraries_to_link.name}"],
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "interface_library",
                                        ),
                                    ),
                                    flag_group(
                                        flags = ["%{libraries_to_link.name}"],
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "static_library",
                                        ),
                                    ),
                                    flag_group(
                                        flags = ["-l%{libraries_to_link.name}"],
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "dynamic_library",
                                        ),
                                    ),
                                    flag_group(
                                        flags = ["-l:%{libraries_to_link.name}"],
                                        expand_if_equal = variable_with_value(
                                            name = "libraries_to_link.type",
                                            value = "versioned_dynamic_library",
                                        ),
                                    ),
                                ],
                                expand_if_available = "libraries_to_link",
                            ),
                            flag_group(flags = ["-Wl,--end-group"]),
                        ],
                        expand_if_available = "libraries_to_link",
                    ),
                ],
            ),
        ],
    )

    user_compile_flags_feature = feature(
        name = "user_compile_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_COMPILE_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["%{user_compile_flags}"],
                        iterate_over = "user_compile_flags",
                        expand_if_available = "user_compile_flags",
                    ),
                ],
            ),
        ],
    )

    sysroot_feature = feature(
        name = "sysroot",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_COMPILE_ACTIONS + _ALL_LINK_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["--sysroot=%{sysroot}"],
                        expand_if_available = "sysroot",
                    ),
                ],
            ),
        ],
    )

    includes_feature = feature(
        name = "includes",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_COMPILE_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["-include", "%{includes}"],
                        iterate_over = "includes",
                        expand_if_available = "includes",
                    ),
                ],
            ),
        ],
    )

    include_paths_feature = feature(
        name = "include_paths",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_COMPILE_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["-iquote", "%{quote_include_paths}"],
                        iterate_over = "quote_include_paths",
                        expand_if_available = "quote_include_paths",
                    ),
                    flag_group(
                        flags = ["-I%{include_paths}"],
                        iterate_over = "include_paths",
                        expand_if_available = "include_paths",
                    ),
                    flag_group(
                        flags = ["-isystem", "%{system_include_paths}"],
                        iterate_over = "system_include_paths",
                        expand_if_available = "system_include_paths",
                    ),
                ],
            ),
        ],
    )

    source_file_feature = feature(
        name = "source_file",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_COMPILE_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["%{source_file}"],
                        expand_if_available = "source_file",
                    ),
                ],
            ),
        ],
    )

    output_compile_flags_feature = feature(
        name = "output_compile_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_COMPILE_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["-o", "%{output_file}"],
                        expand_if_available = "output_file",
                    ),
                ],
            ),
        ],
    )

    features = [
        default_compile_flags_feature,
        default_link_flags_feature,
        supports_pic_feature,
        supports_dynamic_linker_feature,
        dependency_file_feature,
        output_execpath_flags_feature,
        libraries_to_link_feature,
        user_compile_flags_feature,
        sysroot_feature,
        includes_feature,
        include_paths_feature,
        # source_file_feature and output_compile_flags_feature are intentionally
        # omitted: Bazel's CppCompile action already appends "-c <src> -o <out>"
        # as a legacy default; adding them again via features would duplicate the
        # source file on the command line, causing gcc to reject the invocation.
    ]

    return cc_common.create_cc_toolchain_config_info(
        ctx = ctx,
        toolchain_identifier = "xtensa-esp32-elf",
        host_system_name = "x86_64-unknown-linux-gnu",
        target_system_name = "xtensa-esp32-elf",
        target_cpu = "xtensa",
        target_libc = "unknown",
        compiler = "gcc",
        abi_version = "unknown",
        abi_libc_version = "unknown",
        tool_paths = tool_paths,
        features = features,
        cxx_builtin_include_directories = _CXX_BUILTIN_INCLUDE_DIRECTORIES,
    )

cc_toolchain_config = rule(
    implementation = _impl,
    attrs = {},
    provides = [CcToolchainConfigInfo],
)
