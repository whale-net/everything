# 3rd Party Docker Image Support

## Overview

ManManV2 supports running game servers from any Docker image, including popular 3rd party images from Docker Hub. This allows you to use official game server images without building custom containers.

## Supported Images

### Minecraft (itzg/minecraft-server)

**Image:** `itzg/minecraft-server:latest`

**Documentation:** https://docker-minecraft-server.readthedocs.io/

**Example GameConfig:**

```json
{
  "name": "Minecraft Server (Official)",
  "image": "itzg/minecraft-server:latest",
  "env_template": {
    "EULA": "TRUE",
    "TYPE": "{{server_type}}",
    "VERSION": "{{minecraft_version}}",
    "MAX_PLAYERS": "{{max_players}}",
    "DIFFICULTY": "{{difficulty}}",
    "MODE": "{{game_mode}}",
    "PVP": "{{pvp}}",
    "MOTD": "{{server_motd}}"
  },
  "parameters": [
    {
      "key": "server_type",
      "type": "string",
      "default_value": "VANILLA",
      "description": "Server type: VANILLA, PAPER, SPIGOT, FORGE, FABRIC"
    },
    {
      "key": "minecraft_version",
      "type": "string",
      "default_value": "LATEST",
      "description": "Minecraft version (e.g., 1.20.1, LATEST)"
    },
    {
      "key": "max_players",
      "type": "int",
      "default_value": "20",
      "required": true
    },
    {
      "key": "difficulty",
      "type": "string",
      "default_value": "normal",
      "description": "peaceful, easy, normal, hard"
    },
    {
      "key": "game_mode",
      "type": "string",
      "default_value": "survival",
      "description": "survival, creative, adventure"
    },
    {
      "key": "pvp",
      "type": "bool",
      "default_value": "true"
    },
    {
      "key": "server_motd",
      "type": "string",
      "default_value": "A ManManV2 Server"
    }
  ]
}
```

**Port Bindings:**
- Container: `25565/tcp` → Host: `25565/tcp` (game)
- Container: `25575/tcp` → Host: `25575/tcp` (RCON, optional)

**Data Volume:**
- Mount: `/data` (world data, config, logs)

---

### Valheim (lloesche/valheim-server)

**Image:** `lloesche/valheim-server:latest`

**Documentation:** https://github.com/lloesche/valheim-server-docker

**Example GameConfig:**

```json
{
  "name": "Valheim Dedicated Server",
  "image": "lloesche/valheim-server:latest",
  "env_template": {
    "SERVER_NAME": "{{server_name}}",
    "WORLD_NAME": "{{world_name}}",
    "SERVER_PASS": "{{server_password}}",
    "SERVER_PUBLIC": "{{server_public}}"
  },
  "parameters": [
    {
      "key": "server_name",
      "type": "string",
      "default_value": "ManManV2 Valheim",
      "required": true
    },
    {
      "key": "world_name",
      "type": "string",
      "default_value": "Dedicated",
      "required": true
    },
    {
      "key": "server_password",
      "type": "secret",
      "default_value": "",
      "description": "Server password (leave empty for public)"
    },
    {
      "key": "server_public",
      "type": "bool",
      "default_value": "true",
      "description": "List server in public server browser"
    }
  ]
}
```

**Port Bindings:**
- Container: `2456/udp` → Host: `2456/udp` (game)
- Container: `2457/udp` → Host: `2457/udp` (game)
- Container: `2458/udp` → Host: `2458/udp` (game)

**Data Volume:**
- Mount: `/config` (world saves, server config)

---

### Terraria (ryshe/terraria)

**Image:** `ryshe/terraria:latest`

**Documentation:** https://github.com/ryansheehan/terraria

**Example GameConfig:**

```json
{
  "name": "Terraria Server",
  "image": "ryshe/terraria:vanilla-latest",
  "env_template": {
    "WORLD": "{{world_name}}",
    "PASS": "{{server_password}}"
  },
  "command": ["-world", "/root/.local/share/Terraria/Worlds/{{world_name}}.wld", "-autocreate", "{{world_size}}"],
  "parameters": [
    {
      "key": "world_name",
      "type": "string",
      "default_value": "Terraria",
      "required": true
    },
    {
      "key": "world_size",
      "type": "int",
      "default_value": "2",
      "description": "World size: 1=small, 2=medium, 3=large"
    },
    {
      "key": "server_password",
      "type": "secret",
      "default_value": "",
      "description": "Server password (optional)"
    }
  ]
}
```

**Port Bindings:**
- Container: `7777/tcp` → Host: `7777/tcp` (game)

---

### Palworld (thijsvanloef/palworld-server-docker)

**Image:** `thijsvanloef/palworld-server-docker:latest`

**Documentation:** https://github.com/thijsvanloef/palworld-server-docker

**Example GameConfig:**

```json
{
  "name": "Palworld Dedicated Server",
  "image": "thijsvanloef/palworld-server-docker:latest",
  "env_template": {
    "PUID": "1000",
    "PGID": "1000",
    "PORT": "8211",
    "PLAYERS": "{{max_players}}",
    "COMMUNITY": "{{community_server}}",
    "SERVER_NAME": "{{server_name}}",
    "SERVER_PASSWORD": "{{server_password}}"
  },
  "parameters": [
    {
      "key": "max_players",
      "type": "int",
      "default_value": "32",
      "required": true
    },
    {
      "key": "server_name",
      "type": "string",
      "default_value": "ManManV2 Palworld",
      "required": true
    },
    {
      "key": "server_password",
      "type": "secret",
      "default_value": ""
    },
    {
      "key": "community_server",
      "type": "bool",
      "default_value": "true",
      "description": "Enable community server (visible in server browser)"
    }
  ]
}
```

