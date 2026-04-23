# Backstage Integration Plan for Everything Monorepo

## Context

Currently, the Everything monorepo uses a sophisticated release system built around:
- A Python-based release tool (`tools/release_helper/`) that handles version management, release planning, and GitHub release creation
- Git tags for versioning (`{domain}-{app}.vX.Y.Z`)
- GitHub Actions workflow that orchestrates the entire release process
- 24-25 apps across 4 domains with no centralized service catalog

**The Problem**:
- No centralized service catalog or ownership tracking
- Release tool has grown complex with version management, planning, and release notes generation
- No visibility into what's deployed where
- Documentation scattered across README files

**The Goal**:
Integrate Backstage as a self-hosted developer portal that:
1. Provides a centralized service catalog for all 24-25 apps
2. Takes over version management and release notes generation from the release tool
3. Simplifies the release tool to focus only on building artifacts
4. Maintains the current git tag pattern for backward compatibility
5. Uses GitHub Actions for builds and GHCR for artifact storage

## Approach: Hybrid Release Model

**Backstage Responsibilities**:
- Service catalog and ownership
- Version management (replaces auto-increment logic in release tool)
- Release notes generation
- Release orchestration and approval workflows
- Documentation hub (TechDocs)

**GitHub Actions Responsibilities**:
- Building container images (multiarch)
- Building Helm charts
- Pushing to GHCR and ChartMuseum
- Creating git tags (triggered by Backstage)

**What Gets Deprecated**:
- `tools/release_helper/release_notes.py` - Release notes generation
- Auto-increment version logic in release helper
- Release planning/discovery logic (simplified to use Backstage catalog)

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                 Backstage Developer Portal               │
│  ┌───────────────┐  ┌──────────────┐  ┌──────────────┐ │
│  │Service Catalog│  │Release Plugin│  │  TechDocs    │ │
│  │  24-25 apps   │  │Version Mgmt  │  │Documentation │ │
│  └───────────────┘  └──────────────┘  └──────────────┘ │
└─────────────────────────────────────────────────────────┘
              │                    │
              │ 1. Trigger         │ 2. Create Tag
              │    Release         │    & Release
              ↓                    ↓
┌─────────────────────────────────────────────────────────┐
│              GitHub Actions (release.yml)                │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐ │
│  │ Build Images │→ │Package Charts│→ │Push Artifacts│ │
│  │  (Bazel)     │  │   (Helm)     │  │(GHCR/ChartM) │ │
│  └──────────────┘  └──────────────┘  └──────────────┘ │
└─────────────────────────────────────────────────────────┘
```

## Implementation Plan

### Phase 1: Backstage Foundation (Week 1-2)

**1.1 Setup Backstage Instance**

Create new directory structure:
```
everything/
├── backstage/
│   ├── app/                     # Backstage frontend
│   ├── packages/
│   │   ├── backend/            # Backend customizations
│   │   └── app/                # Frontend customizations
│   ├── plugins/
│   │   └── release/            # Custom release plugin
│   ├── catalog-info.yaml       # Backstage as a component
│   ├── app-config.yaml         # Dev config
│   ├── app-config.production.yaml
│   ├── package.json
│   ├── BUILD.bazel             # Bazel build
│   └── Tiltfile                # Local dev
└── tools/
    └── backstage/
        ├── catalog_generator.py # Generate catalog from BUILD.bazel
        └── BUILD.bazel
```

**1.2 Initialize Backstage**

```bash
cd everything/
mkdir backstage
cd backstage
npx @backstage/create-app
```

**1.3 Configure PostgreSQL**

Reuse existing postgres-dev or create new database:
```yaml
# backstage/app-config.yaml
backend:
  database:
    client: pg
    connection:
      host: localhost
      port: 5432
      user: backstage
      password: ${POSTGRES_PASSWORD}
      database: backstage_catalog
```

**1.4 Install Core Plugins**

```bash
cd backstage
yarn add --cwd packages/backend @backstage/plugin-catalog-backend-module-github
yarn add --cwd packages/app @backstage/plugin-github-actions
yarn add --cwd packages/app @backstage/plugin-techdocs
yarn add --cwd packages/app @backstage/plugin-api-docs
```

**1.5 Create Tiltfile for Local Development**

File: `backstage/Tiltfile`
```python
local_resource(
    'backstage-backend',
    serve_cmd='yarn workspace backend start',
    serve_env={
        'POSTGRES_HOST': 'localhost',
        'POSTGRES_PORT': '5432',
        'GITHUB_TOKEN': os.getenv('GITHUB_TOKEN'),
    },
    deps=['packages/backend'],
)

