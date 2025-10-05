"""
Image building and tagging utilities for the release helper.
"""

import os
import subprocess
from typing import Dict, List, Optional

from tools.release_helper.metadata import get_image_targets, get_app_metadata
from tools.release_helper.core import run_bazel


def create_manifest_list(registry_repo: str, version: str, platforms: List[str] = None) -> None:
    """Create a Docker manifest list for multi-architecture images.
    
    This creates a manifest that automatically serves the correct architecture
    when users pull without specifying a platform.
    
    Args:
        registry_repo: Base registry repository (e.g., "ghcr.io/whale-net/demo-hello_python")
        version: Version tag to create manifest for
        platforms: List of platforms to include (defaults to ["amd64", "arm64"])
    """
    if platforms is None:
        platforms = ["amd64", "arm64"]
    
    # Create the manifest list name (without platform suffix)
    manifest_name = f"{registry_repo}:{version}"
    
    # Build list of platform-specific images
    platform_images = []
    for platform in platforms:
        platform_image = f"{registry_repo}:{version}-{platform}"
        platform_images.append(platform_image)
    
    print(f"Creating manifest list {manifest_name} for platforms: {', '.join(platforms)}")
    
    try:
        # Create the manifest list
        cmd = ["docker", "manifest", "create", manifest_name] + platform_images
        subprocess.run(cmd, check=True, capture_output=True, text=True)
        
        # Annotate each platform image with proper architecture metadata
        for platform in platforms:
            platform_image = f"{registry_repo}:{version}-{platform}"
            arch = "amd64" if platform == "amd64" else "arm64"
            os_type = "linux"
            
            annotate_cmd = [
                "docker", "manifest", "annotate", manifest_name, platform_image,
                "--arch", arch, "--os", os_type
            ]
            subprocess.run(annotate_cmd, check=True, capture_output=True, text=True)
        
        print(f"Successfully created manifest list {manifest_name}")
        
    except subprocess.CalledProcessError as e:
        print(f"Failed to create manifest list: {e}")
        if e.stdout:
            print(f"STDOUT: {e.stdout}")
        if e.stderr:
            print(f"STDERR: {e.stderr}")
        raise


def push_manifest_list(registry_repo: str, version: str) -> None:
    """Push a Docker manifest list to the registry.
    
    Args:
        registry_repo: Base registry repository
        version: Version tag to push
    """
    manifest_name = f"{registry_repo}:{version}"
    
    print(f"Pushing manifest list {manifest_name}")
    
    try:
        cmd = ["docker", "manifest", "push", manifest_name]
        subprocess.run(cmd, check=True, capture_output=True, text=True)
        print(f"Successfully pushed manifest list {manifest_name}")
        
    except subprocess.CalledProcessError as e:
        print(f"Failed to push manifest list: {e}")
        if e.stdout:
            print(f"STDOUT: {e.stdout}")
        if e.stderr:
            print(f"STDERR: {e.stderr}")
        raise


def format_registry_tags(domain: str, app_name: str, version: str, registry: str = "ghcr.io", commit_sha: Optional[str] = None, platform: Optional[str] = None) -> Dict[str, str]:
    """Format container registry tags for an app using domain-app:version format.
    
    Args:
        domain: App domain (from metadata)
        app_name: App name (from metadata)
        version: Version tag (semantic version)
        registry: Registry hostname (e.g., "ghcr.io")
        commit_sha: Optional commit SHA for additional tag
        platform: Optional platform suffix (e.g., "amd64", "arm64") for platform-specific tags
    """
    # Use domain-app:version format as specified
    image_name = f"{domain}-{app_name}"
    
    # For GHCR, include the repository owner
    if registry == "ghcr.io" and "GITHUB_REPOSITORY_OWNER" in os.environ:
        owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
        base_repo = f"{registry}/{owner}/{image_name}"
    else:
        base_repo = f"{registry}/{image_name}"

    # Create platform-specific tags if platform is specified
    platform_suffix = f"-{platform}" if platform else ""
    
    tags = {
        "latest": f"{base_repo}:latest{platform_suffix}",
        "version": f"{base_repo}:{version}{platform_suffix}",
    }

    if commit_sha:
        tags["commit"] = f"{base_repo}:{commit_sha}{platform_suffix}"

    return tags


def build_image(bazel_target: str, platform: Optional[str] = None) -> str:
    """Build and load a container image for an app using optimized oci_load targets.
    
    Args:
        bazel_target: Full bazel target path for the app metadata (e.g., "//path/to/app:app_metadata")
        platform: Optional platform specification ("amd64" or "arm64")
    """
    # Get app metadata for proper naming
    metadata = get_app_metadata(bazel_target)
    domain = metadata['domain']
    app_name = metadata['name']

    # Extract the app path from the bazel_target to construct the image target
    app_path = bazel_target[2:].split(':')[0]  # Remove // and :target
    load_target = f"//{app_path}:{app_name}_image_load"

    print(f"Building and loading {load_target} for platform {platform or 'default'} (using optimized oci_load)...")
    
    # Build with platform-specific flags if specified
    build_args = ["run", load_target]
    if platform == "arm64":
        build_args.insert(2, "--platforms=//tools:linux_arm64")
    elif platform == "amd64":
        build_args.insert(2, "--platforms=//tools:linux_x86_64")
    
    # Run the build and load command
    run_bazel(build_args)

    # Return the expected image name in domain-app format
    return f"{domain}-{app_name}:latest"


