"""Generic cc_toolchain_config rule for embedded GCC cross-compilers.

Uses the modern Bazel action_config + tool(tool = <label>) API so board
toolchain binaries are referenced directly as Bazel labels — no wrapper
scripts or glob path hacks required.

cxx_builtin_include_directories are derived from gcc_binary + toolchain_triple
+ gcc_version at analysis time, eliminating the old "/" filesystem whitelist.

### Adding a new board

1.  Download the toolchain via http_archive in MODULE.bazel.
2.  Write a build_file for it (following xtensa_toolchain.BUILD as template):
      filegroup :all_files  — all toolchain files (staged in every sandbox)
      filegroup :gcc        — the C compiler binary
      filegroup :g++        — the C++ compiler binary
      filegroup :ar         — the archiver binary
      cc_library :cxx_runtime  — libgcc.a + libstdc++.a (or equivalent)
3.  In your board's BUILD.bazel:
      load("//tools/firmware:cc_toolchain_config.bzl", "cc_toolchain_config")
      _GCC_VERSION = "14.2.0"  # keep in sync with MODULE.bazel URL
      cc_toolchain_config(
          name = "...",
          gcc_binary       = "@your_toolchain//:gcc",
          gpp_binary       = "@your_toolchain//:g++",
          ar_binary        = "@your_toolchain//:ar",
          toolchain_triple = "arm-none-eabi",
          gcc_version      = _GCC_VERSION,
          toolchain_identifier = "arm-none-eabi",
          target_system_name   = "arm-none-eabi",
          target_cpu           = "armv6m",
          compile_flags_c   = ["-mcpu=cortex-m0plus", "-mthumb", "-Os", ...],
          compile_flags_cxx = ["-mcpu=cortex-m0plus", "-mthumb", "-fno-exceptions", ...],
          link_flags        = ["-nostdlib", "-Wl,--gc-sections", ...],
      )

### cxx_builtin_include_directories

Derived from the execroot-relative path of gcc_binary at analysis time:
  {toolchain_root}/lib/gcc/{triple}/{version}/include
  {toolchain_root}/lib/gcc/{triple}/{version}/include-fixed
  {toolchain_root}/{triple}/include
  {toolchain_root}/{triple}/sys-include

Bazel accepts execroot-relative paths here, so no absolute path or sysroot
magic is needed and the unstable bzlmod canonical repo name is never embedded.
"""

load(
    "@bazel_tools//tools/cpp:cc_toolchain_config_lib.bzl",
    "action_config",
    "feature",
    "flag_group",
    "flag_set",
    "tool",
    "variable_with_value",
)
load("@bazel_tools//tools/build_defs/cc:action_names.bzl", "ACTION_NAMES")

# ── Action sets ───────────────────────────────────────────────────────────────

_C_COMPILE_ACTIONS = [
    ACTION_NAMES.c_compile,
    ACTION_NAMES.assemble,
    ACTION_NAMES.preprocess_assemble,
]

_CXX_COMPILE_ACTIONS = [
    ACTION_NAMES.cpp_compile,
    ACTION_NAMES.cpp_header_parsing,
    ACTION_NAMES.cpp_module_compile,
    ACTION_NAMES.cpp_module_codegen,
    ACTION_NAMES.linkstamp_compile,
]

_ALL_COMPILE_ACTIONS = _C_COMPILE_ACTIONS + _CXX_COMPILE_ACTIONS

_LINK_ACTIONS = [
    ACTION_NAMES.cpp_link_executable,
    ACTION_NAMES.cpp_link_dynamic_library,
    ACTION_NAMES.cpp_link_nodeps_dynamic_library,
]

