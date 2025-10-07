"""
Image building and tagging utilities for the release helper.
"""

import os
import subprocess
import sys
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


def tag_existing_image(source_tag: str, target_tags: List[str]) -> None:
    """Tag an existing image with additional tags and push them.
    
    This is used to re-tag an existing image (e.g., a commit-tagged image)
    with additional tags (e.g., version and latest) without rebuilding.
    
    Args:
        source_tag: Full registry tag of the existing image (e.g., "ghcr.io/owner/repo:commit")
        target_tags: List of full registry tags to apply (e.g., ["ghcr.io/owner/repo:v1.0.0", "ghcr.io/owner/repo:latest"])
    """
    print(f"Tagging existing image {source_tag} with {len(target_tags)} additional tags...")
    
    # Extract just the tag names for display
    tag_names = [tag.split(':')[-1] for tag in target_tags]
    print(f"Additional tags: {', '.join(tag_names)}")
    
    try:
        # Use docker buildx imagetools to create new tags for the existing image
        # This operation doesn't download or rebuild the image - it just creates new manifest references
        for target_tag in target_tags:
            cmd = ["docker", "buildx", "imagetools", "create", "--tag", target_tag, source_tag]
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                check=True
            )
            print(f"✅ Tagged with {target_tag.split(':')[-1]}")
        
        print(f"Successfully tagged existing image with {len(target_tags)} tags")
    except subprocess.CalledProcessError as e:
        print(f"Failed to tag existing image: {e}", file=sys.stderr)
        if e.stdout:
            print(f"STDOUT: {e.stdout}", file=sys.stderr)
        if e.stderr:
            print(f"STDERR: {e.stderr}", file=sys.stderr)
        raise
    except FileNotFoundError:
        # docker buildx not available
        print("Warning: docker buildx not available, falling back to manual tagging", file=sys.stderr)
        # Fall back to pulling, tagging and pushing (slower but works without buildx)
        _tag_existing_image_fallback(source_tag, target_tags)


def _tag_existing_image_fallback(source_tag: str, target_tags: List[str]) -> None:
    """Fallback method to tag an existing image when buildx is not available.
    
    This method pulls the image, tags it locally, and pushes the new tags.
    This is slower but works without docker buildx.
    """
    print("Using fallback method (pull, tag, push)...")
    
    try:
        # Pull the source image
        print(f"Pulling {source_tag}...")
        subprocess.run(
            ["docker", "pull", source_tag],
            check=True,
            capture_output=True,
            text=True
        )
        
        # Tag it with each target tag and push
        for target_tag in target_tags:
            print(f"Tagging and pushing {target_tag.split(':')[-1]}...")
            subprocess.run(
                ["docker", "tag", source_tag, target_tag],
                check=True,
                capture_output=True,
                text=True
            )
            subprocess.run(
                ["docker", "push", target_tag],
                check=True,
                capture_output=True,
                text=True
            )
            print(f"✅ Pushed {target_tag.split(':')[-1]}")
        
        print(f"Successfully tagged and pushed {len(target_tags)} tags using fallback method")
    except subprocess.CalledProcessError as e:
        print(f"Failed to tag existing image (fallback): {e}", file=sys.stderr)
        if e.stdout:
            print(f"STDOUT: {e.stdout}", file=sys.stderr)
        if e.stderr:
            print(f"STDERR: {e.stderr}", file=sys.stderr)
        raise


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
    
    Optimization: If a commit-tagged image already exists in the registry, this function
    will re-tag that existing image instead of rebuilding. This minimizes build times
    when releasing the same commit multiple times with different version tags.
    
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
    
    # Generate registry tags
    tags = format_registry_tags(
        domain=domain,
        app_name=app_name,
        version=version,
        registry=registry,
        commit_sha=commit_sha,
        platform=None  # No platform suffix - this is the index
    )
    
    # Check if we can optimize by re-tagging an existing commit image
    should_rebuild = True
    commit_tag_ref = None
    
    if commit_sha:
        # Check if an image with the commit tag already exists
        from tools.release_helper.validation import check_image_exists_in_registry
        commit_tag_ref = tags.get("commit")
        if commit_tag_ref and check_image_exists_in_registry(commit_tag_ref):
            print(f"✅ Found existing image for commit {commit_sha[:7]}: {commit_tag_ref}")
            print("Optimizing: Re-tagging existing image instead of rebuilding")
            should_rebuild = False
        else:
            print(f"No existing image found for commit {commit_sha[:7]}, will build")
    
    if should_rebuild:
        # Build the OCI image index with platform transitions
        # The oci_image_index rule with platforms parameter will automatically:
        # 1. Build the base image for each platform via Bazel transitions
        # 2. Create a proper OCI index manifest with platform metadata
        print(f"\n{'='*80}")
        print("Building OCI image index with platform transitions...")
        print(f"{'='*80}")
        index_target = f"//{app_path}:{app_name}_image"
        print(f"Building index: {index_target}")
        run_bazel([
            "build", 
            index_target
        ])
        print(f"✅ Built OCI image index containing {len(platforms)} platform variants")

        
        # Push the image index with all tags
        # The oci_push target will push the index with proper multi-arch manifest
        print(f"\n{'='*80}")
        print(f"Pushing OCI image index with {len(tags)} tags...")
        print(f"{'='*80}")
        push_image_with_tags(bazel_target, list(tags.values()))
    else:
        # Re-tag existing commit image with version and latest tags
        print(f"\n{'='*80}")
        print("Re-tagging existing image...")
        print(f"{'='*80}")
        
        # Get tags to apply (exclude the commit tag since it already exists)
        additional_tags = [tag for key, tag in tags.items() if key != "commit"]
        
        try:
            tag_existing_image(commit_tag_ref, additional_tags)
            print(f"Successfully tagged {app_name} {version} from existing commit image")
        except Exception as e:
            print(f"Failed to tag existing image, falling back to rebuild: {e}", file=sys.stderr)
            print("Rebuilding image...")
            
            # Fall back to building and pushing
            index_target = f"//{app_path}:{app_name}_image"
            print(f"Building index: {index_target}")
            run_bazel([
                "build", 
                index_target
            ])
            print(f"✅ Built OCI image index containing {len(platforms)} platform variants")
            
            # Push with all tags
            push_image_with_tags(bazel_target, list(tags.values()))
            print(f"Successfully pushed {app_name} {version} (after fallback)")
    
    print(f"\n{'='*80}")
    print(f"✅ Successfully released {image_name}:{version}")
    print(f"{'='*80}")
    print(f"\nPublished tags:")
    for tag in tags.values():
        print(f"  - {tag}")
    print(f"\nThe image index contains {len(platforms)} platform variants: {', '.join(platforms)}")
    print(f"Docker will automatically select the correct architecture when users pull.")