**Port Bindings:**
- Container: `8211/udp` → Host: `8211/udp` (game)
- Container: `27015/udp` → Host: `27015/udp` (query, optional)

---

## Configuration Options

### Image

The Docker image to use. Can be:
- **Official images**: `minecraft:latest`, `steamcmd/steamcmd:latest`
- **Community images**: `itzg/minecraft-server:latest`, `lloesche/valheim-server:latest`
- **Custom registries**: `ghcr.io/user/game-server:v1.0`, `registry.example.com/game:latest`

### Entrypoint Override

Use when the image's default `ENTRYPOINT` is incompatible with ManManV2's wrapper model.

**Example:** Override bash entrypoint
```json
{
  "entrypoint": ["/bin/sh", "-c"]
}
```

**When to use:**
- Image uses a custom entrypoint script that conflicts with wrapper
- Need to run initialization commands before the game server
- Image entrypoint doesn't support passing arguments properly

### Command Override

Alternative to `args_template` for complex command structures.

**Example:** Multi-step command
```json
{
  "command": [
    "sh", "-c",
    "echo 'Starting server...' && ./game-server --config=/data/config.ini"
  ]
}
```

**When to use:**
- Complex startup logic (multiple commands, pipes, redirects)
- 3rd party image expects specific command structure
- Shell scripting needed for environment setup

### Args Template vs Command

**Args Template** (recommended for simple cases):
```json
{
  "args_template": "--max-players={{max_players}} --difficulty={{difficulty}}"
}
```
- Simple parameter substitution
- Appended to the image's default `CMD`
- Easy to read and maintain

**Command** (for complex cases):
```json
{
  "command": ["./start.sh", "--config={{config_file}}", "--port={{port}}"]
}
```
- Full control over command execution
- Replaces image's default `CMD`
- Supports arrays for proper argument handling

## Image Pull Strategy

ManManV2 automatically pulls images when needed:

1. **On deployment**: Pulls image when creating ServerGameConfig
2. **On session start**: Verifies image exists, pulls if missing
3. **Version tags**: Use specific tags for reproducibility (e.g., `itzg/minecraft-server:java17`)

### Private Registries

To use private registries, configure Docker credentials on the host server:

```bash
docker login registry.example.com
# Credentials stored in ~/.docker/config.json
```

The host manager will use these credentials automatically.

## Data Persistence

3rd party images typically expect data in specific directories:

### Volume Mounts

ManManV2 mounts `/data/{session_id}/game` to the container. Map this to the image's expected path:

**Minecraft (`itzg/minecraft-server`):**
- Image expects: `/data`
- Mount: `/data/{session_id}/game:/data`

**Valheim (`lloesche/valheim-server`):**
- Image expects: `/config`
- Mount: `/data/{session_id}/game:/config`

### File Templates

Use `files` to inject configuration:

```json
{
  "files": [
    {
      "path": "/data/server.properties",
      "content": "max-players={{max_players}}\ndifficulty={{difficulty}}",
      "mode": "0644",
      "is_template": true
    }
  ]
}
```

## Best Practices

### 1. Use Version Tags

❌ Bad: `minecraft:latest` (unpredictable updates)
✅ Good: `minecraft:1.20.1` (reproducible)

### 2. Set Required Parameters

Always mark critical parameters as required:

```json
{
  "parameters": [
    {
      "key": "server_name",
      "type": "string",
      "required": true
    }
  ]
}
```

### 3. Document Port Requirements

Specify all required ports in documentation:

```
Required ports:
- 25565/tcp (game server)
- 25575/tcp (RCON, optional)
```

### 4. Test with Wrapper

Ensure the image works with ManManV2's wrapper:
- Stdout/stderr are not buffered
- Process handles SIGTERM gracefully
- No interactive prompts on startup

### 5. Provide Defaults

Make it easy to get started with sensible defaults:

```json
{
  "parameters": [
    {
      "key": "difficulty",
      "type": "string",
      "default_value": "normal",
      "description": "Game difficulty"
    }
  ]
}
```

## Troubleshooting

### Image Won't Start

**Check wrapper logs:**
```bash
docker logs manmanv2-wrapper-{session_id}
```

**Common issues:**
- Image expects interactive terminal (use `-i` flag)
- Entrypoint conflicts with wrapper
- Missing environment variables

### Permission Errors

Some images run as specific users. Ensure data directory permissions match:

```bash
chown -R 1000:1000 /data/{session_id}/game
```

### Port Conflicts

Verify ports aren't already in use:

```bash
netstat -tuln | grep <port>
```

## Adding New Images

To add support for a new 3rd party image:

1. **Research the image**:
   - Read the image documentation
   - Check required environment variables
   - Note expected volume mounts
   - Test locally with `docker run`

2. **Create GameConfig**:
   - Define all configurable parameters
   - Set up `env_template` for environment variables
   - Configure `command`/`args_template` if needed
   - Add `files` for configuration injection

3. **Test deployment**:
   - Deploy to a test server
   - Start a session
   - Verify game server starts correctly
   - Test parameter changes

4. **Document**:
   - Add to this guide
   - Include port requirements
   - Note any special considerations

## Limitations

- No support for `docker-compose` multi-container setups
- Some images may require `--privileged` (security risk)
- Interactive images (those requiring stdin) may not work
- Images with custom health checks need manual validation

## Future Enhancements

- Image metadata auto-detection (extract parameters from Dockerfile)
- Image compatibility scoring
- Pre-configured templates for popular games
- Image update notifications
