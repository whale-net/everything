#!/usr/bin/env python3
"""
Simple values.yaml generator for helm_chart_native.

This replaces the complex Go renderer with a simple Python script
that generates values.yaml from app metadata and custom values.
"""

import argparse
import json
import sys
from pathlib import Path


def load_app_metadata(metadata_file):
    """Load app metadata from JSON file."""
    try:
        with open(metadata_file, 'r') as f:
            return json.load(f)
    except Exception as e:
        print(f"Error loading {metadata_file}: {e}", file=sys.stderr)
        return None


def parse_job_config(job_str):
    """Parse job configuration from string format 'name:key=val,key=val'."""
    if ':' not in job_str:
        return {"name": job_str}
    
    name, config_str = job_str.split(':', 1)
    config = {"name": name}
    
    if config_str:
        for pair in config_str.split(','):
            if '=' in pair:
                key, value = pair.split('=', 1)
                # Support nested keys like image.repository
                if '.' in key:
                    parts = key.split('.')
                    current = config
                    for part in parts[:-1]:
                        if part not in current:
                            current[part] = {}
                        current = current[part]
                    current[parts[-1]] = value
                else:
                    config[key] = value
    
    return config


def generate_values_yaml(chart_name, domain, app_metadatas, custom_values, jobs):
    """Generate values.yaml content."""
    
    values = {
        "domain": domain,
        "global": {
            "env": "dev",
            "imageRegistry": "ghcr.io/whale-net"
        },
        "apps": {},
        "ingress": {
            "enabled": False,
            "className": "nginx",
            "annotations": {},
            "hosts": [],
            "tls": []
        },
        "service": {
            "type": "ClusterIP"
        },
        "jobs": jobs
    }
    
    # Process app metadata
    for metadata in app_metadatas:
        if not metadata:
            continue
            
        app_name = metadata.get("name", "unknown")
        
        # Extract image information
        image_repo = metadata.get("image_name", f"ghcr.io/whale-net/{domain}-{app_name}")
        image_tag = metadata.get("version", "latest")
        
        # Default app configuration
        app_config = {
            "enabled": True,
            "replicas": 1,
            "image": {
                "repository": image_repo,
                "tag": image_tag,
                "pullPolicy": "IfNotPresent"
            },
            "service": {
                "enabled": True,
                "type": "ClusterIP",
                "port": 8000
            },
            "healthcheck": {
                "enabled": True,
                "path": "/health",
                "initialDelaySeconds": 30,
                "periodSeconds": 10
            },
            "resources": {
                "requests": {
                    "memory": "128Mi",
                    "cpu": "100m"
                },
                "limits": {
                    "memory": "512Mi", 
                    "cpu": "500m"
                }
            },
            "env": {},
            "autoscaling": {
                "enabled": False
            }
        }
        
        values["apps"][app_name] = app_config
    
    # Apply custom values (simple dot-notation support)
    for key, value in custom_values.items():
        parts = key.split('.')
        current = values
        for part in parts[:-1]:
            if part not in current:
                current[part] = {}
            current = current[part]
        current[parts[-1]] = value
    
    return values


def format_yaml_value(value, indent=0):
    """Format a value as YAML with proper indentation."""
    spaces = "  " * indent
    
    if isinstance(value, dict):
        if not value:
            return "{}"
        lines = []
        for k, v in value.items():
            if isinstance(v, (dict, list)) and v:
                lines.append(f"{spaces}{k}:")
                lines.append(format_yaml_value(v, indent + 1))
            else:
                lines.append(f"{spaces}{k}: {format_yaml_value(v, 0)}")
        return "\n".join(lines)
    
    elif isinstance(value, list):
        if not value:
            return "[]"
        lines = []
        for item in value:
            if isinstance(item, (dict, list)) and item:
                lines.append(f"{spaces}-")
                item_yaml = format_yaml_value(item, indent + 1)
                # Adjust first line of item to be inline with dash
                item_lines = item_yaml.split('\n')
                if item_lines:
                    lines[-1] += f" {item_lines[0].strip()}"
                    lines.extend(item_lines[1:])
            else:
                lines.append(f"{spaces}- {format_yaml_value(item, 0)}")
        return "\n".join(lines)
    
    elif isinstance(value, bool):
        return "true" if value else "false"
    
    elif isinstance(value, str):
        # Quote strings that need it
        if any(c in value for c in [' ', ':', '{', '}', '[', ']', '@', '`', '|', '>', '#']):
            return f'"{value}"'
        return value
    
    else:
        return str(value)


def write_values_yaml(values, output_file):
    """Write values as YAML to output file."""
    
    # Generate header comment
    yaml_content = f"""# Generated values.yaml for {values['domain']} domain
# This file is automatically generated by helm_chart_native.bzl

"""
    
    # Generate YAML content
    yaml_content += format_yaml_value(values)
    yaml_content += "\n"
    
    # Write to file
    with open(output_file, 'w') as f:
        f.write(yaml_content)


def main():
    parser = argparse.ArgumentParser(description='Generate values.yaml for helm_chart_native')
    parser.add_argument('--chart-name', required=True, help='Chart name')
    parser.add_argument('--domain', required=True, help='Domain name') 
    parser.add_argument('--output', required=True, help='Output values.yaml file')
    parser.add_argument('--app-metadata', action='append', default=[], help='App metadata JSON files')
    parser.add_argument('--value', action='append', default=[], help='Custom values in key=value format')
    parser.add_argument('--job', action='append', default=[], help='Job configurations')
    
    args = parser.parse_args()
    
    # Load app metadata
    app_metadatas = []
    for metadata_file in args.app_metadata:
        metadata = load_app_metadata(metadata_file)
        if metadata:
            app_metadatas.append(metadata)
    
    # Parse custom values
    custom_values = {}
    for value_str in args.value:
        if '=' in value_str:
            key, value = value_str.split('=', 1)
            custom_values[key] = value
    
    # Parse job configurations  
    jobs = []
    for job_str in args.job:
        job_config = parse_job_config(job_str)
        jobs.append(job_config)
    
    # Generate values
    values = generate_values_yaml(
        args.chart_name,
        args.domain, 
        app_metadatas,
        custom_values,
        jobs
    )
    
    # Write output
    write_values_yaml(values, args.output)
    print(f"Generated {args.output} with {len(app_metadatas)} apps and {len(jobs)} jobs")


if __name__ == '__main__':
    main()