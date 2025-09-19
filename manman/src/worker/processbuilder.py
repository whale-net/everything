import datetime
import enum
import logging
import os
import subprocess
from typing import Optional

from manman.src.util import log_stream

logger = logging.getLogger(__name__)


class ProcessBuilderStatus(enum.Enum):
    NOTSTARTED = 0
    INIT = 1
    RUNNING = 2
    STOPPED = 3
    FAILED = 4


class ProcessBuilder:
    def __init__(self, executable: str, stdin_delay_seconds: int = 20) -> None:
        self._executable: str = executable
        self._args: list[str] = []
        self._parameter_stdin: list[str] = []
        self._stdin_delay_seconds = stdin_delay_seconds
        self._process_start_time: datetime.datetime | None = None

    @property
    def status(self) -> ProcessBuilderStatus:
        if self._process_start_time is None:
            return ProcessBuilderStatus.NOTSTARTED
        proc_status = self._proc.poll()
        # logger.info('status %s', proc_status)
        if proc_status is not None:
            # Process has exited, check the return code
            if proc_status == 0:
                return ProcessBuilderStatus.STOPPED
            else:
                return ProcessBuilderStatus.FAILED

        current_time = datetime.datetime.now()
        if current_time < self._process_start_time + datetime.timedelta(
            seconds=self._stdin_delay_seconds
        ):
            return ProcessBuilderStatus.INIT
        return ProcessBuilderStatus.RUNNING

    @property
    def exit_code(self) -> int | None:
        """
        Get the exit code of the process if it has exited.

        :return: Exit code if process has exited, None if still running or not started
        """
        if self._process_start_time is None:
            return None
        return self._proc.poll()

    def add_parameter(self, *parameters: str):
        for parm in parameters:
            self._args.append(parm)

    def add_parameter_stdin(self, input: str):
        self._parameter_stdin.append(input)

    def render_command(self) -> tuple[str, str | None]:
        """
        render executable + parameters

        :param self: _description_
        :param str: _description_
        :return: command to run, stdin
        """
        command = self._executable
        if len(self._args) > 0:
            command += " " + " ".join(self._args)
        stdinput = None
        if len(self._parameter_stdin) > 0:
            stdinput = " ".join(self._parameter_stdin)

        return command, stdinput

    def run(self, wait: bool = False, extra_env: Optional[dict[str, str]] = None):
        command_base = os.path.basename(self._executable)
        logger.info("About to start executing [%s]", command_base)

        parm_stdinput_bytes: bytes | None = None
        if len(self._parameter_stdin) > 0:
            # this may contain sensitive info and I don't have a more sophisticated way to handle it
            # so just don't log unless debug. hopefully I don't deploy that way
            logger.debug("stdinput list: %s", self._parameter_stdin)
            stdinput = "".join(arg + "\n" for arg in self._parameter_stdin)
            parm_stdinput_bytes = bytes(stdinput, encoding="ascii")
        proc_command = [self._executable, *self._args]
        logger.info("executing [%s]", " ".join(proc_command))

        # always pass in env, TBD if good idea
        # TODO - untested, not even locally
        env = os.environ.copy()
        env.update(extra_env or {})
        try:
            proc = subprocess.Popen(
                proc_command,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                env=env,
            )
        except Exception as e:
            logger.error("failed to start process [%s]", e)
            raise

        logger.info("started process [%s]", proc.pid)
        if parm_stdinput_bytes is not None:
            # TODO test
            proc.communicate(parm_stdinput_bytes)
        # TODO - is this more than a hack?
        # set this false to allow instant (None) read
        os.set_blocking(proc.stdout.fileno(), False)
        self._process_start_time = datetime.datetime.now()
        self._proc = proc

        # TODO - is this the right place to do this?
        # used by steamcmd to ensure it finishes before starting game server
        if wait:
            logger.info("waiting for process to finish")
            self._proc.wait()

        # if is_subprocess_running and self.status == ProcessBuilderStatus.RUNNING:
        #     # TODO kill logic
        #     while not self._stdin_queue.empty():
        #         stdin_command = self._stdin_queue.get()
        #         if len(stdin_command) == 0:
        #             continue
        #         if not stdin_command.endswith("\n"):
        #             stdin_command = stdin_command + "\n"

        #         stdinput_bytes = bytes(stdin_command, encoding="ascii")
        #         proc.communicate(stdinput_bytes)
        #         self.read_output()

    def stop(self):
        if self.status in (ProcessBuilderStatus.INIT, ProcessBuilderStatus.RUNNING):
            # TODO this needs to run in loop
            # with asyncio.timeout(10):
            #     self._proc.terminate()
            self.kill()

    def kill(self):
        if self.status in (ProcessBuilderStatus.INIT, ProcessBuilderStatus.RUNNING):
            self._proc.kill()

    def read_output(self):
        if self.status == ProcessBuilderStatus.NOTSTARTED:
            return
        log_stream(self._proc.stdout, logger=logger)
        log_stream(self._proc.stderr, logger=logger, prefix="stderr:")

    def write_stdin(self, stdin_command: str):
        status = self.status
        if self.status != ProcessBuilderStatus.RUNNING:
            logger.warning(
                "process is %s, cannot write to stdin. ignored input", status.name
            )
            return
        if not stdin_command.endswith("\n"):
            stdin_command = stdin_command + "\n"
        self._proc.stdin.write(stdin_command.encode(encoding="ascii"))
        self._proc.stdin.flush()
        logger.info("wrote to stdin: %s", stdin_command)
