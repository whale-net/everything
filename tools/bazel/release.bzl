"""Release utilities for the Everything monorepo."""

load("//tools/bazel:container_image.bzl", "multiplatform_image")
load("//tools/helm:helm.bzl", "helm_chart")
load("//tools/openapi:openapi.bzl", "openapi_spec")

# Exposes app_metadata fields as structured data so consumers can read them via
# `bazel cquery --output=starlark` without building the JSON file output.
AppMetadataInfo = provider(
    doc = "Release metadata for an app, suitable for discovery via cquery.",
    fields = {
        "metadata": "dict of metadata fields (same shape as the JSON file output)",
    },
)

# Valid values for the deploy_unit attr, mapped to the appmetapb.DeployUnit
# enum's JSON name so protojson (used by //tools/appmeta:manifest_contract_test
# and all Go consumers) can decode the emitted string directly as the enum.
_DEPLOY_UNIT_TO_PROTO_ENUM = {
    "chart": "DEPLOY_UNIT_CHART",
    "image": "DEPLOY_UNIT_IMAGE",
    "none": "DEPLOY_UNIT_NONE",
}

def _app_metadata_impl(ctx):
    """Implementation for app_metadata rule."""

    # Create a JSON file with app metadata
    metadata = {
        "name": ctx.attr.app_name,
        "version": ctx.attr.version,
        "binary_target": str(ctx.attr.binary_target.label) if ctx.attr.binary_target else "",
        "image_target": str(ctx.attr.image_target.label) if ctx.attr.image_target else "",
        "description": ctx.attr.description,
        "language": ctx.attr.language,
        "registry": ctx.attr.registry,
        "organization": ctx.attr.organization,
        "repo_name": ctx.attr.repo_name,
        "domain": ctx.attr.domain,
    }

    # Add metadata directly from attributes
    if ctx.attr.app_type:
        metadata["app_type"] = ctx.attr.app_type
    if ctx.attr.port:
        metadata["port"] = ctx.attr.port
    metadata["replicas"] = ctx.attr.replicas

    # Add optional health check configuration if provided
    if ctx.attr.health_check_enabled:
        metadata["health_check"] = {
            "enabled": ctx.attr.health_check_enabled,
            "path": ctx.attr.health_check_path,
        }

    # Add optional ingress configuration if provided
    if ctx.attr.ingress_host:
        metadata["ingress"] = {
            "host": ctx.attr.ingress_host,
            "tls_secret_name": ctx.attr.ingress_tls_secret,
        }

    # Add command and args
    if ctx.attr.command:
        metadata["command"] = ctx.attr.command
    if ctx.attr.args:
        metadata["args"] = ctx.attr.args

    # Add resource configuration if provided
    if ctx.attr.resources_requests_cpu or ctx.attr.resources_requests_memory or ctx.attr.resources_limits_cpu or ctx.attr.resources_limits_memory:
        metadata["resources"] = {
            "requests_cpu": ctx.attr.resources_requests_cpu,
            "requests_memory": ctx.attr.resources_requests_memory,
            "limits_cpu": ctx.attr.resources_limits_cpu,
            "limits_memory": ctx.attr.resources_limits_memory,
        }

    # Add OpenAPI spec target if provided
    if ctx.attr.openapi_spec_target:
        metadata["openapi_spec_target"] = str(ctx.attr.openapi_spec_target.label)

    # Add gRPC descriptor set target if provided (leaflab FR81/NFR11, issue
    # #1166/#1333) -- unlike openapi_spec_target, never auto-generated; only
    # set when release_app's descriptor_set_target param is passed.
    if ctx.attr.descriptor_set_target:
        metadata["descriptor_set_target"] = str(ctx.attr.descriptor_set_target.label)

    # deploy_unit declares how the app reaches an environment. Stored as the
    # appmetapb.DeployUnit enum's JSON name (not the raw attr string) so
    # protojson can decode it directly; see _DEPLOY_UNIT_TO_PROTO_ENUM above.
    if ctx.attr.deploy_unit not in _DEPLOY_UNIT_TO_PROTO_ENUM:
        fail("deploy_unit must be one of {} (got {})".format(
            sorted(_DEPLOY_UNIT_TO_PROTO_ENUM.keys()),
            ctx.attr.deploy_unit,
        ))
    metadata["deploy_unit"] = _DEPLOY_UNIT_TO_PROTO_ENUM[ctx.attr.deploy_unit]

    output = ctx.actions.declare_file(ctx.label.name + "_metadata.json")
    ctx.actions.write(
        output = output,
        content = json.encode(metadata),
    )

    return [
        DefaultInfo(files = depset([output])),
        AppMetadataInfo(metadata = metadata),
    ]

