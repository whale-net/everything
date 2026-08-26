"""Build rules for generating gRPC service descriptor sets."""

load("@rules_proto//proto:defs.bzl", "proto_library")

def _proto_descriptor_set_impl(ctx):
    """Implementation for proto_descriptor_set rule."""

    # Get the proto_library target
    proto_lib = ctx.attr.proto_library

    # Get the protoc compiler from the toolchain
    toolchain = ctx.toolchains["@rules_proto//proto:toolchain_type"]
    protoc = toolchain.proto.proto_compiler

    # Get the ProtoInfo provider
    proto_info = proto_lib[ProtoInfo]

    # Collect all proto files from the target and its dependencies
    proto_files = []
    import_paths_set = {}

    # Collect all proto source files - direct and transitive
    for proto_file in proto_info.transitive_sources.to_list():
        proto_files.append(proto_file)
        # Extract the import path from each proto file
        # For workspace files, this is the path relative to the workspace root
        import_path = proto_file.root.path if proto_file.root else "."
        if import_path not in import_paths_set:
            import_paths_set[import_path] = True

    # Create output descriptor set file
    descriptor_set = ctx.actions.declare_file(ctx.label.name + ".pb")

    # Build protoc command arguments
    args = [
        "--descriptor_set_out={}".format(descriptor_set.path),
        "--include_source_info",
        "--include_imports",
    ]

    # Add the workspace root as an import path to resolve cross-package imports
    # For workspace files, proto_file.root.path should give us the workspace root
    if proto_files:
        first_file = proto_files[0]
        workspace_root = first_file.root.path if first_file.root else "."
        args.append("--proto_path={}".format(workspace_root))

    # Add proto files to compile (only direct sources, not transitive)
    for proto_file in proto_info.direct_sources:
        args.append(proto_file.path)

    # Run protoc
    ctx.actions.run(
        executable = protoc,
        arguments = args,
        inputs = depset(proto_files),
        outputs = [descriptor_set],
        mnemonic = "ProtoDescriptorSet",
        progress_message = "Generating proto descriptor set for %{label}",
    )

    return [
        DefaultInfo(files = depset([descriptor_set])),
    ]

proto_descriptor_set = rule(
    implementation = _proto_descriptor_set_impl,
    attrs = {
        "proto_library": attr.label(
            mandatory = True,
            providers = [ProtoInfo],
            doc = "The proto_library target to generate descriptor set from",
        ),
    },
    toolchains = ["@rules_proto//proto:toolchain_type"],
    doc = "Generates a protobuf FileDescriptorSet from a proto_library target.",
)