local_resource(
    'backstage-frontend',
    serve_cmd='yarn workspace app start',
    deps=['packages/app'],
    links=['http://localhost:3000'],
)
```

**Critical Files**:
- `backstage/app-config.yaml` - Main configuration
- `backstage/Tiltfile` - Local development setup

### Phase 2: Service Catalog Generation (Week 3-4)

**2.1 Extend release_app Macro**

Add ownership and lifecycle fields to existing macro.

File: `tools/bazel/release.bzl`

```python
def release_app(
    name,
    language,
    domain,
    app_type,
    description = "",
    port = None,
    # ... existing parameters ...
    owner = "team-platform",        # NEW
    lifecycle = "production",       # NEW: production/experimental/deprecated
    depends_on = [],                # NEW: list of dependencies
):
    # Existing metadata creation
    metadata = {
        "name": name,
        "language": language,
        "domain": domain,
        "app_type": app_type,
        "description": description,
        # ... existing fields ...
        "owner": owner,                 # NEW
        "lifecycle": lifecycle,         # NEW
        "depends_on": depends_on,       # NEW
    }
    # ... rest of macro
```

**2.2 Create Catalog Generator Tool**

File: `tools/backstage/catalog_generator.py`

```python
#!/usr/bin/env python3
"""Generate Backstage catalog-info.yaml from Bazel app metadata."""

import json
import subprocess
from pathlib import Path
from typing import Dict, List
import yaml

def query_app_metadata() -> List[Dict]:
    """Query all app_metadata targets and build metadata."""
    # Query for all metadata targets
    result = subprocess.run(
        ["bazel", "query", "kind('app_metadata', //...)", "--output=label"],
        capture_output=True,
        text=True,
        check=True,
    )

    targets = result.stdout.strip().split('\n')
    metadata_list = []

    for target in targets:
        # Build the metadata target
        subprocess.run(["bazel", "build", target], check=True)

        # Read generated JSON
        # Convert //domain:app_metadata to bazel-bin/domain/app_metadata.json
        json_path = target.replace('//', 'bazel-bin/').replace(':', '/')
        if not json_path.endswith('_metadata'):
            continue

        metadata_file = Path(f"{json_path}.json")
        if metadata_file.exists():
            with open(metadata_file) as f:
                metadata = json.load(f)
                metadata_list.append(metadata)

    return metadata_list

def generate_component_entity(metadata: Dict) -> Dict:
    """Generate Backstage Component entity."""
    app_name = metadata["name"]
    domain = metadata["domain"]

    return {
        "apiVersion": "backstage.io/v1alpha1",
        "kind": "Component",
        "metadata": {
            "name": app_name,
            "title": app_name.replace("-", " ").title(),
            "description": metadata.get("description", ""),
            "annotations": {
                "github.com/project-slug": "whale-net/everything",
                "backstage.io/techdocs-ref": "dir:.",
                "ghcr.io/image-name": metadata["repo_name"],
            },
            "tags": [
                metadata["language"],
                metadata["app_type"],
            ],
            "links": [
                {
                    "url": f"https://ghcr.io/whale-net/{metadata['repo_name']}",
                    "title": "Container Registry",
                    "icon": "docker",
                },
                {
                    "url": f"https://github.com/whale-net/everything/releases?q={domain}-{app_name}",
                    "title": "Releases",
                    "icon": "github",
                },
            ],
        },
        "spec": {
            "type": "service" if "api" in metadata["app_type"] else "worker",
            "lifecycle": metadata.get("lifecycle", "production"),
            "owner": metadata.get("owner", "team-platform"),
            "system": domain,
        }
    }

def generate_system_entity(domain: str, apps: List[Dict]) -> Dict:
    """Generate Backstage System entity for a domain."""
    return {
        "apiVersion": "backstage.io/v1alpha1",
        "kind": "System",
        "metadata": {
            "name": domain,
            "title": domain.replace("-", " ").title(),
            "description": f"{domain.title()} domain services",
        },
        "spec": {
            "owner": "team-platform",
        }
    }

