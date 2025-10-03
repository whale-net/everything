# Release Helper - Quick Reference

## Import Paths

### Recommended (New Structure)
```python
# Core utilities
from tools.release_helper.core import run_bazel, find_workspace_root
from tools.release_helper.core.git_ops import get_latest_app_version
from tools.release_helper.core.validate import validate_release_version

# Container operations
from tools.release_helper.containers import build_image, plan_release
from tools.release_helper.containers.image_ops import push_image_with_tags
from tools.release_helper.containers.release_ops import tag_and_push_image

# GitHub operations
from tools.release_helper.github import create_app_release, generate_release_notes
from tools.release_helper.github.releases import create_releases_for_apps
from tools.release_helper.github.notes import generate_release_notes_for_all_apps

# Helm chart operations
from tools.release_helper.charts.composer import HelmComposer
from tools.release_helper.charts.types import AppType, resolve_app_type
from tools.release_helper.charts.operations import list_all_helm_charts

# Other utilities
from tools.release_helper.metadata import list_all_apps, get_app_metadata
from tools.release_helper.changes import detect_changed_apps
from tools.release_helper.summary import generate_release_summary
```

### Legacy (Backward Compatible)
```python
# Still works via lazy loading - will be deprecated
from tools.release_helper.git import get_previous_tag
from tools.release_helper.images import build_image
from tools.release_helper.validation import validate_release_version
from tools.release_helper.release import plan_release
from tools.release_helper.github_release import create_app_release
from tools.release_helper.release_notes import generate_release_notes
from tools.release_helper.helm import list_all_helm_charts
```

## Common Tasks

### Generate Helm Chart
```bash
# Via CLI
python3 tools/release_helper/charts/composer.py \
  --metadata demo/hello_python/hello_python_metadata.json \
  --chart-name my-chart \
  --version 1.0.0 \
  --environment production \
  --namespace prod \
  --output /tmp/charts \
  --template-dir tools/helm/templates

# Via Bazel (when network available)
bazel build //demo:fastapi_chart
```

### Run Tests
```bash
# Verification script
./tools/release_helper/verify_implementation.sh

# Specific test
bazel test //tools/release_helper:test_composer
```

### CLI Commands
```bash
# Release helper commands
bazel run //tools:release -- list
bazel run //tools:release -- changes
bazel run //tools:release -- build app_name

# Helm chart commands
bazel run //tools:release -- list-helm-charts
bazel run //tools:release -- helm-chart-info chart-name
bazel run //tools:release -- build-helm-chart chart-name
```

## Module Structure

```
tools/release_helper/
├── core/              # Bazel, Git, Validation
├── containers/        # Images, Releases
├── github/            # GitHub Releases, Notes
├── charts/            # Helm Composer (Python)
├── metadata.py        # App metadata
├── changes.py         # Change detection
├── summary.py         # Release summary
└── cli.py            # CLI interface
```

## Testing

```python
# Test imports
from tools.release_helper.charts.composer import HelmComposer
from tools.release_helper.charts.types import AppType

# Create composer
composer = HelmComposer(
    chart_name="test-chart",
    version="1.0.0",
    environment="prod",
    namespace="production",
    output_dir="/tmp/output",
    template_dir="tools/helm/templates"
)

# Load metadata and generate
composer.load_metadata(["metadata.json"])
composer.generate_chart()
```

## Documentation

- `tools/release_helper/charts/README.md` - Charts module documentation
- `tools/release_helper/REORGANIZATION.md` - Reorganization details
- `IMPLEMENTATION_SUMMARY.md` - Complete implementation summary
- `tools/helm/README.md` - Helm chart system overview