def _impl(ctx):
    # ── Derive toolchain include directories ──────────────────────────────────
    # ctx.file.gcc_binary.path is execroot-relative, e.g.:
    #   external/+_repo_rules2+xtensa_esp_elf_linux64/bin/xtensa-esp32-elf-gcc
    # Strip everything from "/bin/" onward to get the toolchain root, then
    # construct the standard GCC include directory paths.
    gcc_path = ctx.file.gcc_binary.path
    toolchain_root = gcc_path[:gcc_path.rfind("/bin/")]
    triple = ctx.attr.toolchain_triple
    version = ctx.attr.gcc_version

    cxx_builtin_include_directories = [
        toolchain_root + "/lib/gcc/" + triple + "/" + version + "/include",
        toolchain_root + "/lib/gcc/" + triple + "/" + version + "/include-fixed",
        toolchain_root + "/" + triple + "/include",
        toolchain_root + "/" + triple + "/sys-include",
    ]

    # ── Action configs (modern API: tool referenced by label, not path) ───────
    # action_config maps each Bazel action to its compiler/linker/archiver binary.
    # The File objects come from the rule attrs, so Bazel tracks them as proper
    # dependencies — no wrapper scripts or glob path discovery needed.
    gcc = ctx.file.gcc_binary
    gpp = ctx.file.gpp_binary
    ar  = ctx.file.ar_binary

    action_configs = [
        # C compilation
        action_config(action_name = ACTION_NAMES.c_compile,           enabled = True, tools = [tool(tool = gcc)]),
        action_config(action_name = ACTION_NAMES.assemble,            enabled = True, tools = [tool(tool = gcc)]),
        action_config(action_name = ACTION_NAMES.preprocess_assemble, enabled = True, tools = [tool(tool = gcc)]),
        # C++ compilation
        action_config(action_name = ACTION_NAMES.cpp_compile,         enabled = True, tools = [tool(tool = gpp)]),
        action_config(action_name = ACTION_NAMES.cpp_header_parsing,  enabled = True, tools = [tool(tool = gpp)]),
        action_config(action_name = ACTION_NAMES.cpp_module_compile,  enabled = True, tools = [tool(tool = gpp)]),
        action_config(action_name = ACTION_NAMES.cpp_module_codegen,  enabled = True, tools = [tool(tool = gpp)]),
        action_config(action_name = ACTION_NAMES.linkstamp_compile,   enabled = True, tools = [tool(tool = gpp)]),
        # Linking (GCC as driver — invokes ld internally)
        action_config(action_name = ACTION_NAMES.cpp_link_executable,              enabled = True, tools = [tool(tool = gcc)]),
        action_config(action_name = ACTION_NAMES.cpp_link_dynamic_library,         enabled = True, tools = [tool(tool = gcc)]),
        action_config(action_name = ACTION_NAMES.cpp_link_nodeps_dynamic_library,  enabled = True, tools = [tool(tool = gcc)]),
        # Archiving — implies archiver_flags so the feature is activated for this action
        action_config(action_name = ACTION_NAMES.cpp_link_static_library, enabled = True, tools = [tool(tool = ar)], implies = ["archiver_flags"]),
    ]

    # ── Features ──────────────────────────────────────────────────────────────

    # Board-specific compile flags.  -MMD and -c are appended by the rule.
    default_compile_flags_feature = feature(
        name = "default_compile_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _C_COMPILE_ACTIONS,
                flag_groups = [flag_group(flags = ctx.attr.compile_flags_c + ["-MMD", "-c"])],
            ),
            flag_set(
                actions = _CXX_COMPILE_ACTIONS,
                flag_groups = [flag_group(flags = ctx.attr.compile_flags_cxx + ["-MMD", "-c"])],
            ),
        ],
    )

    default_link_flags_feature = feature(
        name = "default_link_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _LINK_ACTIONS,
                flag_groups = [flag_group(flags = ctx.attr.link_flags)],
            ),
        ],
    )

    supports_pic_feature            = feature(name = "supports_pic",            enabled = False)
    supports_dynamic_linker_feature = feature(name = "supports_dynamic_linker", enabled = False)

    # ar flags for cpp_link_static_library.
    # With action_config, Bazel no longer auto-provides these — they must be
    # explicit.  rcsD: create archive, add with index, use deterministic mode.
    archiver_flags_feature = feature(
        name = "archiver_flags",
        flag_sets = [
            flag_set(
                actions = [ACTION_NAMES.cpp_link_static_library],
                flag_groups = [
                    flag_group(flags = ["rcsD"]),
                    flag_group(
                        flags = ["%{output_execpath}"],
                        expand_if_available = "output_execpath",
                    ),
                    flag_group(
                        iterate_over = "libraries_to_link",
                        flag_groups = [
                            flag_group(
                                flags = ["%{libraries_to_link.name}"],
                                expand_if_equal = variable_with_value(
                                    name = "libraries_to_link.type",
                                    value = "object_file",
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
                        ],
                        expand_if_available = "libraries_to_link",
                    ),
                ],
            ),
        ],
    )

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
                actions = _LINK_ACTIONS,
                flag_groups = [
                    flag_group(
                        flags = ["-o", "%{output_execpath}"],
                        expand_if_available = "output_execpath",
                    ),
                ],
            ),
        ],
    )

    # Wrap all libraries in --start-group/--end-group so embedded SDK archives
    # with circular dependencies link correctly regardless of order.
    libraries_to_link_feature = feature(
        name = "libraries_to_link",
        flag_sets = [
            flag_set(
                actions = _LINK_ACTIONS,
                flag_groups = [
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
                actions = _ALL_COMPILE_ACTIONS + _LINK_ACTIONS,
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

    # Note: source_file and output_compile_flags features are intentionally
    # omitted — Bazel's CppCompile action already appends "-c <src> -o <out>"
    # as a legacy default; adding them again would duplicate the source file.
    features = [
        default_compile_flags_feature,
        default_link_flags_feature,
        supports_pic_feature,
        supports_dynamic_linker_feature,
        archiver_flags_feature,
        dependency_file_feature,
        output_execpath_flags_feature,
        libraries_to_link_feature,
        user_compile_flags_feature,
        sysroot_feature,
        includes_feature,
        include_paths_feature,
    ]

    return cc_common.create_cc_toolchain_config_info(
        ctx = ctx,
        toolchain_identifier = ctx.attr.toolchain_identifier,
        host_system_name = "x86_64-unknown-linux-gnu",
        target_system_name = ctx.attr.target_system_name,
        target_cpu = ctx.attr.target_cpu,
        target_libc = ctx.attr.target_libc,
        compiler = "gcc",
        abi_version = ctx.attr.abi_version,
        abi_libc_version = ctx.attr.abi_libc_version,
        action_configs = action_configs,
        features = features,
        cxx_builtin_include_directories = cxx_builtin_include_directories,
    )

cc_toolchain_config = rule(
    implementation = _impl,
    attrs = {
        # ── Toolchain binaries (referenced by label — no wrapper scripts needed)
        "gcc_binary": attr.label(
            allow_single_file = True,
            mandatory = True,
            doc = "C compiler binary (e.g. @toolchain//:gcc). Also used as the " +
                  "linker driver and to derive cxx_builtin_include_directories.",
        ),
        "gpp_binary": attr.label(
            allow_single_file = True,
            mandatory = True,
            doc = "C++ compiler binary (e.g. @toolchain//:g++).",
        ),
        "ar_binary": attr.label(
            allow_single_file = True,
            mandatory = True,
            doc = "Archiver binary (e.g. @toolchain//:ar).",
        ),
        # ── Include directory derivation ──────────────────────────────────────
        "toolchain_triple": attr.string(
            mandatory = True,
            doc = "Target triple in the GCC installation layout, e.g. " +
                  "'xtensa-esp-elf' or 'arm-none-eabi'.",
        ),
        "gcc_version": attr.string(
            mandatory = True,
            doc = "GCC version string, e.g. '15.2.0'. Must match the version " +
                  "in the toolchain archive. Keep in sync with MODULE.bazel.",
        ),
        # ── Toolchain identity ────────────────────────────────────────────────
        "toolchain_identifier": attr.string(mandatory = True),
        "target_system_name":   attr.string(mandatory = True),
        "target_cpu":           attr.string(mandatory = True),
        "target_libc":          attr.string(default = "unknown"),
        "abi_version":          attr.string(default = "unknown"),
        "abi_libc_version":     attr.string(default = "unknown"),
        # ── Board-specific flags ──────────────────────────────────────────────
        # Architecture flags, optimization, language features.
        # Do NOT include -std=, -MMD, or -c; those are handled by the rule.
        "compile_flags_c":   attr.string_list(default = []),
        "compile_flags_cxx": attr.string_list(default = []),
        "link_flags":        attr.string_list(default = []),
    },
    provides = [CcToolchainConfigInfo],
)
