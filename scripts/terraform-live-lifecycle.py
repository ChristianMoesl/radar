#!/usr/bin/env python3
"""Qualify disposable Terraform run transitions through a real Radar daemon."""

import argparse
import io
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

HOST = "app.terraform.io"
ORGANIZATION = "radar-oss"
WORKSPACE = "radar-oauth-e2e"
FILES = ("main.tf", "variables.tf", "outputs.tf")
TERMINAL = {"applied", "planned_and_finished", "errored", "canceled", "force_canceled", "discarded", "policy_soft_failed"}


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


def identifier(value, prefix):
    require(isinstance(value, str) and re.fullmatch(prefix + r"[A-Za-z0-9]+", value), "Terraform returned an invalid fixture identity.")
    return value


def related(resource, name):
    return resource.get("relationships", {}).get(name, {}).get("data", {}) or {}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class API:
    def __init__(self, token):
        require(token, "Configure the personal TFCTL_TOKEN secret in the selected live validation environment.")
        self.token = token
        self.opener = urllib.request.build_opener(NoRedirect)

    def request(self, path, body=None):
        require(path.startswith("/") and not path.startswith("//"), "Invalid fixture API path.")
        request = urllib.request.Request("https://" + HOST + "/api/v2" + path,
                                         data=json.dumps(body).encode() if body is not None else None,
                                         headers={"Authorization": "Bearer " + self.token,
                                                  "Content-Type": "application/vnd.api+json"},
                                         method="POST" if body is not None else "GET")
        try:
            with self.opener.open(request, timeout=30) as response:
                data = response.read(2 * 1024 * 1024 + 1)
            require(len(data) <= 2 * 1024 * 1024, "Terraform fixture metadata exceeded its size limit.")
            return json.loads(data) if data else {}
        except urllib.error.HTTPError as error:
            raise Failure(f"Terraform fixture {request.get_method()} request failed (HTTP {error.code}); check workspace permissions and run history before retrying.") from None
        except (urllib.error.URLError, OSError, ValueError):
            # Neither provider responses nor signed URLs/credentials enter logs.
            raise Failure("Terraform fixture request failed; check workspace permissions and run history before retrying.") from None

    def upload(self, url, archive):
        parsed = urllib.parse.urlsplit(url)
        require(parsed.scheme == "https" and parsed.hostname and not parsed.username and not parsed.password
                and not parsed.fragment, "Terraform returned an invalid configuration upload URL.")
        request = urllib.request.Request(url, data=archive, method="PUT",
                                         headers={"Content-Type": "application/octet-stream"})
        try:
            # The signed upload URL authenticates itself. Never forward the API
            # Authorization header, follow redirects, or log this URL.
            with self.opener.open(request, timeout=60) as response:
                response.read(1024)
        except (urllib.error.URLError, OSError):
            raise Failure("Terraform fixture configuration upload failed.") from None


def configuration_archive(configuration):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        for name in FILES:
            path = configuration / name
            require(not path.is_symlink() and path.is_file() and path.stat().st_size <= 65536,
                    "The trusted Terraform fixture is missing or invalid.")
            data = path.read_bytes()
            entry = tarfile.TarInfo(name)
            entry.size, entry.mode = len(data), 0o600
            archive.addfile(entry, io.BytesIO(data))
    return buffer.getvalue()


def wait_for(read, accepts, description, timeout=360):
    deadline = time.monotonic() + timeout
    while True:
        value = read()
        if accepts(value):
            return value
        require(time.monotonic() < deadline, "Timed out waiting for " + description + "; inspect the disposable workspace before retrying.")
        time.sleep(2)


