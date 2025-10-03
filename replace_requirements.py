import re
import sys
import os

def transform_requirement(match):
    package = match.group(1)
    # Convert dashes to underscores for Bazel label
    bazel_name = package.replace('-', '_')
    return f'        "@pypi//{bazel_name}",'

def process_file(filepath):
    with open(filepath, 'r') as f:
        content = f.read()
    
    # Pattern to match requirement("package") with proper indentation
    pattern = r'(\s+)requirement\("([^"]+)"\),?'
    
    new_content = re.sub(pattern, transform_requirement, content)
    
    # Also remove the load statement for requirements.bzl
    new_content = re.sub(r'load\("//tools:requirements\.bzl", "requirement"\)\n', '', new_content)
    
    if new_content != content:
        with open(filepath, 'w') as f:
            f.write(new_content)
        print(f"Updated {filepath}")
    else:
        print(f"No changes needed for {filepath}")

if __name__ == "__main__":
    files = [
        "./tools/release_helper/BUILD.bazel",
        "./demo/hello_internal_api/BUILD.bazel", 
        "./demo/hello_job/BUILD.bazel",
        "./demo/hello_world_test/BUILD.bazel",
        "./demo/hello_worker/BUILD.bazel",
        "./manman/src/migrations/BUILD.bazel",
        "./manman/src/worker/BUILD.bazel",
        "./manman/src/host/BUILD.bazel",
        "./manman/src/repository/BUILD.bazel"
    ]
    
    for filepath in files:
        if os.path.exists(filepath):
            process_file(filepath)
