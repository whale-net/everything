"""Single-file, self-contained Python executables via `pex --scie eager`.

`pex --scie eager` (https://docs.pex-tool.org/scie.html) embeds a
python-build-standalone CPython interpreter directly into the output file, so
the result runs with zero on-target Python dependency -- one native
executable file, same shape as a `go_binary` output. That's exactly what the
app_registry "cli"/"binary" packaging path expects: `PackageAppAssets`
(tools/release_helper_go/cmd/package_assets.go) just runs
`bazel build --platforms=//tools:linux_x86_64 //pkg:target` (etc, per
platform) and picks up whichever single executable file lands in
`bazel-bin/<pkg>/`. A py_scie_binary target can be passed straight to
`release_app(app_type = "cli", binary_name = ...)`, the same way
//tools/release_helper_go does today with a go_binary -- see demo/hello_cli.

This is a real `rule()`, not a `genrule` wrapper: `release_app`'s
`app_metadata`, like the rest of the release pipeline, requires
`binary_target` to point at an executable rule target (the same shape
go_binary/py_binary provide), and rejects a bare generated-file label
("generated file ... is misplaced here (expected no files)").

Linux only: the embedded interpreter's platform is select()ed off
`@platforms//cpu`, matching `--platforms=//tools:linux_x86_64` /
`//tools:linux_arm64` (tools/bazel/platforms.bzl). Darwin isn't wired up --
extend _SCIE_PLATFORM_BY_CPU with an os/cpu select arm and a "macos-*" value
if that's ever needed.

Requires network access at build time: `pex --scie eager` downloads the
target's CPython distribution (and the `science` scie-builder tool) from
GitHub on first build, hence the `requires-network` execution requirement.
There's no offline/pre-mirrored mode wired up yet -- see
`--scie-assets-base-url` in `pex --help` if a network-restricted CI runner
ever needs one; the interpreter release already pinned for containers in
MODULE.bazel (python_stripped_x86_64/_arm64, python-build-standalone
20251014) would be the thing to mirror from.
"""

_SCIE_PLATFORM_BY_CPU = select({
    "@platforms//cpu:x86_64": "linux-x86_64",
    "@platforms//cpu:arm64": "linux-aarch64",
})

def _py_scie_binary_impl(ctx):
    src_dir = ctx.files.srcs[0].dirname
    scie_platform = ctx.attr.scie_platform
    out = ctx.actions.declare_file(ctx.label.name)

    # --scie-name-style platform-file-suffix makes pex always write its
    # output as "<given -o path>-<scie-platform>" rather than the exact -o
    # path -- deterministically, not just when cross-building (the default
    # "dynamic" style only suffixes when target != host, which would make
    # the output filename depend on which machine happened to build it).
    # So point -o at the real declared output path and move pex's actual
    # (suffixed) result into place afterward. This path is deliberately a
    # plain string, not a declare_file()'d File: it's an undeclared
    # intermediate the action itself renames away by the time it finishes,
    # and Bazel requires every declared output to have survived on disk.
    pex_output_path = out.path + "-" + scie_platform

    command = " ".join([
        "python3",
        ctx.file._pex_tool.path,
        "-D",
        src_dir,
        "-e",
        ctx.attr.entry_point,
        "--scie eager --scie-only --scie-name-style platform-file-suffix",
        "--scie-platform",
        scie_platform,
        "--scie-python-version",
        ctx.attr.python_version,
        "--scie-pbs-release",
        ctx.attr.pbs_release,
        "-o",
        out.path,
        "&&",
        "mv",
        pex_output_path,
        out.path,
    ])

    ctx.actions.run_shell(
        outputs = [out],
        inputs = ctx.files.srcs,
        tools = [ctx.file._pex_tool],
        command = command,
        mnemonic = "PexScieBinary",
        progress_message = "Building single-file pex scie %{output}",
        execution_requirements = {"requires-network": "1"},
        use_default_shell_env = True,
    )

    return [DefaultInfo(
        executable = out,
        files = depset([out]),
        runfiles = ctx.runfiles(files = [out]),
    )]

_py_scie_binary = rule(
    implementation = _py_scie_binary_impl,
    executable = True,
    attrs = {
        "entry_point": attr.string(mandatory = True),
        "srcs": attr.label_list(mandatory = True, allow_files = [".py"]),
        "python_version": attr.string(default = "3.13.9"),
        "pbs_release": attr.string(default = "20251014"),
        # Public despite being macro-computed: leading-underscore ("private")
        # attrs can only ever take their schema default, so a real value
        # can't be threaded in from py_scie_binary() below. Callers just
        # never see this -- the public py_scie_binary() signature has no
        # scie_platform parameter of its own.
        "scie_platform": attr.string(mandatory = True),
        "_pex_tool": attr.label(default = "@pex_tool//file", allow_single_file = True, cfg = "exec"),
    },
)

def py_scie_binary(name, entry_point, srcs, python_version = "3.13.9", pbs_release = "20251014", visibility = None):
    """Builds `name` as a single-file native executable via `pex --scie eager`.

    Args:
        name: output binary target name.
        entry_point: pex `-e` entry point, e.g. "main:run" for a top-level
            `def run():` in main.py.
        srcs: python source files. All of `srcs` must live in one directory
            (the whole directory is handed to pex as a `-D` sources
            directory) -- fine for a flat module or a single package, not
            yet extended to pull in third-party deps or sibling py_library
            targets.
        python_version: exact CPython version to embed (must be published as
            a python-build-standalone `install_only` release under
            `pbs_release`). Keep in sync with MODULE.bazel's
            python_stripped_x86_64/_arm64 pin unless there's a specific
            reason to diverge.
        pbs_release: python-build-standalone release date (their release tag,
            e.g. "20251014") to pull `python_version` from. Not every release
            publishes every patch version -- pinning this explicitly avoids
            pex silently trying the newest release and failing if that
            release dropped the pinned `python_version`.
        visibility: visibility for the output binary target.
    """
    _py_scie_binary(
        name = name,
        entry_point = entry_point,
        srcs = srcs,
        python_version = python_version,
        pbs_release = pbs_release,
        scie_platform = _SCIE_PLATFORM_BY_CPU,
        visibility = visibility,
    )