class Fixture:
    def __init__(self, api):
        self.api, self.created = api, []

    def prepare(self, configuration):
        account = self.api.request("/account/details")["data"]
        require(account["type"] == "users" and account["attributes"].get("is-service-account") is False,
                "Live fixtures require a personal Terraform user.")
        self.user = identifier(account["id"], "user-")
        authenticated = related(account, "authenticated-resource")
        require(not authenticated or authenticated == {"type": "users", "id": self.user},
                "Live fixtures require personal-user authentication.")
        workspace = self.api.request(f"/organizations/{ORGANIZATION}/workspaces/{WORKSPACE}")["data"]
        require(workspace["type"] == "workspaces" and workspace["attributes"]["name"] == WORKSPACE
                and related(workspace, "organization").get("id") == ORGANIZATION
                and workspace["attributes"].get("execution-mode") == "remote"
                and workspace["attributes"].get("locked") is False,
                "The approved disposable workspace must be remote and unlocked.")
        self.workspace = identifier(workspace["id"], "ws-")
        current = related(workspace, "current-run").get("id")
        if current:
            run = self.api.request("/runs/" + identifier(current, "run-"))["data"]
            require(run["attributes"].get("status") in TERMINAL,
                    "The disposable workspace already has an unfinished run; leave it untouched and finish it before retrying.")
        archive = configuration_archive(configuration)
        version = self.api.request(f"/workspaces/{self.workspace}/configuration-versions", {"data": {
            "type": "configuration-versions", "attributes": {
                "auto-queue-runs": False, "speculative": False, "provisional": True}}})["data"]
        require(version.get("type") == "configuration-versions", "Terraform returned an invalid fixture configuration version.")
        self.version = identifier(version["id"], "cv-")
        self.api.upload(version["attributes"]["upload-url"], archive)
        wait_for(lambda: self.api.request("/configuration-versions/" + self.version)["data"],
                 lambda version: version["attributes"]["status"] == "uploaded", "fixture configuration upload", timeout=90)

    def create(self):
        run = self.api.request("/runs", {"data": {"type": "runs", "attributes": {
            "message": "Radar live lifecycle fixture", "plan-only": False, "is-destroy": False,
            "auto-apply": False, "save-plan": False, "refresh": False,
            "variables": [{"key": "fail_plan", "value": "false"},
                          {"key": "fixture_revision", "value": json.dumps("ci-" + uuid.uuid4().hex)}]},
            "relationships": {"workspace": {"data": {"type": "workspaces", "id": self.workspace}},
                              "configuration-version": {"data": {"type": "configuration-versions", "id": self.version}}}}})["data"]
        run_id = identifier(run["id"], "run-")
        self.created.append(run_id)
        self.run(run_id)
        return run_id

    def run(self, run_id):
        require(run_id in self.created, "Only runs created by this fixture may be changed.")
        run = self.api.request("/runs/" + run_id)["data"]
        require(run.get("id") == run_id and run.get("type") == "runs"
                and related(run, "workspace") == {"type": "workspaces", "id": self.workspace}
                and related(run, "created-by") == {"type": "users", "id": self.user}
                and related(run, "configuration-version") == {"type": "configuration-versions", "id": self.version}
                and run["attributes"].get("auto-apply") is False and run["attributes"].get("plan-only") is False,
                "Fixture run escaped its approved account, configuration or workspace scope.")
        return run

    def wait(self, run_id, accepts, description):
        def read():
            run = self.run(run_id)
            require(run["attributes"]["status"] not in TERMINAL or accepts(run), "A lifecycle fixture run reached an unexpected terminal state.")
            return run
        return wait_for(read, accepts, description)

    def action(self, run_id, action):
        require(action in {"apply", "cancel", "discard"}, "Unsupported fixture action.")
        run = self.run(run_id)
        attrs = run["attributes"]
        flag = {"apply": "confirmable", "cancel": "cancelable", "discard": "discardable"}[action]
        require(attrs.get("actions", {}).get("is-" + flag) is True
                and attrs.get("permissions", {}).get("can-" + action) is True,
                "The personal token cannot perform the required fixture action in the current run state.")
        if action == "apply":
            plan_id = identifier(related(run, "plan").get("id"), "plan-")
            plan = self.api.request("/plans/" + plan_id)["data"]
            require(plan.get("type") == "plans" and plan.get("id") == plan_id
                    and type(plan["attributes"].get("resource-destructions")) is int
                    and plan["attributes"]["resource-destructions"] == 0,
                    "The fixture plan must report zero resource destructions before apply.")
        self.api.request(f"/runs/{run_id}/actions/{action}", {})

    def cleanup(self):
        complete = True
        for run_id in reversed(self.created):
            try:
                attrs = self.run(run_id)["attributes"]
                if attrs["status"] in TERMINAL:
                    continue
                actions = attrs.get("actions", {})
                if actions.get("is-cancelable"):
                    self.action(run_id, "cancel")
                elif actions.get("is-discardable"):
                    self.action(run_id, "discard")
                else:
                    complete = False
                    continue
                wait_for(lambda: self.run(run_id), lambda run: run["attributes"]["status"] in TERMINAL,
                         "fixture cleanup", timeout=60)
            except Failure:
                complete = False
        return complete


