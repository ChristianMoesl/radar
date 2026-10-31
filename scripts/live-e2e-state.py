#!/usr/bin/env python3
"""Transport opaque CLI/MCP client stores through an encrypted environment secret."""

import argparse
import base64
import gzip
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import zlib
from urllib.parse import urlsplit

SECRET = "LIVE_E2E_SESSIONS"
LIMIT = 512 * 1024
VERSION = 2


def require(condition, message):
    if not condition:
        raise ValueError(message)


def pack(config):
    stores = {}
    for name, key in (("pup", "datadog"), ("twg", "jira")):
        root = Path(config[key]["config_dir"])
        require(root.is_absolute() and root.is_dir(), "CLI store is missing")
        files = {}
        for path in sorted(root.rglob("*")):
            require(not path.is_symlink(), "CLI stores must not contain symlinks")
            if path.is_dir() or path.name.endswith((".lock", ".tmp")):
                continue
            require(path.is_file() and path.stat().st_size <= LIMIT, "unexpected CLI store entry")
            files[path.relative_to(root).as_posix()] = base64.b64encode(path.read_bytes()).decode("ascii")
        require(0 < len(files) <= 100, "CLI store is empty or too large")
        if name == "twg":
            require("auth-profiles.json" in files, "TWG requires portable file storage, not an OS keychain")
        else:
            require("sessions.json" in files and any(k.startswith("tokens_") for k in files), "Pup file session is missing")
        stores[name] = files
    mcp = config["mcp"]
    public_config = Path(mcp["config_file"])
    require(public_config.is_absolute() and public_config.is_file() and not public_config.is_symlink()
            and public_config.stat().st_size <= LIMIT, "MCP config is missing or invalid")
    public = public_config.read_bytes()
    servers = json.loads(public).get("mcpServers")
    require(isinstance(servers, dict) and servers, "MCP server profiles are missing")
    files = {"config.json": base64.b64encode(public).decode("ascii")}
    state = Path(mcp["state_dir"])
    require(state.is_absolute(), "MCP state_dir must be absolute")
    for name, server in servers.items():
        require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", name), "invalid MCP profile name")
        require(isinstance(server, dict) and set(server) <= {"url", "oauth"},
                "live MCP profiles require remote OAuth servers, without commands or credential headers")
        url = urlsplit(server.get("url", ""))
        require(url.scheme == "https" and url.hostname and not url.username and not url.password and not url.fragment and not url.query,
                "live MCP servers require HTTPS without embedded credentials")
        oauth = server.get("oauth", {})
        require(isinstance(oauth, dict) and set(oauth) <= {"clientId", "scope", "callbackPort"}, "invalid MCP public configuration")
        path = state / name / "oauth.json"
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= LIMIT, "MCP OAuth state is missing")
        # Transfer opaque client-owned bytes, never parse tokens or include the
        # transient SQLite profile lock in disposable-runner state.
        files[f"state/{name}/oauth.json"] = base64.b64encode(path.read_bytes()).decode("ascii")
    stores["mcp"] = files
    raw = json.dumps({"version": VERSION, "providers": stores}, separators=(",", ":")).encode()
    require(len(raw) <= LIMIT, "CLI stores exceed the size limit")
    bundle = base64.b64encode(gzip.compress(raw, mtime=0)).decode("ascii")
    require(len(bundle) < 48 * 1024, "session bundle exceeds GitHub's secret size limit")
    return bundle


def unpack(bundle, destination):
    require(len(bundle) < 48 * 1024, "session bundle exceeds the size limit")
    compressed = base64.b64decode(bundle, validate=True)
    decoder = zlib.decompressobj(16 + zlib.MAX_WBITS)
    raw = decoder.decompress(compressed, LIMIT + 1)
    require(len(raw) <= LIMIT and decoder.eof and not decoder.unused_data, "invalid or oversized session bundle")
    data = json.loads(raw)
    require(data.get("version") == VERSION and set(data["providers"]) == {"pup", "twg", "mcp"}, "unsupported session bundle; re-seed all client stores")
    entries = []
    for name, files in data["providers"].items():
        require(isinstance(files, dict) and 0 < len(files) <= 100, "invalid CLI store entries")
        for filename, content in files.items():
            path = PurePosixPath(filename)
            require(not path.is_absolute() and path.parts and ".." not in path.parts
                    and "\\" not in filename and str(path) == filename, "invalid session bundle path")
            entries.append((name, path, base64.b64decode(content, validate=True)))
    require(not destination.exists(), "restore requires a new directory")
    destination.mkdir(mode=0o700, parents=True)
    for name, path, content in entries:
        target = destination / name / path
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with target.open("xb") as output:
            os.chmod(target, 0o600)
            output.write(content)


def save(config, repository):
    bundle = pack(config)
    # gh encrypts with the environment's public key before uploading. Use stdin
    # so neither credentials nor the bundle appear in process arguments/logs.
    result = subprocess.run(["gh", "secret", "set", SECRET, "--repo", repository, "--env", "live-e2e"],
                            input=bundle, capture_output=True, text=True, timeout=60)
    require(result.returncode == 0, "could not save refreshed CLI sessions; check the state writer's environment permissions")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("seed", "restore", "save"))
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY"))
    parser.add_argument("--session-dir", type=Path)
    args = parser.parse_args()
    try:
        config = json.loads(args.config.read_text())
        if args.operation == "restore":
            require(args.session_dir and args.session_dir.is_absolute(), "select an absolute session directory")
            bundle = os.environ.pop(SECRET, "")
            require(bundle, "LIVE_E2E_SESSIONS is missing; seed the live-e2e environment first")
            unpack(bundle, args.session_dir)
            for name, key in (("pup", "datadog"), ("twg", "jira")):
                config[key]["config_dir"] = str(args.session_dir / name)
            config["mcp"]["config_file"] = str(args.session_dir / "mcp/config.json")
            config["mcp"]["state_dir"] = str(args.session_dir / "mcp/state")
            args.config.write_text(json.dumps(config, indent=2))
        else:
            require(args.repository, "select the GitHub repository")
            save(config, args.repository)
        print("CLI session restore complete" if args.operation == "restore" else "CLI sessions saved as an encrypted environment secret")
        return 0
    except Exception:
        # Parsing errors can contain input fragments. Never emit the exception.
        print("CLI session storage failed. Check the configuration, portable file stores and GitHub environment write permission. Re-seed after an interrupted refresh.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
