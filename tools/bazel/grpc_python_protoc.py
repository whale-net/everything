"""Runs grpc_tools.protoc exactly as `python3 -m grpc_tools.protoc` would.

py_grpc_library (tools/bazel/grpc.bzl) uses this as a genrule tool to
generate the `_pb2_grpc.py` gRPC service stub -- the one output
`@protobuf//bazel:py_proto_library.bzl`'s `py_proto_library` rule doesn't
produce (it only generates the `_pb2.py` message classes).

Running the module via `runpy` (instead of importing and calling
`protoc.main()` directly) preserves grpc_tools.protoc's own `__main__`
block, which adds an `-I` for its bundled well-known-type `.proto` files
(google/protobuf/*.proto) -- the same behavior `python3 -m grpc_tools.protoc`
gets for free on a developer's machine.
"""

import runpy
import sys

if __name__ == "__main__":
    sys.argv[0] = "protoc"
    runpy.run_module("grpc_tools.protoc", run_name="__main__")
