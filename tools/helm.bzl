"""Helm chart generation and packaging utilities for the Everything monorepo."""

load("@bazel_skylib//lib:shell.bzl", "shell")

def _helm_chart_impl(ctx):
    """Implementation for helm_chart rule."""
    # Metadata for substitution
    metadata = {
        "APP_NAME": ctx.attr.app_name,
        "DESCRIPTION": ctx.attr.description,
        "CHART_VERSION": ctx.attr.chart_version,
        "APP_VERSION": ctx.attr.app_version,
        "DOMAIN": ctx.attr.domain,
        "LANGUAGE": ctx.attr.language,
        "IMAGE_REPO": ctx.attr.image_repo,
    }
    
    # Template files to process
    template_files = [
        ("Chart.yaml.tpl", "Chart.yaml"),
        ("values.yaml.tpl", "values.yaml"),
        ("deployment.yaml.tpl", "templates/deployment.yaml"),
        ("service.yaml.tpl", "templates/service.yaml"),
        ("serviceaccount.yaml.tpl", "templates/serviceaccount.yaml"),
        ("ingress.yaml.tpl", "templates/ingress.yaml"),
        ("_helpers.tpl.tpl", "templates/_helpers.tpl"),
    ]
    
    # Create chart directory structure
    chart_dir = ctx.actions.declare_directory(ctx.attr.app_name)
    
    # Generate substitution script
    substitution_commands = []
    for template_file, output_file in template_files:
        # Use the template files from the filegroup
        template_input = None
        for template in ctx.files.template_files:
            if template.basename == template_file:
                template_input = template
                break
        
        if not template_input:
            fail(f"Template file not found: {template_file}")
        
        output_path = chart_dir.path + "/" + output_file
        
        # Create directory if needed
        output_dir = output_path.rsplit("/", 1)[0]
        substitution_commands.append("mkdir -p " + shell.quote(output_dir))
        
        # Generate sed command for substitutions
        sed_cmd = "sed"
        for key, value in metadata.items():
            sed_cmd += " -e 's/{{" + key + "}}/" + shell.quote(value) + "/g'"
        sed_cmd += " " + shell.quote(template_input.path) + " > " + shell.quote(output_path)
        
        substitution_commands.append(sed_cmd)
    
    # Write and execute the script
    script = "\n".join(substitution_commands)
    
    ctx.actions.run_shell(
        outputs = [chart_dir],
        inputs = ctx.files.template_files,
        command = script,
        mnemonic = "HelmChartGenerate",
        progress_message = "Generating Helm chart for " + ctx.attr.app_name,
    )
    
    return [DefaultInfo(files = depset([chart_dir]))]

helm_chart = rule(
    implementation = _helm_chart_impl,
    attrs = {
        "app_name": attr.string(mandatory = True),
        "description": attr.string(mandatory = True),
        "chart_version": attr.string(mandatory = True),
        "app_version": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "language": attr.string(mandatory = True),
        "image_repo": attr.string(mandatory = True),
        "template_files": attr.label_list(allow_files = [".tpl"], mandatory = True),
    },
)

def _helm_package_impl(ctx):
    """Implementation for helm_package rule."""
    chart_dir = ctx.file.chart_dir
    # Use domain-app naming pattern for package file
    package_filename = ctx.attr.chart_name + "-" + ctx.attr.chart_version + ".tgz"
    output_tgz = ctx.actions.declare_file(package_filename)
    
    # Package the chart using tar (helm package equivalent)
    ctx.actions.run_shell(
        outputs = [output_tgz],
        inputs = [chart_dir],
        command = "cd {} && tar -czf {} {}".format(
            chart_dir.dirname,
            output_tgz.path,
            chart_dir.basename
        ),
        mnemonic = "HelmPackage",
        progress_message = "Packaging Helm chart " + ctx.attr.chart_name,
    )
    
    return [DefaultInfo(files = depset([output_tgz]))]

helm_package = rule(
    implementation = _helm_package_impl,
    attrs = {
        "chart_dir": attr.label(allow_single_file = False, mandatory = True),
        "chart_name": attr.string(mandatory = True),
        "chart_version": attr.string(mandatory = True),
    },
)

def _helm_index_impl(ctx):
    """Implementation for helm_index rule."""
    charts = ctx.files.charts
    index_yaml = ctx.actions.declare_file("index.yaml")
    
    # Generate a simple index.yaml file
    # In a real implementation, this would parse chart metadata
    index_content = """apiVersion: v1
entries: {}
generated: "2024-01-01T00:00:00Z"
"""
    
    ctx.actions.write(
        output = index_yaml,
        content = index_content,
    )
    
    return [DefaultInfo(files = depset([index_yaml] + charts))]

helm_index = rule(
    implementation = _helm_index_impl,
    attrs = {
        "charts": attr.label_list(allow_files = [".tgz"]),
    },
)

