"""Composable application building blocks for the Everything monorepo.

Each building block is a Bazel rule that produces a JSON fragment describing
one aspect of an application. `app_manifest` merges any number of fragments
into the final metadata JSON that release_helper and helm_composer consume.

BUILDING BLOCKS:
================

  app_image    — references an OCI image target
  app_deploy   — deployment shape (app_type, port, replicas, command, args)
  app_ingress  — ingress routing
  app_health   — health check config
  app_resource — CPU/memory requests and limits
  app_openapi  — references an OpenAPI spec target

ASSEMBLY:
=========

  app_manifest — merges identity fields + any number of block labels
                 into one metadata JSON (same shape as current app_metadata)

USAGE:
======

    multiplatform_image(name = "my-app_image", ...)

    app_image(name = "my-app_image_ref", image_target = ":my-app_image", ...)
    app_deploy(name = "my-app_deploy", app_type = "external-api", port = 8000)
    app_ingress(name = "my-app_ingress", host = "my-app.example.com")

    app_manifest(
        name = "my-app_metadata",
        app_name = "my-app",
        language = "go",
        domain = "myteam",
        binary_target = ":my-app",
        components = [
            ":my-app_image_ref",
            ":my-app_deploy",
            ":my-app_ingress",
        ],
    )

A worker omits the ingress block. A CLI tool omits deploy entirely.
You only plug in what applies.
"""

# =============================================================================
# Provider
# =============================================================================

AppFragmentInfo = provider(
    doc = "Marks a target as an app metadata JSON fragment.",
    fields = {"json_file": "The JSON fragment file"},
)

# =============================================================================
# app_image
# =============================================================================

def _app_image_impl(ctx):
    fragment = {
        "image_target": str(ctx.attr.image_target.label),
        "registry": ctx.attr.registry,
        "organization": ctx.attr.organization,
        "repo_name": ctx.attr.repo_name,
    }
    output = ctx.actions.declare_file(ctx.label.name + ".json")
    ctx.actions.write(output = output, content = json.encode(fragment))
    return [
        DefaultInfo(files = depset([output])),
        AppFragmentInfo(json_file = output),
    ]

app_image = rule(
    implementation = _app_image_impl,
    doc = "Fragment referencing an OCI image target.",
    attrs = {
        "image_target": attr.label(mandatory = True),
        "registry": attr.string(default = "ghcr.io"),
        "organization": attr.string(default = "whale-net"),
        "repo_name": attr.string(mandatory = True),
    },
)

# =============================================================================
# app_deploy
# =============================================================================

def _app_deploy_impl(ctx):
    fragment = {}
    if ctx.attr.app_type:
        fragment["app_type"] = ctx.attr.app_type
    if ctx.attr.port:
        fragment["port"] = ctx.attr.port
    if ctx.attr.replicas:
        fragment["replicas"] = ctx.attr.replicas
    if ctx.attr.command:
        fragment["command"] = ctx.attr.command
    if ctx.attr.args:
        fragment["args"] = ctx.attr.args

    output = ctx.actions.declare_file(ctx.label.name + ".json")
    ctx.actions.write(output = output, content = json.encode(fragment))
    return [
        DefaultInfo(files = depset([output])),
        AppFragmentInfo(json_file = output),
    ]

app_deploy = rule(
    implementation = _app_deploy_impl,
    doc = "Fragment for deployment shape (app_type, port, replicas, command, args).",
    attrs = {
        "app_type": attr.string(default = ""),
        "port": attr.int(default = 0),
        "replicas": attr.int(default = 0),
        "command": attr.string_list(default = []),
        "args": attr.string_list(default = []),
    },
)

# =============================================================================
# app_ingress
# =============================================================================

def _app_ingress_impl(ctx):
    fragment = {
        "ingress": {
            "host": ctx.attr.host,
            "tls_secret_name": ctx.attr.tls_secret,
        },
    }
    output = ctx.actions.declare_file(ctx.label.name + ".json")
    ctx.actions.write(output = output, content = json.encode(fragment))
    return [
        DefaultInfo(files = depset([output])),
        AppFragmentInfo(json_file = output),
    ]

app_ingress = rule(
    implementation = _app_ingress_impl,
    doc = "Fragment for ingress routing.",
    attrs = {
        "host": attr.string(mandatory = True),
        "tls_secret": attr.string(default = ""),
    },
)

# =============================================================================
# app_health
# =============================================================================

def _app_health_impl(ctx):
    fragment = {
        "health_check": {
            "enabled": True,
            "path": ctx.attr.path,
        },
    }
    output = ctx.actions.declare_file(ctx.label.name + ".json")
    ctx.actions.write(output = output, content = json.encode(fragment))
    return [
        DefaultInfo(files = depset([output])),
        AppFragmentInfo(json_file = output),
    ]

app_health = rule(
    implementation = _app_health_impl,
    doc = "Fragment for health check config.",
    attrs = {
        "path": attr.string(default = "/health"),
    },
)

