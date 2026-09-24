"""Build rules for gRPC services in Go and Python."""

load("@rules_proto//proto:defs.bzl", "proto_library")
load("@rules_go//go:def.bzl", "go_library")
load("@rules_go//proto:def.bzl", "go_proto_library")
load("@protobuf//bazel:py_proto_library.bzl", "py_proto_library")
load("@rules_python//python:defs.bzl", "py_library")

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

def py_grpc_library(
        name,
        srcs,
        proto_deps = [],
        visibility = None):
    """Generates a Python library from proto files with gRPC support.

    This macro creates:
    1. A proto_library target
    2. A py_proto_library target (from @protobuf//bazel:py_proto_library.bzl)
       for the `*_pb2.py` message classes
    3. A genrule that runs `grpc_tools.protoc` to generate the `*_pb2_grpc.py`
       gRPC stub/servicer -- py_proto_library only covers messages, not the
       gRPC service code, and this repo has no other Python gRPC codegen path
    4. A py_library combining both, importable the same way any other Python
       target under this package is (e.g. `whagent_net.protos.session_pb2`
       and `whagent_net.protos.session_pb2_grpc`)

    Args:
        name: Name of the generated py_library target
        srcs: List of .proto files
        proto_deps: List of other proto_library targets this depends on
        visibility: Visibility of the generated targets
    """
    proto_name = name + "_proto"
    pb2_name = name + "_pb2"
    grpc_stub_name = name + "_pb2_grpc_gen"

    # Create proto_library (shared with the py_proto_library aspect below).
    proto_library(
        name = proto_name,
        srcs = srcs,
        deps = proto_deps,
        visibility = ["//visibility:private"],
    )

    # Generates `*_pb2.py` (+ `*_pb2.pyi`) message classes.
    py_proto_library(
        name = pb2_name,
        deps = [":" + proto_name],
        visibility = ["//visibility:private"],
    )

    # py_proto_library doesn't generate the gRPC service stub/servicer, so
    # run grpc_tools.protoc ourselves for the `*_pb2_grpc.py` output.
    # Invoked with -I. from the repo root so the generated file's own import
    # of its sibling `*_pb2` module comes out package-qualified
    # (e.g. `from whagent_net.protos import session_pb2`), matching where
    # py_proto_library places it -- not a bare `import session_pb2` that
    # would only resolve if this package's directory were also on sys.path.
    grpc_outs = [src[:-len(".proto")] + "_pb2_grpc.py" for src in srcs]
    native.genrule(
        name = grpc_stub_name,
        srcs = srcs,
        outs = grpc_outs,
        tools = ["//tools/bazel:protoc_grpc_python"],
        cmd = "$(location //tools/bazel:protoc_grpc_python) " +
              "-I. --grpc_python_out=$(GENDIR) $(SRCS)",
        visibility = ["//visibility:private"],
    )

    # Combine the generated messages and gRPC stub into one public py_library.
    py_library(
        name = name,
        srcs = [":" + grpc_stub_name],
        deps = [
            ":" + pb2_name,
            "@pypi//:grpcio",
        ],
        visibility = visibility,
    )
