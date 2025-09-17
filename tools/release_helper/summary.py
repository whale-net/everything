"""
Summary generation utilities for the release helper.
"""

import json
from tools.release_helper.metadata import list_all_apps


def generate_release_summary(
    matrix_json: str,
    version: str,
    event_type: str,
    dry_run: bool = False,
    repository_owner: str = ""
) -> str:
    """Generate a release summary for GitHub Actions."""

    try:
        matrix = json.loads(matrix_json) if matrix_json else {"include": []}
    except json.JSONDecodeError:
        matrix = {"include": []}

    summary = []
    summary.append("## 🚀 Release Summary")
    summary.append("")
    
    if not matrix.get("include"):
        summary.append("🔍 **Result:** No apps detected for release")
    else:
        summary.append("✅ **Result:** Release completed")
        summary.append("")
        
        apps = [item["app"] for item in matrix["include"]]
        summary.append(f"📦 **Apps:** {', '.join(apps)}")
        summary.append(f"🏷️  **Version:** {version}")
        summary.append("🛠️ **System:** Consolidated Release + OCI")
        
        if event_type == "workflow_dispatch":
            summary.append("📝 **Trigger:** Manual dispatch")
            if dry_run:
                summary.append("🧪 **Mode:** Dry run (no images published)")
        else:
            summary.append("📝 **Trigger:** Git tag push")
        
        summary.append("")
        summary.append("### 🐳 Container Images")
        if dry_run:
            summary.append("**Dry run mode - no images were published**")
        else:
            summary.append("Published to GitHub Container Registry:")
            # Get app metadata to determine correct image names
            all_apps = list_all_apps()
            app_domains = {app['name']: app['domain'] for app in all_apps}
            
            for app in apps:
                domain = app_domains.get(app, 'unknown')
                image_name = f"{domain}-{app}"
                summary.append(f"- `ghcr.io/{repository_owner.lower()}/{image_name}:{version}`")
        
        summary.append("")
        summary.append("### 🛠️ Local Development")
        summary.append("```bash")
        summary.append("# List all apps")
        summary.append("bazel run //tools:release -- list")
        summary.append("")
        summary.append("# Build and test an app locally")
        for app in apps[:2]:  # Show first 2 apps as examples
            summary.append(f"bazel run //tools:release -- build {app}")
        summary.append("```")
    
    return "\n".join(summary)