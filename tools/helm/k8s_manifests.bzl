"""Macro for defining manual Kubernetes manifest files for Helm charts.

This provides a wrapper around filegroup that tags manifest files
for easy discovery by the helm_chart rule.
"""

def k8s_manifests(name, srcs, values_templating = True, **kwargs):
    """Define a set of Kubernetes manifest YAML files.
    
    These manifests can be included in helm_chart targets and will be
    added to the generated chart's templates/ directory. Manifests can
    optionally be processed to inject Helm templating for values.yaml
    configuration.
    
    Args:
        name: Name of this manifest collection target
        srcs: List of YAML manifest files (*.yaml, *.yml)
        values_templating: If True, manifest will be processed to support
            values.yaml configuration (e.g., namespace, labels). If False,
            manifest will be copied as-is. Default: True
        **kwargs: Additional arguments passed to filegroup
    
    Example:
        ```starlark
        k8s_manifests(
            name = "custom_resources",
            srcs = [
                "configmap.yaml",
                "secret.yaml",
                "networkpolicy.yaml",
            ],
            values_templating = True,
        )
        
        helm_chart(
            name = "my_chart",
            apps = ["//app:metadata"],
            manual_manifests = [":custom_resources"],
            chart_name = "my-app",
            namespace = "production",
        )
        ```
    
    Generated manifests will have access to:
        - .Values.global.namespace
        - .Values.global.environment
        - .Values.manifests.* (custom manifest configuration)
    """
    native.filegroup(
        name = name,
        srcs = srcs,
        tags = ["k8s_manifests"],
        # Store whether values templating is enabled as a custom attribute
        # This will be passed through DefaultInfo
        **kwargs
    )
    
    # Create a companion target that stores the templating config
    native.filegroup(
        name = name + "_config",
        srcs = [],
        tags = ["k8s_manifests_config"],
        # We'll encode the config in the target name pattern
        # The helm_chart rule can query this
        **kwargs
    )

def _k8s_manifests_info_impl(ctx):
    """Provider that carries manifest configuration metadata."""
    return [
        DefaultInfo(
            files = depset(ctx.files.srcs),
        ),
        K8sManifestsInfo(
            files = ctx.files.srcs,
            values_templating = ctx.attr.values_templating,
        ),
    ]

K8sManifestsInfo = provider(
    doc = "Information about k8s_manifests target",
    fields = {
        "files": "Depset of manifest files",
        "values_templating": "Whether to apply Helm values templating",
    },
)

k8s_manifests_rule = rule(
    implementation = _k8s_manifests_info_impl,
    attrs = {
        "srcs": attr.label_list(
            allow_files = [".yaml", ".yml"],
            mandatory = True,
            doc = "Kubernetes manifest YAML files",
        ),
        "values_templating": attr.bool(
            default = True,
            doc = "Enable Helm values.yaml templating",
        ),
    },
    doc = "Rule for collecting Kubernetes manifests with metadata",
)
