# Release Workflow Decision Tree

## Input Validation Flow

```
START
  │
  ├─ Check: At least one of {apps, helm_charts} specified?
  │  ├─ NO  → ERROR: Must specify at least one target
  │  └─ YES → Continue
  │
  └─ Check: Exactly one of {version, increment_minor, increment_patch}?
     ├─ NO  → ERROR: Must specify exactly one version option
     └─ YES → Proceed to release
```

## Job Execution Flow

```
┌─────────────────┐
│ validate-inputs │
└────────┬────────┘
         │
         ├────────────────────────────────┬───────────────────────────┐
         │                                │                           │
         │ (if apps != '')                │ (always)                  │ (if helm_charts != '')
         ▼                                │                           ▼
┌─────────────────┐                       │                  ┌─────────────────────┐
│  plan-release   │                       │                  │ release-helm-charts │
└────────┬────────┘                       │                  │  (needs validation  │
         │                                │                  │   & plan-release)   │
         │ (if plan success)              │                  └──────────┬──────────┘
         ▼                                │                             │
┌─────────────────┐                       │                             │
│    release      │                       │                             │
└────────┬────────┘                       │                             │
         │                                │                             │
         │ (if release success)           │                             │
         ▼                                │                             │
┌─────────────────────────┐               │                             │
│ create-github-releases  │               │                             │
└────────┬────────────────┘               │                             │
         │                                │                             │
         └────────────────────────────────┴─────────────────────────────┘
                                          │
                                          ▼
                                ┌──────────────────┐
                                │ release-summary  │
                                │  (always runs)   │
                                └──────────────────┘
```

## Execution Scenarios

### Scenario 1: Apps Only
```
Input: apps="hello_python", helm_charts=""

validate-inputs ✓
     │
     ├─→ plan-release ✓
     │        │
     │        └─→ release ✓
     │                │
     │                └─→ create-github-releases ✓
     │
     └─→ release-helm-charts ⊘ (skipped)
               │
               └─→ release-summary ✓
```

### Scenario 2: Helm Only
```
Input: apps="", helm_charts="hello-fastapi"

validate-inputs ✓
     │
     ├─→ plan-release ⊘ (skipped)
     │        │
     │        ├─→ release ⊘ (skipped)
     │        │
     │        └─→ create-github-releases ⊘ (skipped)
     │
     └─→ release-helm-charts ✓
               │
               └─→ release-summary ✓
```

### Scenario 3: Both
```
Input: apps="all", helm_charts="all"

validate-inputs ✓
     │
     ├─→ plan-release ✓
     │        │
     │        └─→ release ✓
     │                │
     │                └─→ create-github-releases ✓
     │
     └─→ release-helm-charts ✓
               │
               └─→ release-summary ✓
```

### Scenario 4: Neither (ERROR)
```
Input: apps="", helm_charts=""

validate-inputs ✗ (fails with error message)
     │
     └─→ All subsequent jobs skipped
```

## Version Resolution for Helm Charts

```
┌─────────────────────────────────────────┐
│ Is plan-release job successful?         │
└─────────────┬───────────────────────────┘
              │
      ┌───────┴────────┐
      │                │
     YES              NO
      │                │
      ▼                ▼
┌──────────────┐  ┌────────────────────┐
│ Use version  │  │ Use input version  │
│ from plan-   │  │ or "auto" if empty │
│ release job  │  │                    │
└──────────────┘  └────────────────────┘
```

## Key Differences from Previous Workflow

| Aspect | Before | After |
|--------|--------|-------|
| **Apps input** | Required (default: 'all') | Optional (default: '') |
| **Validation** | Only version options | Release target + version options |
| **Helm dependency** | Depends on `release` job | Depends on `validate-inputs` and `plan-release` |
| **Independent execution** | No - apps always run | Yes - apps or helm can run independently |
| **Version for helm** | Always from plan-release | From plan-release OR inputs OR auto |
| **Summary handling** | Assumes all jobs ran | Handles skipped jobs gracefully |
