# Hello gRPC Demo (Go)

This demo demonstrates the gRPC build infrastructure for the monorepo.

## Structure

```
hello_grpc_go/
├── protos/              # Protocol buffer definitions
│   ├── hello.proto      # Service and message definitions
│   └── BUILD.bazel      # Build rules for proto compilation
├── server/              # gRPC server implementation
│   ├── main.go          # Server code
│   └── BUILD.bazel      # Build rules for server binary
├── client/              # gRPC client implementation
│   ├── main.go          # Client code
│   └── BUILD.bazel      # Build rules for client binary
└── README.md            # This file
```

## Building

Build the server:
```bash
bazel build //demo/hello_grpc_go/server:hello-grpc-server
```

Build the client:
```bash
bazel build //demo/hello_grpc_go/client:hello-grpc-client
```

## Running

Start the server:
```bash
bazel run //demo/hello_grpc_go/server:hello-grpc-server
```

In another terminal, run the client:
```bash
# Unary RPC
bazel run //demo/hello_grpc_go/client:hello-grpc-client -- --name="Bazel"

# Streaming RPC
bazel run //demo/hello_grpc_go/client:hello-grpc-client -- --name="Bazel" --stream
```

## gRPC Build Infrastructure

This demo validates the gRPC build infrastructure:

1. **MODULE.bazel** - Added protobuf and gRPC dependencies
2. **//tools/bazel/grpc.bzl** - Build macro for Go gRPC libraries
3. **go_grpc_library** - Generates Go code from .proto files with gRPC support

## Usage in Other Projects

To use gRPC in your service:

```starlark
# In your BUILD.bazel
load("//tools/bazel:grpc.bzl", "go_grpc_library")

go_grpc_library(
    name = "myservice_go_proto",
    srcs = ["myservice.proto"],
    importpath = "github.com/whale-net/everything/path/to/myservice",
    visibility = ["//visibility:public"],
)
```

Then depend on it in your Go code:
```starlark
go_binary(
    name = "myserver",
    srcs = ["main.go"],
    deps = [
        ":myservice_go_proto",
        "@org_golang_google_grpc//:grpc",
    ],
)
```
