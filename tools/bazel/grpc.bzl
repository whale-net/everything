"""Build rules for gRPC services in Go."""

load("@protobuf//bazel/common:proto_info.bzl", "ProtoInfo")
load("@rules_go//go:def.bzl", "go_library")
load("@rules_go//proto:def.bzl", "go_proto_library")
load("@rules_proto//proto:defs.bzl", "proto_library")

def go_grpc_library(
        name,
        srcs,
        proto_deps = [],
        go_deps = [],
        importpath = None,
        visibility = None,
        **kwargs):
    """Generates a Go library from proto files with gRPC support.

    This macro creates:
    1. A proto_library target
    2. A go_proto_library target with gRPC support
    3. A go_library target that can be imported by other Go code

    Args:
        name: Name of the generated go_library target
        srcs: List of .proto files
        proto_deps: List of other proto_library targets this depends on
        go_deps: List of go_proto_library targets this depends on (for proto imports)
        importpath: Go import path for the generated library
        visibility: Visibility of the generated targets
        **kwargs: Additional arguments passed to the targets
    """
    proto_name = name + "_proto"
    go_proto_name = name + "_go_proto"

    # Create proto_library
    proto_library(
        name = proto_name,
        srcs = srcs,
        deps = proto_deps,
        visibility = ["//visibility:private"],
    )

    # Create go_proto_library with gRPC support
    go_proto_library(
        name = go_proto_name,
        proto = ":" + proto_name,
        compilers = ["@rules_go//proto:go_grpc"],
        importpath = importpath,
        deps = go_deps,
        visibility = ["//visibility:private"],
        **kwargs
    )

    # Create the public go_library
    native.alias(
        name = name,
        actual = ":" + go_proto_name,
        visibility = visibility,
    )

def _proto_descriptor_set_impl(ctx):
    """Implementation for proto_descriptor_set rule."""
    proto_info = ctx.attr.proto_library[ProtoInfo]

    output = ctx.actions.declare_file(ctx.label.name + ".pb")

    # --include_imports + --include_source_info make this a self-contained
    # FileDescriptorSet: every transitively-imported .proto (e.g.
    # firmware/proto/config.proto) is embedded, plus enough source info for
    # a caller to resolve comments/positions -- not just a bare set of the
    # direct .proto's symbols. Only direct_sources are passed as compile
    # inputs; --include_imports is what pulls in the rest via the -I paths
    # below, so this is one protoc invocation compiling exactly the same
    # sources the proto_library (and therefore the server's go_proto_library)
    # was built from -- not a second, potentially-drifting copy.
    args = ctx.actions.args()
    args.add(output, format = "--descriptor_set_out=%s")
    args.add("--include_imports")
    args.add("--include_source_info")
    args.add_all(proto_info.transitive_proto_path, format_each = "--proto_path=%s")
    args.add_all(proto_info.direct_sources)

    ctx.actions.run(
        executable = ctx.executable._protoc,
        arguments = [args],
        inputs = depset(transitive = [proto_info.transitive_sources]),
        outputs = [output],
        mnemonic = "ProtoDescriptorSet",
        progress_message = "Generating proto descriptor set %{output}",
    )

    return [DefaultInfo(files = depset([output]))]

proto_descriptor_set = rule(
    implementation = _proto_descriptor_set_impl,
    attrs = {
        "proto_library": attr.label(
            mandatory = True,
            providers = [ProtoInfo],
            doc = "proto_library target to build a self-contained FileDescriptorSet from.",
        ),
        "_protoc": attr.label(
            default = "@protobuf//:protoc",
            executable = True,
            cfg = "exec",
        ),
    },
    doc = """Generates a self-contained protobuf FileDescriptorSet
    (--include_imports --include_source_info) from a proto_library target,
    as a Bazel build action -- not a file a developer runs protoc on by
    hand. Exists so programmatic callers can learn a gRPC service's
    contract without server reflection; see leaflab's FR81/NFR11
    (github.com/whale-net/everything issue #1166) for the motivating case.
    """,
)
