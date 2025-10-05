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


def push_image_with_tags(bazel_target: str, tags: List[str], platform: Optional[str] = None) -> None:
    """Push a container image with multiple tags to the registry.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        tags: List of full registry tags to push (e.g., ["ghcr.io/whale-net/demo-hello_python:v0.0.6-amd64"])
        platform: Optional platform specification ("amd64" or "arm64", defaults to base/amd64)
    """
    # Get app metadata for proper naming
    metadata = get_app_metadata(bazel_target)
    app_name = metadata['name']

    # Extract the app path from the bazel_target to construct the push target
    app_path = bazel_target[2:].split(':')[0]  # Remove // and :target
    push_target = f"//{app_path}:{app_name}_image_push"

    print(f"Pushing {len(tags)} tags using {push_target} for platform {platform or 'default'}...")
    
    # Extract the tag names from the full tags
    tag_names = [tag.split(':')[-1] for tag in tags]
    
    print(f"Pushing with tags: {', '.join(tag_names)}")
    
    # Build the bazel run command with tag arguments
    bazel_args = ["run", push_target, "--"]
    
    # Add platform-specific build flags if needed
    if platform == "arm64":
        bazel_args.insert(2, "--platforms=//tools:linux_arm64")
    elif platform == "amd64":
        bazel_args.insert(2, "--platforms=//tools:linux_x86_64")
    
    # Add each tag as an argument (oci_push supports multiple --tag arguments)
    for tag_name in tag_names:
        bazel_args.extend(["--tag", tag_name])
    
    try:
        run_bazel(bazel_args, capture_output=False)  # Don't capture output so we can see progress
        print(f"Successfully pushed image with {len(tag_names)} tags for platform {platform or 'default'}")
    except subprocess.CalledProcessError as e:
        print(f"Failed to push image: {e}")
        raise


def release_multiarch_image(bazel_target: str, version: str, registry: str = "ghcr.io", 
                           platforms: List[str] = None, commit_sha: Optional[str] = None) -> None:
    """Release a multi-architecture image with manifest list.
    
    This function orchestrates the complete multi-architecture release process:
    1. Build platform-specific images locally
    2. Push platform-specific images to temporary tags
    3. Create manifest lists that point to all platforms
    4. Push manifest lists (these are what users pull)
    5. Clean up temporary platform-specific tags
    
    NOTE: Only manifest lists are published to keep the registry clean.
    Users pull app:v1.0.0 and Docker automatically selects the right platform.
    
    Args:
        bazel_target: Full bazel target path for the app metadata
        version: Version tag for the release
        registry: Container registry (defaults to ghcr.io)
        platforms: List of platforms to build for (defaults to ["amd64", "arm64"])
        commit_sha: Optional commit SHA for additional tag
    """
    if platforms is None:
        platforms = ["amd64", "arm64"]
    
    # Get app metadata
    metadata = get_app_metadata(bazel_target)
    domain = metadata['domain']
    app_name = metadata['name']
    
    # Create registry repository name
    image_name = f"{domain}-{app_name}"
    if registry == "ghcr.io" and "GITHUB_REPOSITORY_OWNER" in os.environ:
        owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
        registry_repo = f"{registry}/{owner}/{image_name}"
    else:
        registry_repo = f"{registry}/{image_name}"
    
    print(f"Starting multi-architecture release for {image_name}...")
    print(f"Registry: {registry_repo}")
    print(f"Platforms: {', '.join(platforms)}")
    
    # Step 1: Build and push platform-specific images with temporary tags
    # These will be used to create the manifest, then can be cleaned up
    print("\n=== Building and pushing platform-specific images ===")
    for platform in platforms:
        print(f"\nBuilding for {platform}...")
        
        # Build the image for this platform
        build_image(bazel_target, platform=platform)
        
        # Create platform-specific tags (these are temporary for manifest creation)
        platform_tags = format_registry_tags(
            domain=domain,
            app_name=app_name, 
            version=version,
            registry=registry,
            commit_sha=commit_sha,
            platform=platform
        )
        
        print(f"Pushing {platform} images (temporary tags for manifest creation)...")
        # Push with platform-specific tags
        push_image_with_tags(bazel_target, list(platform_tags.values()), platform=platform)
    
    # Step 2: Create and push manifest lists for each tag type
    # These are the ONLY tags that users will see/pull
    print("\n=== Creating and pushing manifest lists ===")
    tag_types = ["latest", "version"]
    if commit_sha:
        tag_types.append("commit")
    
    published_manifests = []
    for tag_type in tag_types:
        if tag_type == "version":
            tag_value = version
        elif tag_type == "latest":
            tag_value = "latest"
        elif tag_type == "commit":
            tag_value = commit_sha
        
        manifest_tag = f"{registry_repo}:{tag_value}"
        print(f"\nCreating manifest list: {manifest_tag}")
        
        # Create manifest list (without platform suffix)
        create_manifest_list(registry_repo, tag_value, platforms)
        
        # Push manifest list
        push_manifest_list(registry_repo, tag_value)
        
        published_manifests.append(manifest_tag)
    
    print(f"\n{'='*80}")
    print(f"✅ Successfully released multi-architecture image {image_name}:{version}")
    print(f"{'='*80}")
    print(f"\nPublished manifest lists (auto-select architecture):")
    for manifest in published_manifests:
        print(f"  - {manifest}")
    print(f"\nUsers can pull: {registry_repo}:{version}")
    print(f"Docker will automatically select the correct architecture.")
    print(f"\nNote: Platform-specific tags ({version}-amd64, {version}-arm64) are temporary")
    print(f"      and will be cleaned up by registry garbage collection.")