def main():
    """Generate all catalog files."""
    apps = query_app_metadata()

    # Group by domain
    domains = {}
    for app in apps:
        domain = app["domain"]
        if domain not in domains:
            domains[domain] = []
        domains[domain].append(app)

    # Generate system catalogs
    for domain, domain_apps in domains.items():
        domain_path = Path(domain)
        catalog_path = domain_path / "catalog-info.yaml"

        # System entity
        system_entity = generate_system_entity(domain, domain_apps)

        with open(catalog_path, "w") as f:
            yaml.dump(system_entity, f, default_flow_style=False)
        print(f"Generated {catalog_path}")

    # Generate component catalogs
    for app in apps:
        # Determine app directory (simplified)
        app_dir = Path(app["domain"]) / app["name"].replace(f"{app['domain']}-", "")
        catalog_path = app_dir / "catalog-info.yaml"

        component = generate_component_entity(app)

        with open(catalog_path, "w") as f:
            yaml.dump(component, f, default_flow_style=False)
        print(f"Generated {catalog_path}")

if __name__ == "__main__":
    main()
```

File: `tools/backstage/BUILD.bazel`

```python
load("@rules_python//python:defs.bzl", "py_binary")

py_binary(
    name = "catalog_generator",
    srcs = ["catalog_generator.py"],
    deps = ["@pypi//pyyaml"],
    visibility = ["//visibility:public"],
)
```

**2.3 Generate Initial Catalogs**

```bash
bazel run //tools/backstage:catalog_generator
```

**2.4 Configure Backstage Catalog Discovery**

File: `backstage/app-config.yaml`

```yaml
catalog:
  locations:
    - type: file
      target: ../../demo/catalog-info.yaml
    - type: file
      target: ../../manman/catalog-info.yaml
    - type: file
      target: ../../friendly_computing_machine/catalog-info.yaml
    - type: file
      target: ../../demo/*/catalog-info.yaml
    - type: file
      target: ../../manman/*/catalog-info.yaml
```

**Critical Files**:
- `tools/bazel/release.bzl` - Extended with owner/lifecycle fields
- `tools/backstage/catalog_generator.py` - Catalog generation tool
- Generated `catalog-info.yaml` files across codebase

### Phase 3: Custom Release Plugin (Week 5-7)

**3.1 Create Release Plugin Structure**

```bash
cd backstage
yarn new --select plugin
# Name: release
# Owner: @whale-net/platform
```

**3.2 Implement Version Management**

File: `backstage/plugins/release/src/api/VersionManager.ts`

```typescript
export class VersionManager {
  private githubApi: Octokit;

  async getLatestVersion(domain: string, app: string): Promise<string> {
    // Fetch git tags from GitHub
    const tags = await this.githubApi.repos.listTags({
      owner: 'whale-net',
      repo: 'everything',
    });

    // Filter for this app
    const appTags = tags.data
      .filter(tag => tag.name.startsWith(`${domain}-${app}.v`))
      .map(tag => tag.name.replace(`${domain}-${app}.v`, ''))
      .sort((a, b) => semver.compare(b, a)); // Latest first

    return appTags[0] || '0.0.0';
  }

  async calculateNextVersion(
    domain: string,
    app: string,
    increment: 'major' | 'minor' | 'patch'
  ): Promise<string> {
    const current = await this.getLatestVersion(domain, app);
    return semver.inc(current, increment);
  }
}
```

**3.3 Implement Release Orchestration**

File: `backstage/plugins/release-backend/src/service/ReleaseService.ts`

```typescript
export class ReleaseService {
  async triggerRelease(params: {
    apps: string[];
    helmCharts?: string[];
    versionIncrement: 'major' | 'minor' | 'patch';
    dryRun: boolean;
  }): Promise<void> {
    // Calculate versions for each app
    const appVersions = await Promise.all(
      params.apps.map(async app => {
        const [domain, appName] = app.split('-', 2);
        const version = await this.versionManager.calculateNextVersion(
          domain,
          appName,
          params.versionIncrement
        );
        return { domain, appName, version };
      })
    );

    // Trigger GitHub Actions workflow
    await this.githubApi.actions.createWorkflowDispatch({
      owner: 'whale-net',
      repo: 'everything',
      workflow_id: 'release.yml',
      ref: 'main',
      inputs: {
        apps: params.apps.join(','),
        helm_charts: params.helmCharts?.join(',') || '',
        versions: JSON.stringify(appVersions),
        dry_run: params.dryRun.toString(),
      },
    });
  }

