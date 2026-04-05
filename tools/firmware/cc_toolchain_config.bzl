"""Generic cc_toolchain_config rule for embedded GCC cross-compilers.

Parameterized so each board directory provides its own flags and identity
without duplicating the feature machinery. The rule derives
cxx_builtin_include_directories from gcc_tool + toolchain_triple + gcc_version,
replacing the previous "whitelist the entire filesystem" hack.

### Adding a new board

1.  Download the toolchain via http_archive in MODULE.bazel.
2.  Write a build file for it (following xtensa_toolchain.BUILD as a template):
      - filegroup :all_files
      - filegroup :gcc  ← this is what gcc_tool points to
      - cc_library :cxx_runtime (libgcc.a + libstdc++.a or equivalent)
3.  In your board's BUILD.bazel:
      load("//tools/firmware:cc_toolchain_config.bzl", "cc_toolchain_config")
      cc_toolchain_config(
          name = "..._toolchain_config",
          gcc_tool          = "@your_toolchain//:gcc",
          toolchain_triple  = "arm-none-eabi",       # must match lib/gcc/<triple>/
          gcc_version       = "14.2.0",              # keep in sync with MODULE.bazel URL
          toolchain_identifier  = "arm-none-eabi",
          target_system_name    = "arm-none-eabi",
          target_cpu            = "armv6m",
          compile_flags_c   = ["-mcpu=cortex-m0plus", "-mthumb", "-Os", ...],
          compile_flags_cxx = ["-mcpu=cortex-m0plus", "-mthumb", "-fno-exceptions", ...],
          link_flags        = ["-nostdlib", "-Wl,--gc-sections", "-mcpu=cortex-m0plus", ...],
      )
4.  Create the same wrapper-script convention the ESP32 uses:
      cc_wrapper.sh / gpp_wrapper.sh / ar_wrapper.sh / ld_wrapper.sh / objcopy_wrapper.sh
    Each is a one-liner that delegates to your board's xtensa_wrapper.sh equivalent.

### Wrapper script convention

tool_path entries use paths relative to the cc_toolchain target's package, so
"cc_wrapper.sh" resolves to <board-dir>/cc_wrapper.sh regardless of where this
.bzl file lives.  All boards must provide files with these exact names.

### cxx_builtin_include_directories

The rule whitelists only the three standard GCC installation directories:
  lib/gcc/<triple>/<version>/include
  lib/gcc/<triple>/<version>/include-fixed
  <triple>/include

This is derived from the execroot-relative path of gcc_tool at analysis time.
Bazel accepts execroot-relative paths in cxx_builtin_include_directories, so
no absolute path or sysroot tricks are needed.
"""

load(
    "@bazel_tools//tools/cpp:cc_toolchain_config_lib.bzl",
    "feature",
    "flag_group",
    "flag_set",
    "tool_path",
    "variable_with_value",
)
load("@bazel_tools//tools/build_defs/cc:action_names.bzl", "ACTION_NAMES")

# ── Wrapper script names (relative to the cc_toolchain target's package) ─────
# Each board directory must provide files with exactly these names.
_GCC     = "cc_wrapper.sh"
_GPP     = "gpp_wrapper.sh"
_AR      = "ar_wrapper.sh"
_LD      = "ld_wrapper.sh"
_OBJCOPY = "objcopy_wrapper.sh"
_CPP     = "cc_wrapper.sh"    # gcc -E handles preprocessing

