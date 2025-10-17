# CI/CD Pipeline

This guide describes the continuous integration and deployment pipeline.

## Overview

The repository uses GitHub Actions for continuous integration with parallel build and test jobs. Bazel provides excellent caching by default, caching build outputs, test results, and dependencies between builds, which significantly speeds up CI runs.

## Pipeline Flow

```mermaid
graph TD
    A[Push/PR] --> B[Build Job]
    A --> C[Test Job]
    C --> D{Test Success?}
    D -->|Yes| E[Container Arch Test]
    D -->|No| F[Pipeline Fails]
    B --> G{Build Success?}
    G -->|Yes| H[Plan Docker]
    G -->|No| F
    E --> I{Arch Test Success?}
    I -->|Yes| H
    I -->|No| F
    H --> J{Main Branch?}
    J -->|Yes| K[Docker Job]
    J -->|No| L[Build Summary]
    K --> M[Build & Push Images]
    M --> L
    L --> N[Report Status]
    C --> O[Upload Test Results]
    
    style B fill:#e1f5fe
    style C fill:#e1f5fe
    style E fill:#f3e5f5
    style K fill:#fff3e0
    style F fill:#ffebee
    style O fill:#e8f5e8
    style M fill:#e8f5e8
    style L fill:#e3f2fd
    style N fill:#f1f8e9
```

## Bazel Caching Benefits

- **Build Cache**: Reuses compiled artifacts across builds when source files haven't changed
- **Test Cache**: Skips re-running tests when code and dependencies are unchanged  
- **Remote Cache**: Shares cache between CI runs and developers (configured in `.bazelrc`)
- **Dependency Cache**: Caches external dependencies like Python packages and Go modules

## CI Jobs

### Build
Builds all targets to verify compilation (runs in parallel with Test)

### Test
Runs all unit and integration tests

### Container Arch Test
Verifies cross-compilation for multi-architecture containers (critical for ARM64 support)

### Plan Docker
Determines which apps need Docker images built based on changes

### Docker
Builds container images and pushes to registry (only runs on main branch commits)

### Build Summary
Collects and reports the status of all CI jobs
