"""
Release notes generation utilities for the release helper.
"""

import subprocess
import sys
from typing import Dict, List, Optional, Tuple
from dataclasses import dataclass
from datetime import datetime

from tools.release_helper.git import get_previous_tag
from tools.release_helper.changes import _get_changed_files
from tools.release_helper.metadata import get_app_metadata


@dataclass
class ReleaseNote:
    """Represents a single release note entry."""
    commit_sha: str
    commit_message: str
    author: str
    date: str
    files_changed: List[str]


def get_commits_between_refs(start_ref: str, end_ref: str = "HEAD") -> List[ReleaseNote]:
    """Get commit information between two Git references.
    
    Args:
        start_ref: Starting Git reference (tag, commit, etc.)
        end_ref: Ending Git reference (defaults to HEAD)
        
    Returns:
        List of ReleaseNote objects with commit information
    """
    try:
        # First check if the start_ref exists
        result_check = subprocess.run(
            ["git", "rev-parse", "--verify", start_ref],
            capture_output=True,
            text=True
        )
        
        if result_check.returncode != 0:
            print(f"Warning: Reference {start_ref} not found, using limited history", file=sys.stderr)
            # Fall back to just the current commit or last few commits
            result = subprocess.run(
                [
                    "git", "log", 
                    "-n", "5",  # Just get last 5 commits
                    "--pretty=format:%H|%s|%an|%ai",
                    "--no-merges"  # Skip merge commits for cleaner release notes
                ],
                capture_output=True,
                text=True,
                check=True
            )
        else:
            # Get commit information in a parseable format
            result = subprocess.run(
                [
                    "git", "log", 
                    f"{start_ref}..{end_ref}",
                    "--pretty=format:%H|%s|%an|%ai",
                    "--no-merges"  # Skip merge commits for cleaner release notes
                ],
                capture_output=True,
                text=True,
                check=True
            )
        
        if not result.stdout.strip():
            return []
            
        release_notes = []
        for line in result.stdout.strip().split('\n'):
            if '|' not in line:
                continue
                
            parts = line.split('|', 3)
            if len(parts) != 4:
                continue
                
            commit_sha, message, author, date = parts
            
            # Get files changed in this commit
            try:
                files_result = subprocess.run(
                    ["git", "diff-tree", "--no-commit-id", "--name-only", "-r", commit_sha],
                    capture_output=True,
                    text=True,
                    check=True
                )
                files_changed = [f for f in files_result.stdout.strip().split('\n') if f.strip()]
            except subprocess.CalledProcessError:
                files_changed = []  # Fallback to empty list if file diff fails
            
            release_notes.append(ReleaseNote(
                commit_sha=commit_sha[:8],  # Short SHA
                commit_message=message.strip(),
                author=author.strip(),
                date=date.strip(),
                files_changed=files_changed
            ))
            
        return release_notes
        
    except subprocess.CalledProcessError as e:
        print(f"Error getting commits between {start_ref} and {end_ref}: {e}", file=sys.stderr)
        return []


def filter_commits_by_app(commits: List[ReleaseNote], app_name: str) -> List[ReleaseNote]:
    """Filter commits to only those that affect the specified app.
    
    Args:
        commits: List of all commits
        app_name: Name of the app to filter for
        
    Returns:
        List of commits that affect the specified app
    """
    try:
        # Get app metadata to determine its path
        from tools.release_helper.metadata import list_all_apps
        all_apps = list_all_apps()
        
        app_info = None
        for app in all_apps:
            if app['name'] == app_name:
                app_info = app
                break
        
        if not app_info:
            print(f"Warning: App {app_name} not found in metadata", file=sys.stderr)
            return []
        
        # Extract package path from bazel target
        bazel_target = app_info['bazel_target']
        app_path = bazel_target[2:].split(':')[0]  # Remove // and :target
        
        # Filter commits that touch files in the app's directory
        app_commits = []
        for commit in commits:
            app_affected = False
            for file_path in commit.files_changed:
                if file_path.startswith(app_path + '/') or file_path == app_path:
                    app_affected = True
                    break
                # Also check for infrastructure changes that affect all apps
                if any(file_path.startswith(infra + '/') or file_path == infra 
                       for infra in ['tools', '.github', 'docker', 'MODULE.bazel', 'WORKSPACE', 'BUILD.bazel']):
                    app_affected = True
                    break
                    
            if app_affected:
                app_commits.append(commit)
                
        return app_commits
        
    except Exception as e:
        print(f"Error filtering commits for app {app_name}: {e}", file=sys.stderr)
        return commits  # Return all commits if filtering fails