# =============================================================================
# app_resource
# =============================================================================

def _app_resource_impl(ctx):
    fragment = {
        "resources": {
            "requests_cpu": ctx.attr.requests_cpu,
            "requests_memory": ctx.attr.requests_memory,
            "limits_cpu": ctx.attr.limits_cpu,
            "limits_memory": ctx.attr.limits_memory,
        },
    }
    output = ctx.actions.declare_file(ctx.label.name + ".json")
    ctx.actions.write(output = output, content = json.encode(fragment))
    return [
        DefaultInfo(files = depset([output])),
        AppFragmentInfo(json_file = output),
    ]

app_resource = rule(
    implementation = _app_resource_impl,
    doc = "Fragment for CPU/memory resource requests and limits.",
    attrs = {
        "requests_cpu": attr.string(default = ""),
        "requests_memory": attr.string(default = ""),
        "limits_cpu": attr.string(default = ""),
        "limits_memory": attr.string(default = ""),
    },
)

# =============================================================================
# app_openapi
# =============================================================================

def _app_openapi_impl(ctx):
    fragment = {
        "openapi_spec_target": str(ctx.attr.spec_target.label),
    }
    output = ctx.actions.declare_file(ctx.label.name + ".json")
    ctx.actions.write(output = output, content = json.encode(fragment))
    return [
        DefaultInfo(files = depset([output])),
        AppFragmentInfo(json_file = output),
    ]

app_openapi = rule(
    implementation = _app_openapi_impl,
    doc = "Fragment referencing an OpenAPI spec target.",
    attrs = {
        "spec_target": attr.label(mandatory = True),
    },
)

# =============================================================================
# app_manifest
# =============================================================================

def _app_manifest_impl(ctx):
    """Merges identity fields with all component JSON fragments."""
    base = {
        "name": ctx.attr.app_name,
        "version": ctx.attr.version,
        "description": ctx.attr.description,
        "language": ctx.attr.language,
        "domain": ctx.attr.domain,
        "binary_target": str(ctx.attr.binary_target.label),
    }

    fragment_files = []
    for component in ctx.attr.components:
        if AppFragmentInfo in component:
            fragment_files.append(component[AppFragmentInfo].json_file)
        else:
            for f in component[DefaultInfo].files.to_list():
                if f.path.endswith(".json"):
                    fragment_files.append(f)

    if not fragment_files:
        output = ctx.actions.declare_file(ctx.label.name + "_metadata.json")
        ctx.actions.write(output = output, content = json.encode(base))
        return [DefaultInfo(files = depset([output]))]

    base_file = ctx.actions.declare_file(ctx.label.name + "_base.json")
    ctx.actions.write(output = base_file, content = json.encode(base))

    merge_script = ctx.actions.declare_file(ctx.label.name + "_merge.sh")
    script_lines = [
        "#!/bin/bash",
        "set -e",
        "",
        'OUTPUT_FILE="$1"',
        "shift",
        "",
        "RESULT=''",
        'for FRAG_FILE in "$@"; do',
        "    INNER=$(cat \"$FRAG_FILE\" | sed 's/^{//' | sed 's/}$//')",
        "    INNER=$(echo \"$INNER\" | sed 's/^[[:space:]]*//' | sed 's/[[:space:]]*$//')",
        '    if [ -z "$INNER" ]; then',
        "        continue",
        "    fi",
        '    if [ -z "$RESULT" ]; then',
        '        RESULT="$INNER"',
        "    else",
        '        RESULT="${RESULT},${INNER}"',
        "    fi",
        "done",
        "",
        'echo "{${RESULT}}" > "$OUTPUT_FILE"',
    ]
    ctx.actions.write(
        output = merge_script,
        content = "\n".join(script_lines) + "\n",
        is_executable = True,
    )

    output = ctx.actions.declare_file(ctx.label.name + "_metadata.json")
    all_inputs = [base_file] + fragment_files
    run_args = [output.path] + [f.path for f in all_inputs]

    ctx.actions.run(
        executable = merge_script,
        arguments = run_args,
        inputs = all_inputs + [merge_script],
        outputs = [output],
        mnemonic = "AssembleAppManifest",
        progress_message = "Assembling app manifest for %s" % ctx.attr.app_name,
    )

    return [DefaultInfo(files = depset([output]))]

app_manifest = rule(
    implementation = _app_manifest_impl,
    doc = """Assembles a complete app metadata file from identity + pluggable component fragments.

    Each component is a label pointing to a block rule (app_image, app_deploy,
    app_ingress, etc.). Fragments are merged in order into a single JSON file
    compatible with release_helper and helm_composer.
    """,
    attrs = {
        "app_name": attr.string(mandatory = True),
        "version": attr.string(default = "latest"),
        "description": attr.string(default = ""),
        "language": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "binary_target": attr.label(mandatory = True),
        "components": attr.label_list(default = []),
    },
)
