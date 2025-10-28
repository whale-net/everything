# ManMan Test Data

This directory contains seed data for local development and testing.

## What's Included

The seed data creates a complete Left 4 Dead 2 server configuration:

- **GameServer**: TestL4D2 (Steam App ID: 222860)
- **GameServerConfig**: Coop Campaign configuration
- **GameServerCommands**: 5 reusable command templates
  - change_map
  - set_difficulty
  - enable_cheats
  - disable_cheats
  - restart_map
- **GameServerCommandDefaults**: 8 popular default commands
  - Campaigns: Dead Center, Dark Carnival, The Parish, No Mercy
  - Difficulties: Easy, Normal, Hard (Advanced), Impossible (Expert)
- **GameServerConfigCommands**: 2 config-specific commands
  - Disable cheats (for coop)
  - Default starting campaign (Dead Center)
- **GameServerConfigOptions**: 9 configuration options
  - 5 args (game mode, console, port, map, maxplayers)
  - 2 env vars (token, hostname)
  - 2 post-install scripts (chmod, config creation)

## Usage

### Prerequisites

1. Ensure database is running:
   ```bash
   tilt up --file=manman/Tiltfile
   ```

2. Run migrations to create tables:
   ```bash
   POSTGRES_URL="postgresql://postgres:password@localhost:5432/manman" \
     bazel run //manman/src/host:migration_cli -- run-migration
   ```

### Seed the Database

```bash
POSTGRES_URL="postgresql://postgres:password@localhost:5432/manman" \
  bazel run //manman/test_data:seed_data
```

### Verify Data

You can verify the data was created by checking the database:

```bash
# Connect to database
kubectl exec -it -n manman-local-dev postgres-dev-0 -- psql -U postgres -d manman

# Query the data
\c manman
SELECT * FROM manman.game_servers;
SELECT * FROM manman.game_server_configs;
SELECT * FROM manman.game_server_commands;
SELECT * FROM manman.game_server_command_defaults;
SELECT * FROM manman.game_server_config_commands;
SELECT * FROM manman.game_server_config_options;
```

## Customizing Test Data

To modify the test data, edit `seed_data.py` and re-run the seed command. The script will create new data (it doesn't delete existing data).

To start fresh:

```bash
# Drop and recreate the database
kubectl exec -n manman-local-dev postgres-dev-0 -- psql -U postgres -c "DROP DATABASE manman;"
kubectl exec -n manman-local-dev postgres-dev-0 -- psql -U postgres -c "CREATE DATABASE manman;"

# Re-run migrations
POSTGRES_URL="postgresql://postgres:password@localhost:5432/manman" \
  bazel run //manman/src/host:migration_cli -- run-migration

# Seed data
POSTGRES_URL="postgresql://postgres:password@localhost:5432/manman" \
  bazel run //manman/test_data:seed_data
```