app_metadata = rule(
    implementation = _app_metadata_impl,
    attrs = {
        "app_name": attr.string(mandatory = True),
        "version": attr.string(default = "latest"),
        "binary_target": attr.label(mandatory = True),
        "image_target": attr.label(default = None),
        "description": attr.string(default = ""),
        "language": attr.string(mandatory = True),
        "registry": attr.string(default = "ghcr.io"),
        "organization": attr.string(default = "whale-net"),
        "repo_name": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "app_type": attr.string(default = ""),
        "port": attr.int(default = 0),
        "replicas": attr.int(default = 0),
        "health_check_enabled": attr.bool(default = False),
        "health_check_path": attr.string(default = "/health"),
        "ingress_host": attr.string(default = ""),
        "ingress_tls_secret": attr.string(default = ""),
        "command": attr.string_list(default = []),
        "args": attr.string_list(default = []),
        "resources_requests_cpu": attr.string(default = ""),
        "resources_requests_memory": attr.string(default = ""),
        "resources_limits_cpu": attr.string(default = ""),
        "resources_limits_memory": attr.string(default = ""),
        "openapi_spec_target": attr.label(default = None),
        "descriptor_set_target": attr.label(default = None),
        "deploy_unit": attr.string(default = "chart", values = ["chart", "image", "none"]),
    },
)

