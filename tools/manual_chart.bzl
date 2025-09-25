"""Manual Helm chart packaging for existing chart directories."""

def _manual_helm_chart_impl(ctx):
    """Implementation for manual_helm_chart rule."""
    
    chart_name = ctx.attr.chart_name or ctx.attr.name
    
    # Copy all chart files to output directory
    all_files = []
    for src in ctx.files.srcs:
        # Calculate relative path within the chart
        src_path = src.path
        if ctx.attr.source_dir:
            # Remove source_dir prefix to get relative path
            source_prefix = ctx.attr.source_dir + "/"
            if src_path.startswith(source_prefix):
                rel_path = src_path[len(source_prefix):]
            else:
                rel_path = src.basename
        else:
            rel_path = src.basename
            
        output_file = ctx.actions.declare_file(chart_name + "/" + rel_path)
        
        ctx.actions.run_shell(
            inputs = [src],
            outputs = [output_file],
            command = "mkdir -p $(dirname {}) && cp {} {}".format(output_file.path, src.path, output_file.path)
        )
        all_files.append(output_file)
    
    return [DefaultInfo(files = depset(all_files))]

manual_helm_chart = rule(
    implementation = _manual_helm_chart_impl,
    attrs = {
        "chart_name": attr.string(
            doc = "Name of the helm chart (defaults to rule name)"
        ),
        "srcs": attr.label_list(
            allow_files = True,
            mandatory = True,
            doc = "Chart source files"
        ),
        "source_dir": attr.string(
            doc = "Source directory path to strip from file paths"
        ),
    },
    doc = """
    Package a manual Helm chart from existing files.
    
    This rule takes existing chart files and packages them in a format
    compatible with the helm chart CI pipeline.
    
    Example:
        manual_helm_chart(
            name = "host_chart", 
            chart_name = "manman-host",
            srcs = glob(["charts/manman-host/**/*"]),
            source_dir = "charts/manman-host",
        )
    """
)