def release_helm_chart(
    name, 
    app_name, 
    description, 
    chart_version, 
    app_version, 
    domain, 
    language, 
    image_repo,
    template_files = "//tools/charts:templates"):
    """Generate and package a Helm chart for an app.
    
    Args:
        name: Base name for targets
        app_name: Application name
        description: Chart description
        chart_version: Helm chart version
        app_version: Application version (usually matches container image tag)
        domain: Application domain
        language: Programming language
        image_repo: Container image repository
        template_files: Label list of template files
    """
    
    # Generate the chart
    helm_chart(
        name = name + "_chart",
        app_name = app_name,
        description = description,
        chart_version = chart_version,
        app_version = app_version,
        domain = domain,
        language = language,
        image_repo = image_repo,
        template_files = template_files,
    )
    
    # Package the chart using domain-app naming pattern
    helm_package(
        name = name + "_package",
        chart_dir = ":" + name + "_chart",
        chart_name = domain + "-" + app_name,  # Use domain-app pattern
        chart_version = chart_version,
    )

def _composite_helm_chart_impl(ctx):
    """Implementation for composite_helm_chart rule."""
    # Simplified implementation - just create a basic composite chart structure
    # In a full implementation, this would parse app metadata and generate
    # per-app sections dynamically
    
    # Basic metadata for substitution
    metadata = {
        "COMPOSITE_NAME": ctx.attr.composite_name,
        "DESCRIPTION": ctx.attr.description,
        "CHART_VERSION": ctx.attr.chart_version,
        "DOMAIN": ctx.attr.domain,
        "GLOBAL_REGISTRY": ctx.attr.global_registry,
        "APP_LIST": ",".join([app.label.name.replace("_metadata", "") for app in ctx.attr.apps]),
    }
    
    # Template files to process for composite charts
    template_files = [
        ("composite-Chart.yaml.tpl", "Chart.yaml"),
        ("composite-values.yaml.tpl", "values.yaml"),
        ("composite-deployment.yaml.tpl", "templates/deployment.yaml"),
        ("composite-service.yaml.tpl", "templates/service.yaml"),
        ("composite-serviceaccount.yaml.tpl", "templates/serviceaccount.yaml"),
        ("composite-ingress.yaml.tpl", "templates/ingress.yaml"),
        ("composite-_helpers.tpl.tpl", "templates/_helpers.tpl"),
    ]
    
    # Create chart directory structure
    chart_dir = ctx.actions.declare_directory(ctx.attr.composite_name)
    
    # Generate substitution script (simplified version)
    substitution_commands = []
    for template_file, output_file in template_files:
        # Find the template file
        template_input = None
        for template in ctx.files.template_files:
            if template.basename == template_file:
                template_input = template
                break
        
        if not template_input:
            fail("Template file not found: " + template_file)
        
        output_path = chart_dir.path + "/" + output_file
        
        # Create directory if needed
        output_dir = output_path.rsplit("/", 1)[0]
        substitution_commands.append("mkdir -p " + shell.quote(output_dir))
        
        # Simple substitution
        sed_cmd = "sed"
        for key, value in metadata.items():
            sed_cmd += " -e 's/{{" + key + "}}/" + shell.quote(str(value)) + "/g'"
        sed_cmd += " " + shell.quote(template_input.path) + " > " + shell.quote(output_path)
        
        substitution_commands.append(sed_cmd)
    
    # Write and execute the script
    script = "\n".join(substitution_commands)
    
    ctx.actions.run_shell(
        outputs = [chart_dir],
        inputs = ctx.files.template_files + [f for target in ctx.attr.apps for f in target.files.to_list()],
        command = script,
        mnemonic = "CompositeHelmChartGenerate",
        progress_message = "Generating composite Helm chart " + ctx.attr.composite_name,
    )
    
    return [DefaultInfo(files = depset([chart_dir]))]

composite_helm_chart = rule(
    implementation = _composite_helm_chart_impl,
    attrs = {
        "composite_name": attr.string(mandatory = True),
        "description": attr.string(mandatory = True),
        "chart_version": attr.string(mandatory = True),
        "domain": attr.string(mandatory = True),
        "global_registry": attr.string(default = "ghcr.io"),
        "apps": attr.label_list(mandatory = True),  # List of app metadata targets
        "template_files": attr.label_list(allow_files = [".tpl"], mandatory = True),
    },
)

def release_composite_helm_chart(
    name,
    composite_name,
    description,
    chart_version,
    domain,
    apps,  # List of app names
    global_registry = "ghcr.io",
    template_files = "//tools/charts:composite_templates"):
    """Generate and package a composite Helm chart for multiple apps.
    
    Args:
        name: Base name for targets
        composite_name: Name of the composite chart
        description: Chart description
        chart_version: Helm chart version
        domain: Application domain
        apps: List of app names to include in the composite chart
        global_registry: Container registry for all apps
        template_files: Label list of template files
    """
    
    # Convert app names to metadata targets
    app_targets = []
    for app in apps:
        app_targets.append("//" + app + ":" + app + "_metadata")
    
    # Generate the composite chart
    composite_helm_chart(
        name = name + "_chart",
        composite_name = composite_name,
        description = description,
        chart_version = chart_version,
        domain = domain,
        global_registry = global_registry,
        apps = app_targets,
        template_files = template_files,
    )
    
    # Package the composite chart
    helm_package(
        name = name + "_package",
        chart_dir = ":" + name + "_chart",
        chart_name = composite_name,
        chart_version = chart_version,
    )