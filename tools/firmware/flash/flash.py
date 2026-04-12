#!/usr/bin/env python3
"""Universal firmware flash driver.

Invoked by the generated wrapper scripts produced by flash_firmware() in
tools/firmware/flash.bzl.  All file paths arrive as resolved absolute paths
from the shell wrapper (via rlocation), so this script never touches runfiles.

Supported tools:
  --tool esptool   ESP32 / ESP8266 (uses esptool.py)
  --tool avrdude   AVR (ATmega328, ATmega2560, …)
  --tool picotool  Raspberry Pi Pico (RP2040)
  --tool openocd   ARM Cortex-M via JTAG/SWD (generic)

Usage:
  flash.py --tool esptool --chip esp32 --baud 921600 \\
      --before default_reset --after hard_reset \\
      --write-flash-args "--flash_mode dio --flash_freq 80m --flash_size 4MB" \\
      --segment 0x1000=/path/to/bootloader.bin \\
      --segment 0x8000=/path/to/partitions.bin \\
      --segment 0x10000=/path/to/app.bin \\
      --esptool /path/to/esptool_wrapper \\
      --port /dev/ttyUSB0
"""

import argparse
import os
import signal
import subprocess
import sys
from pathlib import Path

_SERIAL_DAEMON_PID_FILE = Path.home() / ".local" / "share" / "serial-mcp" / "daemon.pid"


def _pause_serial_daemon() -> "int | None":
    """SIGSTOP the serial-mcp daemon so esptool can claim the port."""
    if not _SERIAL_DAEMON_PID_FILE.exists():
        return None
    try:
        pid = int(_SERIAL_DAEMON_PID_FILE.read_text().strip())
        os.kill(pid, signal.SIGSTOP)
        return pid
    except (ValueError, ProcessLookupError, OSError):
        return None


def _resume_serial_daemon(pid: "int | None") -> None:
    """SIGCONT the serial-mcp daemon after flashing."""
    if pid is None:
        return
    try:
        os.kill(pid, signal.SIGCONT)
    except (ProcessLookupError, OSError):
        pass


def _check_file(path: str, desc: str) -> None:
    import os
    if not os.path.isfile(path):
        print(f"ERROR: {desc} not found: {path}", file=sys.stderr)
        sys.exit(1)


def flash_esptool(args: argparse.Namespace, segments: list[tuple[str, str]]) -> None:
    _check_file(args.esptool, "esptool")
    cmd = [
        sys.executable, args.esptool,
        "--port", args.port,
        "--chip", args.chip,
        "--baud", str(args.baud),
        "--before", args.before,
        "--after", args.after,
        "write-flash",
    ]
    if args.write_flash_args:
        cmd += args.write_flash_args.split()
    for addr, path in segments:
        _check_file(path, f"segment {addr}")
        cmd += [addr, path]

    print("$ " + " ".join(cmd))
    daemon_pid = _pause_serial_daemon()
    try:
        subprocess.check_call(cmd)
    finally:
        _resume_serial_daemon(daemon_pid)


def flash_avrdude(args: argparse.Namespace, segments: list[tuple[str, str]]) -> None:
    if not segments:
        print("ERROR: no --segment provided for avrdude", file=sys.stderr)
        sys.exit(1)
    _addr, app_path = segments[-1]
    _check_file(app_path, "app binary")
    cmd = [
        "avrdude",
        f"-p{args.part}",
        f"-c{args.programmer}",
        f"-P{args.port}",
        f"-Uflash:w:{app_path}:i",
    ]
    print("$ " + " ".join(cmd))
    subprocess.check_call(cmd)


def flash_picotool(args: argparse.Namespace, segments: list[tuple[str, str]]) -> None:
    if not segments:
        print("ERROR: no --segment provided for picotool", file=sys.stderr)
        sys.exit(1)
    _addr, app_path = segments[-1]
    _check_file(app_path, "app binary")
    cmd = ["picotool", "load", "-x", app_path]
    print("$ " + " ".join(cmd))
    subprocess.check_call(cmd)


def flash_openocd(args: argparse.Namespace, segments: list[tuple[str, str]]) -> None:
    if not segments:
        print("ERROR: no --segment provided for openocd", file=sys.stderr)
        sys.exit(1)
    _addr, app_path = segments[-1]
    addr = _addr
    _check_file(app_path, "app binary")
    iface = getattr(args, "openocd_interface", "stlink")
    target = getattr(args, "openocd_target", "stm32f4x")
    cmd = [
        "openocd",
        "-f", f"interface/{iface}.cfg",
        "-f", f"target/{target}.cfg",
        "-c", f"program {app_path} {addr} verify reset exit",
    ]
    print("$ " + " ".join(cmd))
    subprocess.check_call(cmd)


def main() -> None:
    p = argparse.ArgumentParser(description="Universal firmware flash driver")
    p.add_argument("--port", default="/dev/ttyUSB0", help="Serial port")
    p.add_argument("--tool", required=True,
                   choices=["esptool", "avrdude", "picotool", "openocd"],
                   help="Flash tool")
    # esptool options
    p.add_argument("--chip", default="esp32")
    p.add_argument("--baud", type=int, default=115200)
    p.add_argument("--before", default="default_reset")
    p.add_argument("--after", default="hard_reset")
    p.add_argument("--write-flash-args", default="",
                   help="Extra args passed verbatim to esptool write_flash")
    p.add_argument("--esptool", help="Path to esptool wrapper binary")
    # avrdude options
    p.add_argument("--part", default="m328p", help="avrdude -p value")
    p.add_argument("--programmer", default="arduino", help="avrdude -c value")
    # openocd options
    p.add_argument("--openocd-interface", default="stlink")
    p.add_argument("--openocd-target", default="stm32f4x")
    # segments: addr=path pairs, in flash order
    p.add_argument("--segment", action="append", default=[],
                   metavar="ADDR=PATH",
                   help="Memory segment to flash: address=file (repeatable)")

    args = p.parse_args()

    # Parse segments
    segments: list[tuple[str, str]] = []
    for seg in args.segment:
        addr, sep, path = seg.partition("=")
        if not sep:
            print(f"ERROR: --segment must be ADDR=PATH, got: {seg}", file=sys.stderr)
            sys.exit(1)
        segments.append((addr, path))

    dispatch = {
        "esptool": flash_esptool,
        "avrdude": flash_avrdude,
        "picotool": flash_picotool,
        "openocd": flash_openocd,
    }
    dispatch[args.tool](args, segments)
    print("\nFlash complete.")


if __name__ == "__main__":
    main()