def push_image_with_tags(bazel_target: str, tags: List[str]) -> None:
    """Push a container image with multiple tags to the registry.
    
    Pushes the OCI image index which contains all platform variants (amd64, arm64).
    The index allows Docker to automatically select the correct architecture.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        tags: List of full registry tags to push (e.g., ["ghcr.io/whale-net/demo-hello_python:v0.0.6"])
    """
    # Get app metadata for proper naming
    metadata = get_app_metadata(bazel_target)
    app_name = metadata['name']

    # Extract the app path from the bazel_target to construct the push target
    app_path = bazel_target[2:].split(':')[0]  # Remove // and :target
    
    # Use the image index push target (contains both amd64 and arm64)
    push_target = f"//{app_path}:{app_name}_image_push"

    print(f"Pushing {len(tags)} tags using {push_target}...")
    
    # Extract the tag names from the full tags
    tag_names = [tag.split(':')[-1] for tag in tags]
    
    print(f"Pushing with tags: {', '.join(tag_names)}")
    
    # Build the bazel run command with tag arguments
    # DO NOT add --platforms flag - oci_push must use host architecture tools
    bazel_args = ["run", push_target, "--"]
    
    # Add each tag as an argument (oci_push supports multiple --tag arguments)
    for tag_name in tag_names:
        bazel_args.extend(["--tag", tag_name])
    
    try:
        run_bazel(bazel_args, capture_output=False)  # Don't capture output so we can see progress
        print(f"Successfully pushed image with {len(tag_names)} tags")
    except subprocess.CalledProcessError as e:
        print(f"Failed to push image: {e}")
        raise


def release_multiarch_image(bazel_target: str, version: str, registry: str = "ghcr.io", 
                           platforms: List[str] = None, commit_sha: Optional[str] = None) -> None:
    """Release a multi-architecture image using OCI image index.
    
    Builds platform-specific images and pushes a single OCI image index containing
    all platform variants. The index allows Docker to automatically select the
    correct architecture when users pull.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        version: Version tag for the release
        registry: Container registry (defaults to ghcr.io)
        platforms: List of platforms to build (defaults to ["amd64", "arm64"])
        commit_sha: Optional commit SHA for additional tag
    """
    if platforms is None:
        platforms = ["amd64", "arm64"]
    
    # Get app metadata
    metadata = get_app_metadata(bazel_target)
    domain = metadata['domain']
    app_name = metadata['name']
    
    # Extract the app path from the bazel_target
    app_path = bazel_target[2:].split(':')[0]  # Remove // and :target
    
    # Create registry repository name
    image_name = f"{domain}-{app_name}"
    if registry == "ghcr.io" and "GITHUB_REPOSITORY_OWNER" in os.environ:
        owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
        registry_repo = f"{registry}/{owner}/{image_name}"
    else:
        registry_repo = f"{registry}/{image_name}"
    
    print(f"Releasing multi-architecture image: {image_name}")
    print(f"Registry: {registry_repo}")
    print(f"Version: {version}")
    print(f"Platforms: {', '.join(platforms)}")
    
    # Step 1: Build platform-specific images
    # This populates Bazel's cache with correctly cross-compiled images for each platform
    # Add action_env with unique value to bust stale cache from incorrect platform builds
    import time
    build_id = f"{version}_{int(time.time())}"
    
    print(f"\n{'='*80}")
    print("Building platform-specific images...")
    print(f"{'='*80}")
    for platform in platforms:
        platform_target = f"//{app_path}:{app_name}_image_{platform}"
        platform_flag = f"//tools:linux_{platform == 'arm64' and 'arm64' or 'x86_64'}"
        
        print(f"\nBuilding {platform} image: {platform_target}")
        build_args = [
            "build", 
            platform_target, 
            f"--platforms={platform_flag}",
            f"--action_env=RELEASE_BUILD_ID={build_id}"
        ]
        run_bazel(build_args)
        print(f"✅ Built {platform} image successfully")
    
    # Step 2: Build the OCI image index (depends on platform-specific images)
    # The oci_image_index rule will use the platform-specific images we just built
    print(f"\n{'='*80}")
    print("Building OCI image index...")
    print(f"{'='*80}")
    index_target = f"//{app_path}:{app_name}_image"
    print(f"Building index: {index_target}")
    run_bazel([
        "build", 
        index_target,
        f"--action_env=RELEASE_BUILD_ID={build_id}"
    ])
    print(f"✅ Built OCI image index containing {len(platforms)} platform variants")
    
    # Step 3: Push the image index with all tags
    # The oci_push target will push the index (NOT the individual platform images)
    tags = format_registry_tags(
        domain=domain,
        app_name=app_name,
        version=version,
        registry=registry,
        commit_sha=commit_sha,
        platform=None  # No platform suffix - this is the index
    )
    
    print(f"\n{'='*80}")
    print(f"Pushing OCI image index with {len(tags)} tags...")
    print(f"{'='*80}")
    push_image_with_tags(bazel_target, list(tags.values()))
    
    print(f"\n{'='*80}")
    print(f"✅ Successfully released {image_name}:{version}")
    print(f"{'='*80}")
    print(f"\nPublished tags:")
    for tag in tags.values():
        print(f"  - {tag}")
    print(f"\nThe image index contains {len(platforms)} platform variants: {', '.join(platforms)}")
    print(f"Docker will automatically select the correct architecture when users pull.")