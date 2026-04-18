---
name: manmanv2-add-game
description: Add a new game, gameconfig, and action seed scripts to manmanv2, then deploy to prod
---

# ManManV2 — Add New Game

Adds a new game server to manmanv2: creates `load-<game>-config.sh` and
`seed_<game>_actions.sh` in `manmanv2/scripts/`, then runs them against prod.

## Usage

```
/manmanv2-add-game <game-name> <docker-image> <port> [host-port]
```

Examples:
```
/manmanv2-add-game "Necesse" brammys/necesse-server:latest 14159/udp 38159
/manmanv2-add-game "Valheim" lloesche/valheim-server:latest 2456/udp
```

## What to gather before starting

1. **Docker image** — check the image's README for:
   - Default port(s) and protocols (TCP/UDP)
   - Required/optional environment variables
   - Volume paths for persistent data
2. **Host port** — the external port to expose (may differ from container port)
3. **Game metadata** — genre, publisher, relevant tags

Fetch the image README with:
```bash
gh api repos/<owner>/<repo>/readme --jq '.content' | base64 -d
```

## Step 1 — Write `load-<game>-config.sh`

Copy the structure from an existing script (Necesse is the simplest example):

```
manmanv2/scripts/load-necesse-config.sh
```

Key sections to customise:
- **Header comment** — update game name, image, default config name
- **Default variables** — `GAME_NAME`, `GAME_CONFIG_NAME`, `IMAGE`
- **`create_game_payload`** — set `steam_app_id` (empty string if not on Steam), `metadata`
- **`create_config_payload`** — set `env_template` from the image's env vars
- **Volume** — set `container_path` and `host_subpath` from the image's volume paths
- **Port bindings** — set `container_port`, `host_port`, `protocol` (TCP/UDP)
- **Summary block** — update port description and next-steps text

The script must `source "${SCRIPT_DIR}/common.sh"` — do not inline auth/grpc logic.

## Step 2 — Write `seed_<game>_actions.sh`

Copy the structure from an existing seed script:

```
manmanv2/scripts/seed_necesse_actions.sh   # simple, no select fields
manmanv2/scripts/seed_cs2_actions.sh       # complex, with select/option fields
```

Each action uses `create_action` from `common.sh`. Fields:

| Field | Notes |
|-------|-------|
| `name` | snake_case, unique within game |
| `label` | Human-readable button text |
| `command_template` | Console command sent to server; use `{{.field}}` for inputs |
| `group_name` | Groups buttons in UI (e.g. "Player Management", "World Management") |
| `button_style` | `primary` / `success` / `warning` / `danger` |
| `requires_confirmation` | `true` for destructive actions (kick, ban) |
| `input_fields` | Array of `{name, label, field_type, required}` — omit if no inputs |

Common actions to consider: kick, ban, broadcast message, save world, change map.

## Step 3 — Make scripts executable and commit

```bash
chmod +x manmanv2/scripts/load-<game>-config.sh manmanv2/scripts/seed_<game>_actions.sh
git add manmanv2/scripts/load-<game>-config.sh manmanv2/scripts/seed_<game>_actions.sh
git commit -m "feat(manmanv2/scripts): add <Game> game config and action seed scripts"
```

## Step 4 — Run against prod

Auth uses OIDC client credentials. The credentials live outside the repo — ask
the user to provide them or check a secrets manager.

```bash
# Required env vars
CONTROL_API_ADDR=api.manmanv2.whalenet.dev:443
GRPC_AUTH_MODE=oidc
GRPC_AUTH_TOKEN_URL=https://auth.whalenet.dev/realms/whalenet/protocol/openid-connect/token
GRPC_AUTH_CLIENT_ID=<client-id>
GRPC_AUTH_CLIENT_SECRET=<client-secret>

# Load game + config + deploy
CONTROL_API_ADDR=... GRPC_AUTH_MODE=oidc ... \
  ./manmanv2/scripts/load-<game>-config.sh

# Seed actions
CONTROL_API_ADDR=... GRPC_AUTH_MODE=oidc ... \
  ./manmanv2/scripts/seed_<game>_actions.sh
```

TLS is auto-detected when the address ends in `:443`.

## Reference

- `manmanv2/scripts/common.sh` — shared utilities (grpc_call, setup_auth, find_game_id_by_name, etc.)
- `manmanv2/scripts/load-necesse-config.sh` — minimal load script example
- `manmanv2/scripts/load-cs2-config.sh` — load script with volume + port bindings
- `manmanv2/scripts/seed_necesse_actions.sh` — minimal seed script example
- `manmanv2/scripts/seed_cs2_actions.sh` — seed script with select fields and options
- `.claude/skills/manmanv2-grpc.md` — raw grpcurl reference for the API