# ── Action sets ───────────────────────────────────────────────────────────────

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
    # ── Derive toolchain include directories ──────────────────────────────────
    # ctx.file.gcc_tool.path is execroot-relative, e.g.:
    #   external/+_repo_rules2+xtensa_esp_elf_linux64/bin/xtensa-esp-elf-gcc
    # Strip everything from "/bin/" onward to get the toolchain root.
    # Bazel accepts execroot-relative paths in cxx_builtin_include_directories.
    gcc_path = ctx.file.gcc_tool.path
    toolchain_root = gcc_path[:gcc_path.rfind("/bin/")]
    triple = ctx.attr.toolchain_triple
    version = ctx.attr.gcc_version

    cxx_builtin_include_directories = [
        toolchain_root + "/lib/gcc/" + triple + "/" + version + "/include",
        toolchain_root + "/lib/gcc/" + triple + "/" + version + "/include-fixed",
        toolchain_root + "/" + triple + "/include",
        toolchain_root + "/" + triple + "/sys-include",
    ]

    # ── Tool paths ────────────────────────────────────────────────────────────
    tool_paths = [
        tool_path(name = "gcc",     path = _GCC),
        tool_path(name = "g++",     path = _GPP),
        tool_path(name = "cpp",     path = _CPP),
        tool_path(name = "ar",      path = _AR),
        tool_path(name = "ld",      path = _LD),
        tool_path(name = "objcopy", path = _OBJCOPY),
        tool_path(name = "strip",   path = "/bin/false"),
        tool_path(name = "objdump", path = "/bin/false"),
        tool_path(name = "nm",      path = "/bin/false"),
        tool_path(name = "gcov",    path = "/bin/false"),
        tool_path(name = "dwp",     path = "/bin/false"),
        tool_path(name = "llvm-profdata", path = "/bin/false"),
    ]

    # ── Features ──────────────────────────────────────────────────────────────

    # Board-specific compile flags.  The rule appends -MMD and -c so callers
    # don't need to include them.
    default_compile_flags_feature = feature(
        name = "default_compile_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = [ACTION_NAMES.c_compile],
                flag_groups = [flag_group(flags = ctx.attr.compile_flags_c + ["-MMD", "-c"])],
            ),
            flag_set(
                actions = [
                    ACTION_NAMES.cpp_compile,
                    ACTION_NAMES.cpp_header_parsing,
                    ACTION_NAMES.cpp_module_compile,
                    ACTION_NAMES.cpp_module_codegen,
                ],
                flag_groups = [flag_group(flags = ctx.attr.compile_flags_cxx + ["-MMD", "-c"])],
            ),
        ],
    )

    default_link_flags_feature = feature(
        name = "default_link_flags",
        enabled = True,
        flag_sets = [
            flag_set(
                actions = _ALL_LINK_ACTIONS,
                flag_groups = [flag_group(flags = ctx.attr.link_flags)],
            ),
        ],
    )

    supports_pic_feature = feature(name = "supports_pic", enabled = False)
    supports_dynamic_linker_feature = feature(name = "supports_dynamic_linker", enabled = False)

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

    # Wrap all libraries in --start-group/--end-group so embedded SDK archives
    # with circular dependencies link correctly regardless of order.
    # (Arduino's platform.txt does the same thing.)
    libraries_to_link_feature = feature(
        name = "libraries_to_link",
        flag_sets = [
            flag_set(
                actions = _ALL_LINK_ACTIONS,
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

    # Note: source_file and output_compile_flags features are intentionally
    # omitted — Bazel's CppCompile action already appends "-c <src> -o <out>"
    # as a legacy default; adding them again would duplicate the source file.
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
        tool_paths = tool_paths,
        features = features,
        cxx_builtin_include_directories = cxx_builtin_include_directories,
    )

cc_toolchain_config = rule(
    implementation = _impl,
    attrs = {
        # ── Toolchain binary ──────────────────────────────────────────────────
        "gcc_tool": attr.label(
            allow_single_file = True,
            mandatory = True,
            doc = "GCC binary filegroup. Its execroot-relative path is used to " +
                  "derive cxx_builtin_include_directories at analysis time.",
        ),
        # ── Include directory derivation ──────────────────────────────────────
        # These two attrs together locate lib/gcc/<triple>/<version>/include
        # and <triple>/include inside the toolchain archive.
        "toolchain_triple": attr.string(
            mandatory = True,
            doc = "Target triple used in the GCC installation layout, e.g. " +
                  "'xtensa-esp-elf' or 'arm-none-eabi'.",
        ),
        "gcc_version": attr.string(
            mandatory = True,
            doc = "GCC version string, e.g. '15.2.0'. Must match the version " +
                  "in the toolchain archive. Keep in sync with the http_archive " +
                  "URL in MODULE.bazel.",
        ),
        # ── Toolchain identity (passed to cc_common.create_cc_toolchain_config_info)
        "toolchain_identifier": attr.string(mandatory = True),
        "target_system_name":   attr.string(mandatory = True),
        "target_cpu":           attr.string(mandatory = True),
        "target_libc":          attr.string(default = "unknown"),
        "abi_version":          attr.string(default = "unknown"),
        "abi_libc_version":     attr.string(default = "unknown"),
        # ── Board-specific flags ──────────────────────────────────────────────
        # Architecture flags, optimization level, language features.
        # Do NOT include -std=, -MMD, or -c here; those are handled by the rule.
        "compile_flags_c":   attr.string_list(default = []),
        "compile_flags_cxx": attr.string_list(default = []),
        "link_flags":        attr.string_list(default = []),
    },
    provides = [CcToolchainConfigInfo],
)