  async generateReleaseNotes(
    domain: string,
    app: string,
    fromVersion: string,
    toVersion: string
  ): Promise<string> {
    // Get commits between versions
    const fromTag = `${domain}-${app}.v${fromVersion}`;
    const toTag = `${domain}-${app}.v${toVersion}`;

    const comparison = await this.githubApi.repos.compareCommits({
      owner: 'whale-net',
      repo: 'everything',
      base: fromTag,
      head: 'main',
    });

    // Generate notes from commits
    const notes = comparison.data.commits
      .map(commit => `- ${commit.commit.message.split('\n')[0]}`)
      .join('\n');

    return `## Changes\n\n${notes}`;
  }
}
```

**3.4 Create Release UI Component**

File: `backstage/plugins/release/src/components/ReleaseDialog.tsx`

```typescript
export const ReleaseDialog = ({ entity }: { entity: Entity }) => {
  const [versionIncrement, setVersionIncrement] = useState<'patch' | 'minor' | 'major'>('patch');
  const [dryRun, setDryRun] = useState(false);
  const releaseApi = useApi(releaseApiRef);

  const handleRelease = async () => {
    await releaseApi.triggerRelease({
      apps: [entity.metadata.name],
      versionIncrement,
      dryRun,
    });
  };

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogTitle>Release {entity.metadata.name}</DialogTitle>
      <DialogContent>
        <FormControl>
          <FormLabel>Version Increment</FormLabel>
          <RadioGroup value={versionIncrement} onChange={(e) => setVersionIncrement(e.target.value)}>
            <FormControlLabel value="patch" control={<Radio />} label="Patch (bug fixes)" />
            <FormControlLabel value="minor" control={<Radio />} label="Minor (features)" />
            <FormControlLabel value="major" control={<Radio />} label="Major (breaking)" />
          </RadioGroup>
        </FormControl>

        <FormControlLabel
          control={<Checkbox checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} />}
          label="Dry run (build but don't publish)"
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button onClick={handleRelease} variant="contained" color="primary">
          Release
        </Button>
      </DialogActions>
    </Dialog>
  );
};
```

**Critical Files**:
- `backstage/plugins/release/` - Custom release plugin
- `backstage/plugins/release-backend/` - Backend API

### Phase 4: Simplify Release Workflow (Week 8-9)

**4.1 Update GitHub Actions Workflow**

Simplify `.github/workflows/release.yml` to focus only on building:

```yaml
name: Release

on:
  workflow_dispatch:
    inputs:
      apps:
        description: 'Comma-separated list of apps'
        required: true
      helm_charts:
        description: 'Comma-separated list of charts'
        required: false
      versions:
        description: 'JSON map of app versions from Backstage'
        required: true
      dry_run:
        description: 'Dry run mode'
        type: boolean
        default: false

jobs:
  # Simplified - no planning phase needed (Backstage provides versions)
  release:
    name: Release ${{ matrix.domain }}-${{ matrix.app }}
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include: ${{ fromJson(github.event.inputs.versions) }}
    steps:
      - uses: actions/checkout@v4

      - name: Setup Build Environment
        uses: ./.github/actions/setup-build-env

      - name: Build and push image
        env:
          APP: ${{ matrix.app }}
          DOMAIN: ${{ matrix.domain }}
          VERSION: ${{ matrix.version }}
        run: |
          # Simplified - just build and push
          bazel run --config=ci-images //tools:release -- release-multiarch \
            "${DOMAIN}-${APP}" \
            --version "$VERSION" \
            --commit "${{ github.sha }}"

      - name: Create git tag
        if: ${{ github.event.inputs.dry_run == 'false' }}
        run: |
          git tag "${DOMAIN}-${APP}.v${VERSION}"
          git push origin "${DOMAIN}-${APP}.v${VERSION}"