# Note: This function has many parameters (24) to support flexible app configuration.
# They are logically grouped as:
# - Binary config: name, binary_name, language
# - Release config: domain, description, version, registry, organization, custom_repo_name
# - Deployment config: app_type, port, replicas, command, args
# - Health check config: health_check_enabled, health_check_path
# - Ingress config: ingress_host, ingress_tls_secret
# - Resource config: resources_requests_cpu, resources_requests_memory, resources_limits_cpu, resources_limits_memory
# - OpenAPI config: fastapi_app
# - Container config: additional_tars
# Bazel/Starlark does not support nested struct parameters, so they remain flat.
def release_app(name, binary_name = None, language = None, domain = None, description = "", version = "latest", registry = "ghcr.io", organization = "whale-net", custom_repo_name = None, app_type = "", port = 0, replicas = 0, health_check_enabled = False, health_check_path = "/health", ingress_host = "", ingress_tls_secret = "", command = [], args = [], resources_requests_cpu = "", resources_requests_memory = "", resources_limits_cpu = "", resources_limits_memory = "", fastapi_app = None, additional_tars = None, deploy_unit = None, app_name = None, base = None, descriptor_set_target = None):
    """Convenience macro to set up release metadata and OCI images for an app.

    This macro consolidates the creation of OCI images and release metadata,
    ensuring consistency between the two systems. Works with standard py_binary
    and go_binary targets, as well as CLI and firmware release targets.

    The binaries are built for different platforms using Bazel's --platforms flag.
    Cross-compilation is handled automatically by rules_pycross (Python) and rules_go (Go).

    Args:
        name: Target name / App identifier
        binary_name: Target label for the binary. Can be:
                     - Simple name: "my_app" -> looks for :my_app
                     - Full label: "//path/to:binary" -> uses that binary
                     Defaults to name if not provided.
        language: Programming language ("python", "go", "cpp", "c++", etc.)
        domain: Domain/category for the app (e.g., "demo", "api", "web")
        description: Optional description of the app
        version: Default version (can be overridden at release time)
        registry: Container registry (defaults to ghcr.io)
        organization: Container registry organization (defaults to whale-net)
        custom_repo_name: Custom repository name (defaults to name)
        app_type: Application type (external-api, internal-api, worker, job, cli, binary, firmware)
        port: Port the application listens on (0 = not specified)
        replicas: Default number of replicas (0 = use composer default based on app_type)
        health_check_enabled: Whether to enable health checks (default: False)
        health_check_path: Path for health check endpoint (default: /health)
        ingress_host: Custom ingress hostname (empty = use default {app}-{env}.local pattern)
        ingress_tls_secret: TLS secret name for ingress (empty = no TLS)
        command: Override container command (default: use image ENTRYPOINT)
        args: Container arguments
        resources_requests_cpu: Custom CPU request (e.g., "100m", "0.5"). Empty = use defaults from app type
        resources_requests_memory: Custom memory request (e.g., "128Mi", "1Gi"). Empty = use defaults from app type
        resources_limits_cpu: Custom CPU limit (e.g., "200m", "1"). Empty = use defaults from app type
        resources_limits_memory: Custom memory limit (e.g., "256Mi", "2Gi"). Empty = use defaults from app type
        fastapi_app: For FastAPI apps, specify the module path and variable name (e.g., "main:app")
                     to auto-generate OpenAPI specs. Creates a {name}_openapi_spec target.
        additional_tars: Additional tar layers to include in the image (e.g., ["//tools/steamcmd:steamcmd"])
        deploy_unit: How the app reaches an environment: "chart" (default for containerized apps, bundled
                     into a Helm chart and not independently promotable), "image" (deployed by moving an image
                     reference directly, no chart involved, e.g. manmanv2-host-manager), or "none"
                     (default for cli/firmware apps, built and published but never deployed to K8s).
        app_name: Override app name for metadata if different from target name.
        base: Override the container base image label (defaults to
              multiplatform_image's own default, "@ubuntu_base"). Use this
              when the app needs a tool the default base doesn't carry --
              e.g. app_registry's worker uses a git-enabled base so its
              writeback activity can shell out to `git` (see
              tools/app_registry/worker/BUILD.bazel and
              MODULE.bazel's "git_base" oci.pull). Go binaries here are
              built pure/static, so swapping the base image never affects
              the binary itself -- only what's available on $PATH.
        descriptor_set_target: Label of a //tools/bazel:grpc.bzl
              proto_descriptor_set target (a published self-contained
              protobuf FileDescriptorSet) to publish as a release artifact
              of this app -- e.g. leaflab-api's
              //leaflab/api/proto:leaflabapi_descriptor_set (FR81/NFR11,
              issue #1166/#1333). Unlike fastapi_app's OpenAPI spec, never
              auto-generated; pass it explicitly.
    """
    effective_name = app_name if app_name else name
    is_container_app = app_type not in ["cli", "binary", "firmware"]

    # Validate name format - must use dashes, not underscores for container apps
    if is_container_app and "_" in effective_name:
        fail("App name '{}' contains underscores. Use dashes instead (e.g., 'my-app' not 'my_app')".format(effective_name))

    if deploy_unit == None:
        deploy_unit = "chart" if is_container_app else "none"

    if is_container_app:
        if language not in ["python", "go"]:
            fail("Unsupported language: {}. Must be 'python' or 'go' for containerized apps".format(language))
    elif not language:
        fail("language is required for release_app")

    # Single binary target - no platform suffixes needed
    # Binary will be built for different platforms using --platforms flag
    base_label = binary_name if binary_name else effective_name
    if not base_label.startswith("//") and not base_label.startswith(":"):
        base_label = ":" + base_label

    # Image name uses domain-app format (e.g., "demo-hello-python")
    image_name = (domain + "-" + effective_name) if domain else effective_name
    image_target_ref = None

    if is_container_app:
        image_target = name + "_image"

        # Create multiplatform OCI image using SINGLE binary target
        # Bazel will build it for different platforms based on --platforms flag
        # Inject default environment variables for logging auto-detection
        default_env = {
            "APP_NAME": name,
            "APP_DOMAIN": domain,
            "APP_TYPE": app_type,
        }
        image_kwargs = {}
        if base:
            image_kwargs["base"] = base
        multiplatform_image(
            name = image_target,
            binary = base_label,  # Single binary, built for different platforms
            registry = registry,
            repository = organization,
            image_name = image_name,
            language = language,
            env = default_env,  # Bake default environment variables
            cmd = args if args else [],  # Pass container args if specified
            additional_tars = additional_tars,  # Pass additional tar layers if specified
            **image_kwargs
        )
        image_target_ref = ":" + image_target

    # Use the binary directly for change detection
    # All platforms are built from the same sources, so one reference is enough
    binary_target_ref = base_label

    # Auto-generate OpenAPI spec for FastAPI apps (before metadata creation)
    openapi_spec_target_ref = None
    if is_container_app and fastapi_app and language == "python":
        # Parse module:variable syntax
        if ":" in fastapi_app:
            module_path, app_var = fastapi_app.split(":", 1)
        else:
            module_path = fastapi_app
            app_var = "app"

        # For OpenAPI generation, we need a library target, not a binary
        # Try to find a corresponding _lib target, or use the binary if that's all we have
        lib_target = base_label
        if not lib_target.endswith("_lib"):
            # Check if there's a {name}_lib or main_lib target we should use instead
            # For now, just use the binary target - it will work but might be less efficient
            pass

        # Use the openapi_spec rule to generate spec with proper dependencies
        openapi_spec_target_name = name + "_openapi_spec"
        openapi_spec(
            name = openapi_spec_target_name,
            app_target = lib_target,
            module_path = module_path,
            app_variable = app_var,
            domain = domain,
            visibility = ["//visibility:public"],
        )
        openapi_spec_target_ref = ":" + openapi_spec_target_name

    # Create release metadata
    app_metadata(
        name = name + "_metadata",
        app_name = effective_name,
        binary_target = binary_target_ref,
        image_target = image_target_ref,
        description = description,
        version = version,
        language = language,
        registry = registry,
        organization = organization,
        repo_name = image_name,
        domain = domain,
        app_type = app_type,
        port = port,
        replicas = replicas,
        health_check_enabled = health_check_enabled,
        health_check_path = health_check_path,
        ingress_host = ingress_host,
        ingress_tls_secret = ingress_tls_secret,
        command = command,
        args = args,
        resources_requests_cpu = resources_requests_cpu,
        resources_requests_memory = resources_requests_memory,
        resources_limits_cpu = resources_limits_cpu,
        resources_limits_memory = resources_limits_memory,
        openapi_spec_target = openapi_spec_target_ref,
        descriptor_set_target = descriptor_set_target,
        deploy_unit = deploy_unit,
        tags = ["release-metadata"],
        visibility = ["//visibility:public"],
    )

