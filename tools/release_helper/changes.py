"""
Change detection utilities for the release helper using bazel-diff.
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Dict, List, Optional, Set

from tools.release_helper.core import run_bazel
from tools.release_helper.metadata import list_all_apps


def generate_hashes(output_file: str, commit: Optional[str] = None) -> None:
    """Generate bazel-diff hashes for workspace state.
    
    Args:
        output_file: Path to write hashes JSON
        commit: Optional git commit to checkout before generating hashes
    """
    current_ref = None
    
    try:
        if commit:
            # Store current ref
            result = subprocess.run(
                ["git", "rev-parse", "HEAD"],
                capture_output=True,
                text=True,
                check=True
            )
            current_ref = result.stdout.strip()
            
            # Checkout target commit
            print(f"Checking out {commit}...", file=sys.stderr)
            subprocess.run(
                ["git", "checkout", "-q", commit],
                check=True
            )
        
        print(f"Generating bazel-diff hashes to {output_file}...", file=sys.stderr)
        
        result = run_bazel([
            "run",
            "//tools:bazel_diff",
            "--",
            "-w", os.getcwd(),
            "-b", "/dev/null",
            "-o", output_file
        ])
        
        if not os.path.exists(output_file):
            raise RuntimeError(f"Hash generation failed: {output_file} not created")
        
        print(f"✓ Generated hashes: {output_file}", file=sys.stderr)
        
    finally:
        # Restore original ref if we checked out a different commit
        if current_ref and commit:
            print(f"Restoring {current_ref}...", file=sys.stderr)
            subprocess.run(
                ["git", "checkout", "-q", current_ref],
                check=True
            )


def get_changed_targets(
    starting_hashes: str,
    ending_hashes: str
) -> Set[str]:
    """Get changed targets between two hash files.
    
    Args:
        starting_hashes: Path to starting hashes JSON
        ending_hashes: Path to ending hashes JSON
    
    Returns:
        Set of changed target labels
    """
    if not os.path.exists(starting_hashes):
        raise FileNotFoundError(f"Starting hashes not found: {starting_hashes}")
    if not os.path.exists(ending_hashes):
        raise FileNotFoundError(f"Ending hashes not found: {ending_hashes}")
    
    print(f"Computing changed targets using bazel-diff...", file=sys.stderr)
    
    # Create temp file for output
    with tempfile.NamedTemporaryFile(mode='w', suffix=".txt", delete=False) as f:
        output_file = f.name
    
    try:
        result = run_bazel([
            "run",
            "//tools:bazel_diff",
            "--",
            "-sh", starting_hashes,
            "-fh", ending_hashes,
            "-o", output_file
        ])
        
        # Read changed targets
        with open(output_file, 'r') as f:
            changed_targets = {line.strip() for line in f if line.strip()}
        
        print(f"✓ Found {len(changed_targets)} changed targets", file=sys.stderr)
        
        return changed_targets
        
    finally:
        # Clean up temp file
        if os.path.exists(output_file):
            os.unlink(output_file)


def detect_changed_apps(base_commit: Optional[str] = None) -> List[Dict[str, str]]:
    """Detect which apps have changed using bazel-diff.
    
    Args:
        base_commit: Base commit to compare HEAD against. If None, returns all apps.
    
    Returns:
        List of app dictionaries with bazel_target, name, and domain
    """
    all_apps = list_all_apps()

    if not base_commit:
        print("No base commit specified, considering all apps as changed", file=sys.stderr)
        return all_apps

    # Create temp files for hashes
    with tempfile.NamedTemporaryFile(mode='w', suffix="-base.json", delete=False) as f:
        base_hashes = f.name
    with tempfile.NamedTemporaryFile(mode='w', suffix="-head.json", delete=False) as f:
        head_hashes = f.name
    
    try:
        # Generate hashes for base commit
        generate_hashes(base_hashes, commit=base_commit)
        
        # Generate hashes for current state
        generate_hashes(head_hashes)
        
        # Get changed targets
        changed_targets = get_changed_targets(base_hashes, head_hashes)
        
        if not changed_targets:
            print("No targets changed", file=sys.stderr)
            return []
        
        # Get all app_metadata targets
        result = run_bazel([
            "query",
            "kind('app_metadata', //...)",
            "--output=label"
        ])
        all_metadata_targets = set(result.stdout.strip().split('\n')) if result.stdout.strip() else set()
        
        if not all_metadata_targets:
            print("No app_metadata targets found", file=sys.stderr)
            return []
        
        # Find affected apps
        affected_apps = []
        
        for app in all_apps:
            metadata_target = app['bazel_target']
            
            # Check if metadata target itself changed
            if metadata_target in changed_targets:
                affected_apps.append(app)
                print(f"  {app['name']}: metadata target changed", file=sys.stderr)
                continue
            
            # Check if any dependency of the metadata changed
            # Use somepath to check if metadata depends on any changed target
            if changed_targets:
                try:
                    # Build a query expression (limit to 100 targets to avoid huge queries)
                    changed_list = list(changed_targets)[:100]
                    changed_expr = " + ".join(f'"{t}"' for t in changed_list)
                    result = run_bazel([
                        "query",
                        f"somepath({metadata_target}, {changed_expr})",
                        "--output=label"
                    ])
                    
                    if result.stdout.strip():
                        affected_apps.append(app)
                        print(f"  {app['name']}: depends on changed targets", file=sys.stderr)
                except subprocess.CalledProcessError:
                    # No path found, app not affected
                    pass
        
        if not affected_apps:
            print("No apps affected by changed targets", file=sys.stderr)
        
        return affected_apps
        
    finally:
        # Clean up temp files
        for f in [base_hashes, head_hashes]:
            if os.path.exists(f):
                os.unlink(f)