def generate_release_notes(
    app_name: str,
    current_tag: str,
    previous_tag: Optional[str] = None,
    format_type: str = "markdown"
) -> str:
    """Generate release notes for an app between two tags.
    
    Args:
        app_name: Name of the app
        current_tag: Current tag/version
        previous_tag: Previous tag to compare against (auto-detected if None)
        format_type: Format for output ("markdown", "plain", "json")
        
    Returns:
        Formatted release notes string
    """
    # Auto-detect previous tag if not provided
    if previous_tag is None:
        previous_tag = get_previous_tag()
        if not previous_tag:
            previous_tag = "HEAD~10"  # Fallback to last 10 commits
            print(f"Warning: No previous tag found, comparing against {previous_tag}", file=sys.stderr)
    
    print(f"Generating release notes for {app_name} from {previous_tag} to {current_tag}", file=sys.stderr)
    
    # Get all commits between tags
    all_commits = get_commits_between_refs(previous_tag, current_tag)
    
    if not all_commits:
        return _format_empty_release_notes(app_name, current_tag, previous_tag, format_type)
    
    # Filter commits that affect this app
    app_commits = filter_commits_by_app(all_commits, app_name)
    
    # Generate formatted release notes
    return _format_release_notes(app_name, current_tag, previous_tag, app_commits, format_type)


def _format_empty_release_notes(
    app_name: str, 
    current_tag: str, 
    previous_tag: str, 
    format_type: str
) -> str:
    """Format release notes when no changes are found."""
    if format_type == "markdown":
        return f"""# Release Notes: {app_name} {current_tag}

**Released:** {datetime.now().strftime('%Y-%m-%d %H:%M:%S UTC')}
**Previous Version:** {previous_tag}

## Changes

No changes affecting {app_name} found between {previous_tag} and {current_tag}.

---
*Generated automatically by the release helper*
"""
    elif format_type == "plain":
        return f"""Release Notes: {app_name} {current_tag}
Released: {datetime.now().strftime('%Y-%m-%d %H:%M:%S UTC')}
Previous Version: {previous_tag}

Changes:
No changes affecting {app_name} found between {previous_tag} and {current_tag}.
"""
    elif format_type == "json":
        import json
        return json.dumps({
            "app": app_name,
            "version": current_tag,
            "previous_version": previous_tag,
            "released_at": datetime.now().isoformat(),
            "changes": [],
            "summary": f"No changes affecting {app_name} found"
        }, indent=2)
    else:
        raise ValueError(f"Unsupported format type: {format_type}")


def _format_release_notes(
    app_name: str,
    current_tag: str, 
    previous_tag: str,
    commits: List[ReleaseNote],
    format_type: str
) -> str:
    """Format release notes with commit information."""
    if format_type == "markdown":
        lines = [
            f"# Release Notes: {app_name} {current_tag}",
            "",
            f"**Released:** {datetime.now().strftime('%Y-%m-%d %H:%M:%S UTC')}",
            f"**Previous Version:** {previous_tag}",
            f"**Commits:** {len(commits)}",
            "",
            "## Changes",
            ""
        ]
        
        for commit in commits:
            lines.append(f"### [{commit.commit_sha}] {commit.commit_message}")
            lines.append(f"**Author:** {commit.author}")
            lines.append(f"**Date:** {commit.date}")
            if commit.files_changed:
                lines.append(f"**Files:** {', '.join(commit.files_changed[:5])}")
                if len(commit.files_changed) > 5:
                    lines.append(f"*... and {len(commit.files_changed) - 5} more files*")
            lines.append("")
        
        lines.extend([
            "---",
            "*Generated automatically by the release helper*"
        ])
        
        return "\n".join(lines)
        
    elif format_type == "plain":
        lines = [
            f"Release Notes: {app_name} {current_tag}",
            f"Released: {datetime.now().strftime('%Y-%m-%d %H:%M:%S UTC')}",
            f"Previous Version: {previous_tag}",
            f"Commits: {len(commits)}",
            "",
            "Changes:"
        ]
        
        for i, commit in enumerate(commits, 1):
            lines.append(f"{i}. [{commit.commit_sha}] {commit.commit_message}")
            lines.append(f"   Author: {commit.author}")
            lines.append(f"   Date: {commit.date}")
            lines.append("")
            
        return "\n".join(lines)
        
    elif format_type == "json":
        import json
        commit_data = []
        for commit in commits:
            commit_data.append({
                "sha": commit.commit_sha,
                "message": commit.commit_message,
                "author": commit.author,
                "date": commit.date,
                "files_changed": commit.files_changed
            })
            
        return json.dumps({
            "app": app_name,
            "version": current_tag,
            "previous_version": previous_tag,
            "released_at": datetime.now().isoformat(),
            "commit_count": len(commits),
            "changes": commit_data,
            "summary": f"{len(commits)} commits affecting {app_name}"
        }, indent=2)
    else:
        raise ValueError(f"Unsupported format type: {format_type}")


def generate_release_notes_for_all_apps(
    current_tag: str,
    previous_tag: Optional[str] = None,
    format_type: str = "markdown"
) -> Dict[str, str]:
    """Generate release notes for all apps between two tags.
    
    Args:
        current_tag: Current tag/version  
        previous_tag: Previous tag to compare against (auto-detected if None)
        format_type: Format for output ("markdown", "plain", "json")
        
    Returns:
        Dictionary mapping app names to their release notes
    """
    from tools.release_helper.metadata import list_all_apps
    
    all_apps = list_all_apps()
    release_notes = {}
    
    for app in all_apps:
        app_name = app['name']
        notes = generate_release_notes(app_name, current_tag, previous_tag, format_type)
        release_notes[app_name] = notes
        
    return release_notes