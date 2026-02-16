# ManManV2 Management UI

HTMX-based web interface for managing the ManManV2 control plane.

## Overview

This is the primary management interface for ManManV2 at `manman.whalenet.dev`. It provides a web UI for:

- **Dashboard**: System overview with server/game/session statistics
- **Games Management**: Create and configure games and game configurations
- **Servers**: Monitor registered servers and their deployments
- **Sessions**: Track active and historical game server sessions (Phase 2)

## Architecture

- **Frontend**: HTMX for dynamic updates without custom JavaScript
- **Backend**: Go HTTP server
- **API**: gRPC client connecting to ManManV2 control-api
- **Auth**: Dual-mode authentication (OIDC or no-auth for development)

## Key Dependencies

- `//libs/go/htmxauth` - Authentication middleware
- `//libs/go/grpcclient` - gRPC connection management
- `//manman/protos:manmanpb` - Generated gRPC stubs

## Local Development

### With Tilt (Recommended)

```bash
cd manman-v2/
tilt up
```

The UI will be available at http://localhost:8080

### Standalone

```bash
# Build the binary
bazel build //manman-v2/ui:manmanv2-ui

# Run with environment variables
CONTROL_API_URL=localhost:50051 \
AUTH_MODE=none \
./bazel-bin/manman-v2/ui/manmanv2-ui_/manmanv2-ui
```

## Configuration

Environment variables:

- `HOST` - Server host (default: 0.0.0.0)
- `PORT` - Server port (default: 8000)
- `AUTH_MODE` - Authentication mode: `none` or `oidc` (default: none)
- `CONTROL_API_URL` - gRPC API address (default: control-api-dev-service:50051)
- `SECRET_KEY` - Session secret for OIDC mode
- `OIDC_ISSUER` - OIDC provider URL (if AUTH_MODE=oidc)
- `OIDC_CLIENT_ID` - OIDC client ID
- `OIDC_CLIENT_SECRET` - OIDC client secret
- `OIDC_REDIRECT_URI` - OIDC callback URL

## Project Structure

```
manman-v2/ui/
├── BUILD.bazel              # Bazel build configuration
├── main.go                  # Entry point, config, routing
├── grpc_client.go           # gRPC client wrapper
├── handlers_home.go         # Dashboard handlers
├── handlers_games.go        # Games + GameConfigs CRUD
├── handlers_servers.go      # Servers list + detail
├── templates.go             # Template loading + helpers
└── templates/
    ├── layout.html          # Base layout with nav
    ├── home.html            # Dashboard
    ├── games.html           # Games list
    ├── game_detail.html     # Game detail + configs
    ├── game_form.html       # Create/edit game form
    ├── config_detail.html   # Config detail
    ├── config_form.html     # Create/edit config form
    ├── servers.html         # Servers list
    ├── server_detail.html   # Server detail + deployments
    ├── sessions.html        # Sessions (Phase 2)
    └── partials/
        └── dashboard_summary.html  # Dashboard stats
```

## Phase 1 Features (Current)

✅ Dashboard with system summary
✅ Games CRUD (list, create, edit, delete)
✅ GameConfigs CRUD (nested under games)
✅ Servers list (read-only, self-registered)
✅ Server detail with deployments

## Phase 2 Features (Planned)

- Server detail + ServerGameConfig deployment flow
- Deploy form with validation (ValidateDeployment RPC)
- Session management (list, start, stop)
- Configuration preview
- Backup management

## Development Notes

### Adding New Pages

1. Create handler in `handlers_*.go`
2. Create template in `templates/*.html`
3. Add route in `main.go` `setupRoutes()`
4. Use HTMX attributes for dynamic updates

### HTMX Patterns

- **Forms**: `hx-post` with `hx-target` for inline updates
- **Auto-refresh**: `hx-trigger="load, every 30s"`
- **Delete confirmations**: `hx-delete` with `hx-confirm`
- **Redirects**: `HX-Redirect` response header

### Template Helpers

- `formatTime` - Format Unix timestamp
- `timeAgo` - Relative time (e.g., "5 minutes ago")
- `statusBadge` - CSS class for status badges

## Deployment

The UI is deployed via the ManManV2 helm chart:

```bash
# Build chart
bazel build //manman:manmanv2_chart

# Install with helm
helm install manmanv2 ./bazel-bin/manman/helm-manmanv2-control-services_chart/
```

Ingress configuration in production should point to `manman.whalenet.dev`.
