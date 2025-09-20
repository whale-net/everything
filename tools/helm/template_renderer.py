#!/usr/bin/env python3
"""Template renderer for Helm chart files.

This utility renders Helm chart templates using simple string substitution.
It's designed to replace the string building logic in helm_chart_release.bzl.
"""

import argparse
import json
import sys
from pathlib import Path
from string import Template


def render_chart_yaml(template_content: str, context: dict) -> str:
    """Render Chart.yaml template with the provided context."""
    template = Template(template_content)
    return template.safe_substitute(context)


def load_sub_template(template_dir: Path, template_name: str) -> Template:
    """Load a sub-template from the templates directory."""
    template_path = template_dir / template_name
    if not template_path.exists():
        raise FileNotFoundError(f"Sub-template not found: {template_path}")
    return Template(template_path.read_text())


def render_values_yaml(template_content: str, context: dict, template_dir: Path) -> str:
    """Render values.yaml template with the provided context."""
    
    # Load sub-templates
    image_config_template = load_sub_template(template_dir, "image_config.template")
    app_config_template = load_sub_template(template_dir, "app_config.template")
    
    # Render image configs section
    image_configs = []
    for app_name in context.get('apps', []):
        registry = "ghcr.io"
        repo_name = f"{context['domain']}-{app_name}"
        image_config = image_config_template.safe_substitute({
            'app_name': app_name,
            'registry': registry,
            'repo_name': repo_name
        })
        image_configs.append(image_config)
    
    # Render app configs section
    app_configs = []
    for app_name in context.get('apps', []):
        app_config = app_config_template.safe_substitute({
            'app_name': app_name
        })
        app_configs.append(app_config)
    overrides_section = ""
    overrides = context.get('overrides', {})
    if overrides:
        overrides_section = "\n# User overrides:\n"
        for key, value in overrides.items():
            overrides_section += f"# {key}: {value}\n"
    
    # Update context with rendered sections
    context = dict(context)
    context['image_configs'] = '\n'.join(image_configs)
    context['app_configs'] = '\n'.join(app_configs)
    context['overrides_section'] = overrides_section
    
    template = Template(template_content)
    return template.safe_substitute(context)


def main():
    parser = argparse.ArgumentParser(description='Render Helm chart templates')
    parser.add_argument('--template', required=True, help='Template file path')
    parser.add_argument('--context', required=True, help='JSON context for template variables')
    parser.add_argument('--output', required=True, help='Output file path')
    parser.add_argument('--type', choices=['chart', 'values'], required=True,
                        help='Type of template to render')
    
    args = parser.parse_args()
    
    try:
        # Read template file
        template_path = Path(args.template)
        if not template_path.exists():
            print(f"Error: Template file not found: {args.template}", file=sys.stderr)
            sys.exit(1)
        
        template_content = template_path.read_text()
        template_dir = template_path.parent
        
        # Parse context JSON
        try:
            context = json.loads(args.context)
        except json.JSONDecodeError as e:
            print(f"Error: Invalid JSON context: {e}", file=sys.stderr)
            sys.exit(1)
        
        # Render template based on type
        if args.type == 'chart':
            rendered = render_chart_yaml(template_content, context)
        elif args.type == 'values':
            rendered = render_values_yaml(template_content, context, template_dir)
        else:
            print(f"Error: Unknown template type: {args.type}", file=sys.stderr)
            sys.exit(1)
        
        # Write output
        output_path = Path(args.output)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        output_path.write_text(rendered)
        
        print(f"Template rendered successfully: {args.output}")
        
    except Exception as e:
        print(f"Error rendering template: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()