```

**4.2 Deprecate Release Helper Code**

Remove or simplify these files:
- `tools/release_helper/release_notes.py` - Moved to Backstage
- `tools/release_helper/version.py` - Simplified (Backstage provides versions)
- `tools/release_helper/plan.py` - No longer needed (Backstage catalog)

Keep minimal code for:
- Building images (`tools/release_helper/build.py`)
- Pushing to registries (`tools/release_helper/publish.py`)

**Critical Files**:
- `.github/workflows/release.yml` - Simplified workflow
- `tools/release_helper/` - Deprecated/simplified files

### Phase 5: Documentation & TechDocs (Week 10)

**5.1 Add mkdocs.yml to Each Domain**

File: `demo/mkdocs.yml`

```yaml
site_name: Demo Applications
site_description: Example applications

nav:
  - Home: README.md
  - Architecture: ARCHITECTURE.md

plugins:
  - techdocs-core
```

**5.2 Update Existing Documentation**

Existing `README.md` files work as-is! Just add mkdocs.yml.

**5.3 Configure TechDocs**

File: `backstage/app-config.yaml`

```yaml
techdocs:
  builder: 'local'
  generator:
    runIn: 'local'
  publisher:
    type: 'local'
```

**Critical Files**:
- `{domain}/mkdocs.yml` - Documentation configuration
- Existing README.md files (no changes needed)

### Phase 6: Production Deployment (Week 11-12)

**6.1 Create Helm Chart for Backstage**

File: `backstage/chart/Chart.yaml`

```yaml
apiVersion: v2
name: backstage
description: Backstage Developer Portal
type: application
version: 0.1.0
```

**6.2 Add to Release System**

Add Backstage as a releasable app in `backstage/BUILD.bazel`:

```python
release_app(
    name = "backstage",
    language = "javascript",
    domain = "platform",
    app_type = "external-api",
    description = "Developer portal and service catalog",
    port = 7007,
    owner = "team-platform",
    lifecycle = "production",
)
```

**6.3 Deploy to Production**

```bash
# Release Backstage itself
helm install backstage ./backstage/chart --namespace platform
```

**Critical Files**:
- `backstage/chart/` - Helm chart for deployment
- `backstage/BUILD.bazel` - Make Backstage releasable

## Migration Strategy

### Before Migration
- Release via `/release` skill or manual GitHub Actions
- Version managed by release tool auto-increment
- No service catalog

### After Migration (Phased)

**Week 1-4**: Parallel operation
- Both systems work (old release skill + new Backstage)
- Team can test Backstage releases
- Old system still available as backup

**Week 5-8**: Gradual adoption
- Encourage Backstage for new releases
- Update documentation to point to Backstage
- Monitor adoption

**Week 9-12**: Full migration
- Deprecate `/release` skill
- Remove old release tool code
- Backstage is primary release mechanism

## Verification

### Service Catalog
1. Navigate to http://localhost:3000 (dev) or https://backstage.whalenet.dev (prod)
2. See all 24-25 apps in catalog
3. Each app shows owner, system, links
4. Search works for finding services

### Release Workflow
1. Open a component in Backstage
2. Click "Release" button
3. Select version increment (patch/minor/major)
4. Backstage calculates next version
5. GitHub Actions workflow triggered
6. Artifacts built and pushed
7. Git tag created
8. Release appears in Backstage history

### Documentation
1. TechDocs renders README.md files
2. Search finds documentation
3. Links between docs work

## Critical Files Summary

**New Files**:
- `backstage/` - Entire Backstage application
- `tools/backstage/catalog_generator.py` - Catalog generation
- Generated `catalog-info.yaml` files (24-25 apps + 4 domains)

**Modified Files**:
- `tools/bazel/release.bzl` - Add owner/lifecycle fields
- `.github/workflows/release.yml` - Simplified workflow
- `**/BUILD.bazel` - Add owner field to all apps

**Deprecated/Simplified**:
- `tools/release_helper/release_notes.py` - Logic moved to Backstage
- `tools/release_helper/version.py` - Version management in Backstage
- `tools/release_helper/plan.py` - Catalog discovery in Backstage
- `.claude/skills/release/` - Release skill (eventually deprecated)

## Success Metrics

- [ ] All 24-25 apps discoverable in Backstage catalog
- [ ] Releases triggered from Backstage UI
- [ ] Version management handled by Backstage (no manual version specification)
- [ ] Release notes auto-generated by Backstage
- [ ] GitHub Actions workflow simplified (no planning phase)
- [ ] Release tool code reduced by 60%+
- [ ] Documentation accessible via TechDocs
- [ ] Team adoption: 80% of releases via Backstage within 3 months
