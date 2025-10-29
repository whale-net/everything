"""
Seed test data for game server configuration.

Creates sample data for local development:
- GameServer (Left 4 Dead 2)
- GameServerConfig (Default coop config)
- GameServerCommands (Map change, gamemode, etc.)
- GameServerCommandDefaults (Popular campaigns)
- GameServerConfigCommands (Config-specific commands)
- GameServerConfigOptions (Args, env vars, post-install scripts)
"""

from manman.src.models import (
    GameServer,
    GameServerConfig,
    GameServerCommand,
    GameServerCommandDefaults,
    GameServerConfigCommands,
    GameServerConfigOption,
    ConfigOptionType,
    ServerType,
)


def create_test_data(session):
    """Create test data for game server configuration."""
    
    # 1. Create a GameServer (Left 4 Dead 2)
    l4d2_server = GameServer(
        name="TestL4D2",
        server_type=ServerType.STEAM,
        app_id=222860,  # L4D2 app ID
    )
    session.add(l4d2_server)
    session.flush()  # Get the ID
    
    # 2. Create a GameServerConfig (Default coop config)
    coop_config = GameServerConfig(
        game_server_id=l4d2_server.game_server_id,
        name="Coop Campaign",
        is_default=True,
        is_visible=True,
        executable="./srcds_run",
        args=[],  # Will be replaced by options
        env_var=[],  # Will be replaced by options
        post_install_commands=[],  # Will be replaced by options
    )
    session.add(coop_config)
    session.flush()
    
    # 3. Create GameServerCommands (reusable command templates)
    commands = [
        GameServerCommand(
            game_server_id=l4d2_server.game_server_id,
            name="change_map",
            command="map {map_name}",
            description="Change to a different campaign/map",
            is_visible=True,
        ),
        GameServerCommand(
            game_server_id=l4d2_server.game_server_id,
            name="set_difficulty",
            command="z_difficulty {difficulty}",
            description="Change difficulty (easy, normal, hard, impossible)",
            is_visible=True,
        ),
        GameServerCommand(
            game_server_id=l4d2_server.game_server_id,
            name="enable_cheats",
            command="sv_cheats 1",
            description="Enable cheats on the server",
            is_visible=True,
        ),
        GameServerCommand(
            game_server_id=l4d2_server.game_server_id,
            name="disable_cheats",
            command="sv_cheats 0",
            description="Disable cheats on the server",
            is_visible=True,
        ),
        GameServerCommand(
            game_server_id=l4d2_server.game_server_id,
            name="restart_map",
            command="map {current_map}",
            description="Restart the current map",
            is_visible=True,
        ),
    ]
    session.add_all(commands)
    session.flush()
    
    # 4. Create GameServerCommandDefaults (popular campaigns and settings)
    command_defaults = [
        # Map changes - popular L4D2 campaigns
        GameServerCommandDefaults(
            game_server_command_id=commands[0].game_server_command_id,  # change_map
            command_value="c1m1_hotel",
            description="Dead Center - Hotel",
            is_visible=True,
        ),
        GameServerCommandDefaults(
            game_server_command_id=commands[0].game_server_command_id,  # change_map
            command_value="c2m1_highway",
            description="Dark Carnival - Highway",
            is_visible=True,
        ),
        GameServerCommandDefaults(
            game_server_command_id=commands[0].game_server_command_id,  # change_map
            command_value="c5m1_waterfront",
            description="The Parish - Waterfront",
            is_visible=True,
        ),
        GameServerCommandDefaults(
            game_server_command_id=commands[0].game_server_command_id,  # change_map
            command_value="c8m1_apartment",
            description="No Mercy - Apartment",
            is_visible=True,
        ),
        # Difficulties
        GameServerCommandDefaults(
            game_server_command_id=commands[1].game_server_command_id,  # set_difficulty
            command_value="Easy",
            description="Easy difficulty",
            is_visible=True,
        ),
        GameServerCommandDefaults(
            game_server_command_id=commands[1].game_server_command_id,  # set_difficulty
            command_value="Normal",
            description="Normal difficulty",
            is_visible=True,
        ),
        GameServerCommandDefaults(
            game_server_command_id=commands[1].game_server_command_id,  # set_difficulty
            command_value="Hard",
            description="Advanced difficulty",
            is_visible=True,
        ),
        GameServerCommandDefaults(
            game_server_command_id=commands[1].game_server_command_id,  # set_difficulty
            command_value="Impossible",
            description="Expert difficulty",
            is_visible=True,
        ),
    ]
    session.add_all(command_defaults)
    session.flush()
    
    # 5. Create GameServerConfigCommands (config-specific commands)
    # For coop config, no cheats and start with Dead Center
    config_commands = [
        GameServerConfigCommands(
            game_server_config_id=coop_config.game_server_config_id,
            game_server_command_id=commands[3].game_server_command_id,  # disable_cheats
            command_value="",  # No parameters needed
            description="Ensure cheats are disabled in coop",
            is_visible=True,
        ),
        GameServerConfigCommands(
            game_server_config_id=coop_config.game_server_config_id,
            game_server_command_id=commands[0].game_server_command_id,  # change_map
            command_value="c1m1_hotel",  # Start with Dead Center
            description="Default starting campaign",
            is_visible=True,
        ),
    ]
    session.add_all(config_commands)
    session.flush()
    
    # 6. Create GameServerConfigOptions (args, env vars, post-install scripts)
    options = [
        # Args
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.ARG,
            value="-game left4dead2",
            order=0,
            is_enabled=True,
            description="Specify L4D2 game mode",
        ),
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.ARG,
            value="-console",
            order=1,
            is_enabled=True,
            description="Enable console",
        ),
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.ARG,
            value="-port 27015",
            order=2,
            is_enabled=True,
            description="Server port",
        ),
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.ARG,
            value="+map c1m1_hotel",
            order=3,
            is_enabled=True,
            description="Starting map (Dead Center)",
        ),
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.ARG,
            value="+maxplayers 4",
            order=4,
            is_enabled=True,
            description="Maximum players",
        ),
        # Env vars
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.ENV_VAR,
            value="STEAM_GAMESERVER_TOKEN=your_token_here",
            order=0,
            is_enabled=True,
            description="Steam Game Server Token",
        ),
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.ENV_VAR,
            value="L4D2_HOSTNAME=My L4D2 Server",
            order=1,
            is_enabled=True,
            description="Server name displayed in browser",
        ),
        # Post-install scripts
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.POST_INSTALL_SCRIPT,
            value="chmod +x ./srcds_run",
            order=-1,  # Run before default
            is_enabled=True,
            description="Make binary executable",
        ),
        GameServerConfigOption(
            game_server_config_id=coop_config.game_server_config_id,
            option_type=ConfigOptionType.POST_INSTALL_SCRIPT,
            value="echo 'sv_cheats 0' > left4dead2/cfg/server.cfg",
            order=0,
            is_enabled=True,
            description="Create server config",
        ),
    ]
    session.add_all(options)
    session.flush()
    
    session.commit()
    
    return {
        "server": l4d2_server,
        "config": coop_config,
        "commands": commands,
        "command_defaults": command_defaults,
        "config_commands": config_commands,
        "options": options,
    }


if __name__ == "__main__":
    import os
    from sqlalchemy import create_engine
    from sqlalchemy.orm import sessionmaker
    
    # Get database URL from environment or use default
    db_url = os.environ.get(
        "POSTGRES_URL",
        "postgresql://postgres:password@localhost:5432/manman"
    )
    
    engine = create_engine(db_url)
    Session = sessionmaker(bind=engine)
    session = Session()
    
    try:
        data = create_test_data(session)
        print("✅ Test data created successfully!")
        print(f"  - Server: {data['server'].name} (ID: {data['server'].game_server_id})")
        print(f"  - Config: {data['config'].name} (ID: {data['config'].game_server_config_id})")
        print(f"  - Commands: {len(data['commands'])} created")
        print(f"  - Command Defaults: {len(data['command_defaults'])} created")
        print(f"  - Config Commands: {len(data['config_commands'])} created")
        print(f"  - Config Options: {len(data['options'])} created")
    except Exception as e:
        session.rollback()
        print(f"❌ Error creating test data: {e}")
        raise
    finally:
        session.close()
