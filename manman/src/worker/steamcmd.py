import logging
import os
import subprocess

from opentelemetry import trace

from manman.src.worker.processbuilder import ProcessBuilder, ProcessBuilderStatus

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)


class SteamCMD:
    DEFAULT_USERNAME = "anonymous"
    DEFAULT_EXECUTABLE = "/opt/steamcmd/steamcmd.sh"

    def __init__(
        self,
        install_dir: str,
        username: str = DEFAULT_USERNAME,
        password: str | None = None,
        steamcmd_executable: str | None = None,
    ) -> None:
        self._install_dir = install_dir

        if username != SteamCMD.DEFAULT_USERNAME and password is None:
            raise Exception(
                "non-anonymous username specified and password not provided"
            )
        self._username = username
        self._password = password

        if steamcmd_executable is not None:
            raise NotImplementedError(
                "steamcmd_executable is not supported, use the env var"
            )
        env_steamcmd_executable = os.environ.get("STEAMCMD_EXECUTABLE")
        self._steamcmd_executable = (
            env_steamcmd_executable or SteamCMD.DEFAULT_EXECUTABLE
        )

        logger.info("using login [%s]", self._username)
        # don't log password
        logger.info("using steamcmd executable [%s]", self._steamcmd_executable)

    def install(self, app_id: int, post_install_commands: list[str] | None = None):
        """
        install provided app_id
        limited to a single server per app_id

        :param app_id: steam app_id
        :param post_install_commands: optional list of shell commands to run after installation
        """
        with tracer.start_as_current_span("steamcmd_install") as span:
            span.set_attribute("steamcmd.app_id", app_id)
            span.set_attribute("steamcmd.install_dir", self._install_dir)
            span.set_attribute("steamcmd.username", self._username)
            
            logger.info("installing app_id=[%s]", app_id)

            # prepare directory
            if not os.path.exists(self._install_dir):
                logger.info("directroy not found, creating=[%s]", self._install_dir)
                os.makedirs(self._install_dir)

            # leave a little something behind
            # check_file_name = os.path.join(self._install_dir, ".manman")
            # pathlib.Path(check_file_name).touch()

            pb = ProcessBuilder(self._steamcmd_executable)
            # steamcmd is different and uses + for args
            # TODO - temp? should come from config
            pb.add_parameter("+@sSteamCmdForcePlatformType", "linux")
            pb.add_parameter("+force_install_dir", self._install_dir)
            pb.add_parameter("+login", self._username)
            if self._password is not None:
                pb.add_parameter_stdin(self._password)
            pb.add_parameter("+app_update", str(app_id))
            pb.add_parameter("+exit")

            pb.run(wait=True)
            if pb.status == ProcessBuilderStatus.STOPPED:
                span.set_attribute("steamcmd.status", "success")
                logger.info("successfully installed app_id=[%s]", app_id)
                
                # Execute post-install commands if provided
                if post_install_commands:
                    self._run_post_install_commands(post_install_commands, span)
                    
            elif pb.status == ProcessBuilderStatus.FAILED:
                span.set_attribute("steamcmd.status", "failed")
                span.set_attribute("steamcmd.exit_code", pb.exit_code)
                logger.error(
                    "failed to install app_id=[%s], exit code: %s", app_id, pb.exit_code
                )
                raise Exception(f"SteamCMD failed (exit code: {pb.exit_code})")

    def _run_post_install_commands(self, commands: list[str], span):
        """Execute post-installation shell commands.
        
        Commands are executed with shell=True, so they can use shell features like
        conditionals, pipes, etc. to ensure idempotency.
        
        Example idempotent commands:
        - mkdir -p /path/to/dir  (creates only if doesn't exist)
        - ln -sf source target    (force symlink, replaces if exists)
        - [ -f file ] || touch file  (create only if doesn't exist)
        """
        logger.info("Running %d post-install commands", len(commands))
        span.set_attribute("steamcmd.post_install_commands_count", len(commands))
        
        for idx, command in enumerate(commands):
            logger.info("Executing post-install command [%d/%d]: %s", idx + 1, len(commands), command)
            try:
                result = subprocess.run(
                    command,
                    shell=True,
                    check=True,
                    capture_output=True,
                    text=True,
                    cwd=self._install_dir,
                )
                logger.info("Command output: %s", result.stdout.strip() if result.stdout else "(no output)")
                if result.stderr:
                    logger.warning("Command stderr: %s", result.stderr.strip())
            except subprocess.CalledProcessError as e:
                logger.error(
                    "Post-install command failed (exit code %d): %s\nstdout: %s\nstderr: %s",
                    e.returncode,
                    command,
                    e.stdout,
                    e.stderr,
                )
                span.set_attribute("steamcmd.post_install_failed", True)
                span.set_attribute("steamcmd.post_install_failed_command", command)
                raise Exception(f"Post-install command failed: {command}") from e
        
        logger.info("All post-install commands completed successfully")
