"""
Git operations for the release helper.
"""

import subprocess
from typing import Optional


def format_git_tag(domain: str, app_name: str, version: str) -> str:
    """Format a Git tag in the domain-app-name-version format."""
    return f"{domain}-{app_name}-{version}"


def create_git_tag(tag_name: str, commit_sha: Optional[str] = None, message: Optional[str] = None) -> None:
    """Create a Git tag on the specified commit."""
    cmd = ["git", "tag"]

    if message:
        cmd.extend(["-a", tag_name, "-m", message])
    else:
        cmd.append(tag_name)

    if commit_sha:
        cmd.append(commit_sha)

    print(f"Creating Git tag: {tag_name}")
    subprocess.run(cmd, check=True)


def push_git_tag(tag_name: str) -> None:
    """Push a Git tag to the remote repository."""
    print(f"Pushing Git tag: {tag_name}")
    subprocess.run(["git", "push", "origin", tag_name], check=True)


def get_previous_tag() -> Optional[str]:
    """Get the previous Git tag."""
    try:
        result = subprocess.run(
            ["git", "describe", "--tags", "--abbrev=0", "HEAD^"],
            capture_output=True,
            text=True,
            check=True
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        return None