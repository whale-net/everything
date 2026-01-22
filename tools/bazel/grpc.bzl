"""Build rules for gRPC services in Go."""

load("@rules_proto//proto:defs.bzl", "proto_library")
load("@rules_go//go:def.bzl", "go_library")
load("@rules_go//proto:def.bzl", "go_proto_library")

def go_grpc_library(
        name,
        srcs,
        deps = [],
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
        deps: List of other proto_library targets this depends on
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
        deps = deps,
        visibility = ["//visibility:private"],
    )

    # Create go_proto_library with gRPC support
    go_proto_library(
        name = go_proto_name,
        proto = ":" + proto_name,
        compilers = ["@rules_go//proto:go_grpc"],
        importpath = importpath,
        visibility = ["//visibility:private"],
        **kwargs
    )

    # Create the public go_library
    native.alias(
        name = name,
        actual = ":" + go_proto_name,
        visibility = visibility,
    )