def get_release_metadata_target(app_name):
    """Get the metadata target name for an app.

    Args:
        app_name: Name of the app

    Returns:
        Target name for the app's metadata
    """
    return "//" + app_name + ":" + app_name + "_metadata"

def get_image_targets(app_name):
    """Get all image target names for an app.

    Args:
        app_name: Name of the app

    Returns:
        Dict with image target names including platform-specific push targets
    """
    base_name = app_name + "_image"
    return {
        "base": "//" + app_name + ":" + base_name,
        "push": "//" + app_name + ":" + base_name + "_push",
    }

def get_binary_targets(app_name):
    """Get binary target and platform references for a CLI app.

    Args:
        app_name: Name of the app

    Returns:
        Dict with binary target reference and platform identifiers
    """
    return {
        "target": "//" + app_name + ":" + app_name,
        "platforms": {
            "linux_x86_64": "//tools:linux_x86_64",
            "linux_arm64": "//tools:linux_arm64",
            "darwin_arm64": "//tools:darwin_arm64",
        },
    }

HelmChartMetadataInfo = provider(
    doc = "Release metadata for a helm chart, suitable for discovery via cquery.",
    fields = {
        "metadata": "dict of helm chart metadata fields (same shape as JSON output)",
    },
)

def _helm_chart_metadata_impl(ctx):
    """Implementation for helm_chart_metadata rule."""

    # app_refs is domain-qualified ("<domain>/<name>") per app, read from
    # each app_metadata target's own AppMetadataInfo provider rather than
    # inferred from the target's package path -- the path is not guaranteed
    # to match the app's declared `domain` attr, and inferring it would
    # reintroduce exactly the kind of guess this field exists to remove. See
    # //tools/app_registry/PLAN.md's AR-7a and appmeta.proto's ChartManifest
    # doc comment.
    app_names = []
    app_refs = []
    for dep in ctx.attr.apps:
        info = dep[AppMetadataInfo]
        app_names.append(info.metadata["name"])
        app_refs.append("{}/{}".format(info.metadata["domain"], info.metadata["name"]))

    # Create a JSON file with helm chart metadata
    metadata = {
        "name": ctx.attr.chart_name,
        "version": ctx.attr.chart_version,
        "namespace": ctx.attr.namespace,
        "environment": ctx.attr.environment,
        "domain": ctx.attr.domain,
        # DEPRECATED bare names -- kept for one release cycle's backward
        # compatibility. New consumers should read app_refs. See
        # ChartManifest.apps's doc comment in appmeta.proto and PLAN.md's
        # AR-7a.
        "apps": app_names,
        "chart_target": ctx.attr.chart_target,  # The actual helm_chart target
        "app_refs": app_refs,  # domain-qualified "<domain>/<name>" references
    }

    output = ctx.actions.declare_file(ctx.label.name + "_chart_metadata.json")
    ctx.actions.write(
        output = output,
        content = json.encode(metadata),
    )

    return [
        DefaultInfo(files = depset([output])),
        HelmChartMetadataInfo(metadata = metadata),
    ]

