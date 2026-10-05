#!/usr/bin/env python3
"""Image-owned startup hooks and readiness; no host identity is built into this file."""

import argparse
import contextlib
import fcntl
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import time


class StartupError(Exception):
    pass


def boot_token():
    """A persisted success must not survive a VM or container restart."""
    boot_id = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
    process = Path("/proc/1/stat").read_text()
    # comm (field 2) can contain spaces and parentheses. Fields after its last
    # closing parenthesis begin at field 3; starttime is field 22.
    fields = process[process.rfind(")") + 2:].split()
    if not boot_id or len(fields) <= 19 or not fields[19].isdigit():
        raise StartupError("cannot identify the current sandbox boot")
    return f"{boot_id}:{fields[19]}"


def state_directory():
    directory = Path.home() / ".cache" / "sandbox-startup"
    if directory.is_symlink():
        raise StartupError("startup state directory must not be a symlink")
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    directory.chmod(0o700)
    return directory


def read_state(directory):
    try:
        value = json.loads((directory / "status.json").read_text())
        return value if isinstance(value, dict) else {}
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def write_state(directory, value):
    descriptor, temporary = tempfile.mkstemp(prefix=".status-", dir=directory)
    try:
        with os.fdopen(descriptor, "w") as file:
            json.dump(value, file)
            file.write("\n")
        os.replace(temporary, directory / "status.json")
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(temporary)


def protected_file(path, flags):
    descriptor = os.open(path, flags | os.O_NOFOLLOW, 0o600)
    os.fchmod(descriptor, 0o600)
    return descriptor


def require_agent():
    if os.geteuid() == 0:
        raise StartupError("startup hooks must run as a non-root user")


def run(directory_name):
    require_agent()
    if not directory_name:
        return 0
    token = boot_token()
    state = state_directory()
    with os.fdopen(protected_file(state / "lock", os.O_CREAT | os.O_RDWR), "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        previous = read_state(state)
        if previous.get("boot") == token and previous.get("status") == "ready":
            return 0
        current = {"boot": token, "status": "running"}
        write_state(state, current)
        script_name = None
        try:
            directory = Path(directory_name)
            if not directory.is_absolute() or not directory.is_dir():
                raise StartupError("SBX_STARTUP_DIR must name an existing absolute directory")
            # Byte order is the equivalent of LC_ALL=C, independent of locale.
            scripts = sorted(directory.iterdir(), key=lambda path: os.fsencode(path.name))
            log_descriptor = protected_file(state / "output.log", os.O_CREAT | os.O_WRONLY | os.O_TRUNC)
            with os.fdopen(log_descriptor, "w") as output:
                for script in scripts:
                    if not stat.S_ISREG(script.lstat().st_mode) or not os.access(script, os.X_OK):
                        continue
                    script_name = script.name
                    print(f"Running {script_name!r}", file=output, flush=True)
                    result = subprocess.run([str(script)], cwd=directory, stdin=subprocess.DEVNULL,
                                            stdout=output, stderr=subprocess.STDOUT, check=False)
                    if result.returncode:
                        code = result.returncode if result.returncode > 0 else 128 - result.returncode
                        current.update(status="failed", script=script_name, exit_code=code)
                        write_state(state, current)
                        print(f"startup hook {script_name!r} failed (exit {code})", file=sys.stderr)
                        return min(code, 255)
            write_state(state, {"boot": token, "status": "ready"})
            return 0
        except (OSError, StartupError):
            # Never propagate hook output, directory contents or environment values.
            current.update(status="failed", exit_code=1)
            if script_name is not None:
                current["script"] = script_name
            write_state(state, current)
            raise StartupError("startup initialization failed; inspect the configured directory and private output.log") from None


def wait(directory_name, timeout=60):
    require_agent()
    if not directory_name:
        return 0
    token = boot_token()
    state = state_directory()
    deadline = time.monotonic() + timeout
    while True:
        current = read_state(state)
        if current.get("boot") == token:
            if current.get("status") == "ready":
                return 0
            if current.get("status") == "failed":
                name = current.get("script")
                code = current.get("exit_code", 1)
                detail = f"hook {name!r}" if name is not None else "initialization"
                print(f"sandbox startup {detail} failed (exit {code})", file=sys.stderr)
                return 1
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            print("timed out waiting for sandbox startup", file=sys.stderr)
            return 1
        time.sleep(min(0.1, remaining))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("run", help="execute configured hooks for the current boot")
    waiter = commands.add_parser("wait", help="wait for current-boot initialization")
    waiter.add_argument("--timeout", type=float, default=60, help="maximum wait in seconds (default: 60)")
    arguments = parser.parse_args()
    directory = os.environ.get("SBX_STARTUP_DIR", "")
    try:
        if arguments.command == "run":
            return run(directory)
        if not 0 < arguments.timeout < float("inf"):
            parser.error("timeout must be a positive finite number")
        return wait(directory, arguments.timeout)
    except (OSError, StartupError):
        print("sandbox startup could not initialize; inspect its directory and private state", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
