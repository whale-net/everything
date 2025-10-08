# Tilt Quick Reference

Quick commands and patterns for using Tilt in the Everything monorepo.

## Common Commands

```bash
# Start Tilt UI and development environment
tilt up

# Start in non-interactive mode (for CI/debugging)
tilt up -- --stream

# Stop all resources but keep Tilt running
tilt down

# Stop Tilt completely
tilt down && killall tilt

# View logs for a specific resource
tilt logs <resource-name>

# Trigger manual rebuild
tilt trigger <resource-name>

# Get resource status
tilt get
```

## Quick Setup

### First Time Setup

```bash
# 1. Install prerequisites
brew install tilt-dev/tap/tilt bazelisk helm

# 2. Start Kubernetes (Docker Desktop)
# Enable Kubernetes in Docker Desktop settings

# 3. Setup environment
cp .env.example .env
# Edit .env to enable desired services

# 4. Build charts (one-time)
bazel build //demo/...
```

### Daily Development

```bash
# Start your environment
cd /path/to/everything
tilt up

# Access Tilt UI: http://localhost:10350
# Access your apps via port-forwards or ingress

# When done
tilt down
```

## Environment Variables Cheat Sheet

### Root .env

```bash
# Enable domains
TILT_ENABLE_DEMO=true
TILT_ENABLE_MANMAN=false

# Platform (M1/M2 Macs)
TILT_PLATFORM=linux_arm64

# Platform (Intel Macs/Linux)
TILT_PLATFORM=linux_x86_64
```

### demo/.env

```bash
# Enable specific apps
DEMO_ENABLE_FASTAPI=true
DEMO_ENABLE_INTERNAL_API=false
DEMO_ENABLE_WORKER=false
DEMO_ENABLE_HELLO_PYTHON=false
DEMO_ENABLE_HELLO_GO=false
DEMO_ENABLE_JOB=false

# Infrastructure
DEMO_ENABLE_INGRESS=true
```

## Development Patterns

### Pattern 1: Single App Focus

Fastest startup, minimal resources.

```bash
# In .env
TILT_ENABLE_DEMO=true
TILT_ENABLE_MANMAN=false

# In demo/.env
DEMO_ENABLE_FASTAPI=true
# All others = false
```

### Pattern 2: API Development

Work on multiple APIs together.

```bash
# In demo/.env
DEMO_ENABLE_FASTAPI=true
DEMO_ENABLE_INTERNAL_API=true
DEMO_ENABLE_INGRESS=true
# Workers = false
```

### Pattern 3: Full Stack

Everything running locally.

```bash
# In .env
TILT_ENABLE_DEMO=true
TILT_ENABLE_MANMAN=true

# Enable all services in respective .env files
```

## Access Points

### Demo Apps

```bash
# FastAPI (external-api)
http://localhost:30080  # Via ingress
http://localhost:8000   # Direct port-forward

# Internal API
http://localhost:8080   # Direct port-forward

# Workers/Jobs
tilt logs <worker-name>  # View logs in terminal
# Or check Tilt UI
```

### Manman Apps

```bash
# Experience API
http://localhost:30080/experience/

# Worker DAL API  
http://localhost:30080/workerdal/

# Status API
http://localhost:30080/status/
```

## Troubleshooting Quick Fixes

### "chart not found"

```bash
bazel build //demo/...
```

### "no such image"

```bash
# Rebuild the image
bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_arm64
```

### "port already in use"

```bash
# Find and kill process using the port
lsof -ti:8000 | xargs kill -9
```

### "namespace already exists"

```bash
kubectl delete namespace demo-dev
kubectl delete namespace manman-dev
```

### Kubernetes not responding

```bash
# Restart Docker Desktop Kubernetes
# Or restart your K8s cluster
kubectl cluster-info
```

### Bazel build fails

```bash
bazel clean --expunge
bazel build //demo/...
```

## Resource Labels

Resources in Tilt UI are organized by labels:

- `build`: Build resources
- `api`: API services
- `worker`: Background workers
- `job`: One-time jobs
- `infra`: Infrastructure (databases, etc.)

Filter by label in Tilt UI to focus on specific resources.

## Tips & Tricks

### Fast Iteration

1. **Use labels** to focus on relevant resources
2. **Disable unused services** in .env to save resources
3. **Watch specific logs** instead of all logs
4. **Use port-forwards** for direct access during debugging

### Resource Management

```bash
# Check resource usage
kubectl top pods -n demo-dev

# Scale down replicas
kubectl scale deployment <name> --replicas=0 -n demo-dev

# Delete stuck pods
kubectl delete pod <pod-name> -n demo-dev --force
```

### Debugging

```bash
# Get pod details
kubectl describe pod <pod-name> -n demo-dev

# Get pod logs
kubectl logs <pod-name> -n demo-dev -f

# Execute into pod
kubectl exec -it <pod-name> -n demo-dev -- /bin/sh

# Check events
kubectl get events -n demo-dev --sort-by='.lastTimestamp'
```

## File Locations

```
.
├── Tiltfile                      # Root orchestrator
├── .env.example                  # Configuration template
├── .env                          # Your local config (gitignored)
├── demo/
│   ├── Tiltfile                  # Demo apps configuration
│   ├── .env.example              # Demo config template
│   └── .env                      # Your demo config (gitignored)
├── manman/
│   └── Tiltfile                  # Manman services config
└── docs/
    └── TILT_DEVELOPMENT.md       # Full documentation
```

## Help & Resources

- **Tilt UI**: http://localhost:10350
- **Tilt Docs**: https://docs.tilt.dev/
- **This Repo**: [TILT_DEVELOPMENT.md](TILT_DEVELOPMENT.md)
- **Kubernetes**: Use `kubectl` for advanced debugging

---

**Remember**: Always build helm charts before running tilt!

```bash
bazel build //demo/...
```