class Daemon:
    def __init__(self, binary, directory, fixture, inherited):
        self.binary, self.directory, self.fixture = binary, directory, fixture
        self.process = None
        self.env = {key: value for key, value in inherited.items()
                    if not key.startswith(("RADAR_", "TFCTL_", "TF_LOG", "TF_CLI_ARGS", "XDG_"))}
        real_cli = shutil.which("tfctl", path=self.env.get("PATH"))
        require(real_cli, "Install the checksum-pinned tfctl before live lifecycle validation.")
        proxies = directory / "bin"
        proxies.mkdir()
        wrapper = proxies / "tfctl"
        wrapper.write_text(f"#!/bin/sh\nexec {shlex.quote(sys.executable)} {shlex.quote(str(Path(__file__).resolve()))} tfctl-proxy \"$@\"\n")
        wrapper.chmod(0o700)
        self.fault = directory / "fault"
        self.fault.write_text("")
        self.env.update(PATH=str(proxies) + os.pathsep + self.env["PATH"],
                        TFCTL_CONFIG_DIR=str(directory / "tfctl"), TFCTL_TOKEN=inherited["TFCTL_TOKEN"],
                        XDG_CONFIG_HOME=str(directory / "config"), XDG_DATA_HOME=str(directory / "data"),
                        XDG_STATE_HOME=str(directory / "state"), RADAR_STATE=str(directory / "state.db"),
                        RADAR_SOCKET=str(directory / "r.sock"), RADAR_PID=str(directory / "r.pid"),
                        RADAR_LOG=str(directory / "daemon.log"), RADAR_AGENT_ROOT=str(directory / "agents"),
                        RADAR_LIVE_TFCTL_REAL=real_cli, RADAR_LIVE_TFCTL_FAULT=str(self.fault), TMUX="")
        profile = directory / "tfctl/profiles/default.hcl"
        profile.parent.mkdir(parents=True)
        profile.write_text(f'name = "default"\nhostname = "{HOST}"\ndefault_organization = "{ORGANIZATION}"\ntelemetry = "disabled"\n')
        (profile.parent.parent / "active_profile.hcl").write_text('name = "default"\n')
        config = directory / "config/radar/config.yaml"
        config.parent.mkdir(parents=True)
        config.write_text(json.dumps({
            "repository_dirs": [str(directory / "repositories")], "linking_mark_prefixes": [],
            "workspace": {"root_dir": str(directory / "workspaces")},
            "obsidian": {"vault_path": str(directory / "vault")},
            **{source: {"enabled": False} for source in ("github", "jira", "datadog", "sbx")},
            "terraform": {"enabled": True, "profile": "default", "organization": ORGANIZATION, "workspaces": [WORKSPACE]}}))

    def rpc(self, method, timeout=90):
        with socket.socket(socket.AF_UNIX) as connection:
            connection.settimeout(timeout)
            connection.connect(self.env["RADAR_SOCKET"])
            connection.sendall(json.dumps({"method": method}).encode() + b"\n")
            with connection.makefile("rb") as reader:
                line = reader.readline(8 * 1024 * 1024 + 1)
        require(len(line) <= 8 * 1024 * 1024, "Isolated daemon response exceeded its size limit.")
        response = json.loads(line)
        require(response.get("ok") is True, "Isolated Radar daemon request failed.")
        return response

    def start(self):
        require(self.process is None, "The isolated daemon is already running.")
        self.process = subprocess.Popen([self.binary, "daemon"], env=self.env, cwd=self.directory,
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            require(self.process.poll() is None, "Isolated Radar daemon exited during startup.")
            try:
                if self.rpc("version", timeout=.2):
                    return
            except (OSError, ValueError):
                pass
            time.sleep(.1)
        raise Failure("Isolated Radar daemon did not become ready.")

    def stop(self):
        if self.process is not None:
            self.process.terminate()
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
            self.process = None

    def set_fault(self, mode):
        temporary = self.fault.with_suffix(".tmp")
        temporary.write_text(mode)
        temporary.replace(self.fault)

    def snapshot(self, source_status="ok", expected=None):
        result = self.rpc("refresh")
        source = next(item for item in result["sources"] if item["name"] == "terraform")
        require(source["status"] == source_status, "Terraform source did not report the expected healthy, partial or failed state.")
        facts = {}
        for task in result["tasks"]:
            for ref in task.get("source_refs", []):
                if ref.get("source") != "terraform":
                    continue
                metadata = ref.get("metadata", {})
                require(metadata.get("creator_id") == self.fixture.user and metadata.get("workspace_id") == self.fixture.workspace
                        and metadata.get("organization") == ORGANIZATION and metadata.get("workspace_name") == WORKSPACE
                        and ref["id"] == f'terraform:run:{HOST}:{metadata.get("run_id")}' and ref["id"] not in facts,
                        "Radar task escaped approved personal ownership, identity or workspace scope.")
                facts[ref["id"]] = (task["id"], task["attention"], metadata["run_status"], ref["signal"])
        for run_id, (signal, state) in (expected or {}).items():
            fact = facts.get(f"terraform:run:{HOST}:{run_id}")
            require(fact is not None and fact[1] == signal and fact[3] == signal and (state is None or fact[2] == state),
                    "Radar task did not reflect the expected live run state and signal.")
        return facts


def tfctl_proxy(arguments):
    mode = Path(os.environ["RADAR_LIVE_TFCTL_FAULT"]).read_text()
    if arguments and arguments[0] == "api":
        if "--method=GET" not in arguments or mode == "service":
            return 1
        if mode == "listing" and len(arguments) > 1 and re.match(r"/workspaces/ws-[A-Za-z0-9]+/runs\?", arguments[1]):
            return 1
    real = os.environ["RADAR_LIVE_TFCTL_REAL"]
    os.execve(real, [real, *arguments], os.environ)


def qualify(fixture, daemon):
    apply_run = fixture.create()
    fixture.wait(apply_run, lambda run: run["attributes"].get("actions", {}).get("is-confirmable") is True, "manual apply confirmation")
    daemon.start()
    daemon.snapshot(expected={apply_run: ("attention", None)})
    print("Terraform approval wait: production daemon requires Attention.", flush=True)

    cancel_run = fixture.create()
    daemon.snapshot(expected={apply_run: ("attention", None), cancel_run: ("in_progress", None)})
    fixture.wait(cancel_run, lambda run: run["attributes"].get("actions", {}).get("is-cancelable") is True, "cancelable queued fixture run")
    fixture.action(cancel_run, "cancel")
    fixture.wait(cancel_run, lambda run: run["attributes"]["status"] == "canceled", "fixture cancellation")
    expected = {apply_run: ("attention", None), cancel_run: ("done", "canceled")}
    baseline = daemon.snapshot(expected=expected)
    print("Terraform cancellation: production daemon confirms Done and retains canceled outcome.", flush=True)

    daemon.set_fault("listing")
    require(daemon.snapshot("partial", expected) == baseline, "Partial discovery changed existing task identities or facts.")
    daemon.set_fault("service")
    require(daemon.snapshot("error", expected) == baseline, "Service read failure changed existing task identities or facts.")
    daemon.stop()
    daemon.start()
    require(daemon.snapshot("error", expected) == baseline, "Restart during a read outage lost persisted task identities or facts.")
    daemon.set_fault("")
    require(daemon.snapshot(expected=expected) == baseline, "Read recovery changed task identities or retained facts.")
    print("Terraform recovery: injected listing/service failures retain live facts across restart and recover to healthy.", flush=True)

    daemon.stop()
    fixture.action(apply_run, "apply")
    fixture.wait(apply_run, lambda run: run["attributes"]["status"] == "applied", "fixture apply while the daemon is stopped")
    daemon.start()
    after = daemon.snapshot(expected={apply_run: ("done", "applied"), cancel_run: ("done", "canceled")})
    require({key: fact[0] for key, fact in after.items()} == {key: fact[0] for key, fact in baseline.items()},
            "Daemon catch-up changed persisted task identities.")
    print("Terraform apply/restart: production daemon catches up to applied Done with the original task ID.", flush=True)


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "tfctl-proxy":
        return tfctl_proxy(sys.argv[2:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--configuration", type=Path, required=True)
    parser.add_argument("--radar", type=Path, required=True)
    args = parser.parse_args()
    fixture, daemon = None, None
    try:
        fixture = Fixture(API(os.environ.get("TFCTL_TOKEN")))
        with tempfile.TemporaryDirectory(prefix="radar-tf-", dir="/tmp") as temporary:
            os.chmod(temporary, 0o700)
            try:
                fixture.prepare(args.configuration)
                daemon = Daemon(str(args.radar.resolve()), Path(temporary), fixture, os.environ)
                qualify(fixture, daemon)
            finally:
                if daemon is not None:
                    daemon.stop()
                require(fixture.cleanup(), "Fixture cleanup incomplete; inspect the disposable workspace runs before retrying.")
        print("Terraform live lifecycle qualification passed: approval, cancellation, injected read recovery and restart/apply catch-up.")
        return 0
    except (Failure, OSError, ValueError, KeyError, TypeError, StopIteration):
        # Unexpected provider JSON can contain secrets; publish only our static
        # Failure messages, never exception values from parsers or transports.
        error = sys.exc_info()[1]
        print(str(error) if isinstance(error, Failure) else "Terraform live lifecycle qualification failed; check the isolated fixture and its run history.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