helm_chart_metadata = rule(
    implementation = _helm_chart_metadata_impl,
    attrs = {
        "chart_name": attr.string(mandatory = True),
        "chart_version": attr.string(mandatory = True),
        "namespace": attr.string(mandatory = True),
        "environment": attr.string(default = "production"),
        "domain": attr.string(mandatory = True),
        # app_metadata targets composed into this chart -- a label_list (not
        # attr.string_list) so the rule can read each app's real `domain`
        # attribute off AppMetadataInfo at analysis time instead of parsing
        # it out of a target label. See AR-7a.
        "apps": attr.label_list(mandatory = True, providers = [AppMetadataInfo]),
        "chart_target": attr.string(mandatory = True),
    },
)

def release_helm_chart(
        name,
        apps,
        chart_name = None,
        chart_version = "0.0.0-dev",
        namespace = None,
        environment = "production",
        domain = None,
        manual_manifests = [],
        **kwargs):
    """Convenience macro to set up a releasable Helm chart.

    This macro wraps helm_chart and creates release metadata for CI/CD integration.
    The actual chart name will be prefixed with "helm-{domain}-" to make artifacts
    clearly identifiable (e.g., "helm-demo-hello-fastapi").

    **Performance Optimization**: Helm chart targets are tagged with `manual`, `no_test`,
    and `helm-chart`. This prevents `bazel test //...` from building chart tarball outputs,
    avoiding unnecessary genrule executions in test runs. Charts can still be:
    - Manually built: `bazel build //demo:fastapi_chart`
    - Discovered: `bazel query "kind('helm_chart', //)"`
    - Released: Release system queries with tag filters find them automatically

    Args:
        name: Target name for the chart
        apps: List of app_metadata targets to include (e.g., ["//demo/hello_python:hello-python_metadata"])
        chart_name: Base name of the Helm chart (defaults to name, MUST use dashes not underscores).
                   Will be prefixed with "helm-{domain}-" automatically.
        chart_version: Version for local builds (default: "0.0.0-dev").
                      This is overridden during release by auto-versioning from git tags.
                      Only affects local/development builds.
        namespace: Kubernetes namespace for the chart
        environment: Target environment (development, staging, production)
        domain: Domain/category for the chart (e.g., "demo", "api", required)
        manual_manifests: List of k8s_manifests targets or direct YAML files
        **kwargs: Additional arguments passed to helm_chart

    Example:
        release_helm_chart(
            name = "fastapi_chart",
            chart_name = "hello-fastapi",  # Will become "helm-demo-hello-fastapi"
            namespace = "demo",
            domain = "demo",
            apps = ["//demo/hello_fastapi:hello-fastapi_metadata"],
        )
    """
    if not domain:
        fail("domain is required for release_helm_chart")

    if not namespace:
        fail("namespace is required for release_helm_chart")

    # Validate chart_name format - must use dashes, not underscores
    base_chart_name = chart_name or name
    if "_" in base_chart_name:
        fail("Chart name '{}' contains underscores. Use dashes instead (e.g., 'my-chart' not 'my_chart')".format(base_chart_name))

    # Construct the actual chart name with helm-domain- prefix
    # This makes chart artifacts clearly identifiable (e.g., helm-demo-hello-fastapi)
    actual_chart_name = "helm-{}-{}".format(domain, base_chart_name)

    # Create the helm_chart target
    # Tags: manual - not built by default, no_test - excludes from test runs, helm-chart - for discovery
    helm_chart(
        name = name,
        apps = apps,
        chart_name = actual_chart_name,
        chart_version = chart_version,
        namespace = namespace,
        environment = environment,
        manual_manifests = manual_manifests,
        tags = ["manual", "no_test", "helm-chart"],
        **kwargs
    )

    # Create release metadata for the chart. `apps` is passed straight
    # through as the label list of app_metadata targets -- helm_chart_metadata
    # reads each app's real `domain` attr off its AppMetadataInfo provider to
    # emit domain-qualified app_refs, rather than this macro guessing the
    # domain from the target's package path. See AR-7a.
    helm_chart_metadata(
        name = name + "_chart_metadata",
        chart_name = actual_chart_name,
        chart_version = chart_version,
        namespace = namespace,
        environment = environment,
        domain = domain,
        apps = apps,
        chart_target = ":" + name,
        tags = ["helm-release-metadata", "manual", "no_test"],
        visibility = ["//visibility:public"],
    )
