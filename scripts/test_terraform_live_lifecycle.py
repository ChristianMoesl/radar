"""Offline scope, mutation and real-daemon safeguards for live Terraform CI."""

import copy
from datetime import datetime, timezone
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

SCRIPT = Path(__file__).with_name("terraform-live-lifecycle.py")
spec = importlib.util.spec_from_file_location("terraform_live_lifecycle", SCRIPT)
live = importlib.util.module_from_spec(spec)
spec.loader.exec_module(live)


def relationship(kind, value):
    return {"data": {"type": kind, "id": value}}


class FixtureAPI:
    def __init__(self, state_file):
        self.state_file, self.requests = state_file, []
        self.state = {
            "account": {"type": "users", "id": "user-Test", "attributes": {"is-service-account": False}},
            "workspace": {"type": "workspaces", "id": "ws-Test", "attributes": {
                "name": live.WORKSPACE, "execution-mode": "remote", "locked": False},
                "relationships": {"organization": relationship("organizations", live.ORGANIZATION)}},
            "plan": {"type": "plans", "id": "plan-Test", "attributes": {
                "resource-destructions": 0, "resource-additions": 1, "resource-changes": 0}}, "runs": {}}
        self.on_apply = lambda: None
        self.save()

    def save(self):
        temporary = self.state_file.with_suffix(".tmp")
        temporary.write_text(json.dumps(self.state))
        temporary.replace(self.state_file)

    def request(self, path, body=None):
        self.requests.append((path, copy.deepcopy(body)))
        if path == "/account/details":
            data = self.state["account"]
        elif path == f"/organizations/{live.ORGANIZATION}/workspaces/{live.WORKSPACE}":
            data = self.state["workspace"]
        elif path == "/workspaces/ws-Test/configuration-versions":
            data = {"type": "configuration-versions", "id": "cv-Test", "attributes": {"upload-url": "https://upload.example/secret-url"}}
        elif path == "/configuration-versions/cv-Test":
            data = {"type": "configuration-versions", "id": "cv-Test", "attributes": {"status": "uploaded"}}
        elif path == "/runs":
            run_id = "run-" + str(len(self.state["runs"]) + 1)
            first = not self.state["runs"]
            data = {"type": "runs", "id": run_id, "attributes": {
                "status": "planned" if first else "pending", "message": "Radar lifecycle fixture",
                "plan-only": False, "auto-apply": False, "created-at": datetime.now(timezone.utc).isoformat(),
                "actions": {"is-confirmable": first, "is-cancelable": not first, "is-discardable": first},
                "permissions": {"can-apply": True, "can-cancel": True, "can-discard": True}},
                "relationships": {"created-by": relationship("users", "user-Test"),
                                  "workspace": relationship("workspaces", "ws-Test"),
                                  "configuration-version": relationship("configuration-versions", "cv-Test"),
                                  "plan": relationship("plans", "plan-Test")}}
            self.state["runs"][run_id] = data
            self.save()
        elif path.startswith("/runs/"):
            parts = path.split("/")
            data = self.state["runs"][parts[2]]
            if len(parts) == 5:
                if parts[4] == "apply":
                    self.on_apply()
                data["attributes"]["status"] = {"apply": "applied", "cancel": "canceled", "discard": "discarded"}[parts[4]]
                data["attributes"]["actions"] = {}
                self.save()
                return {}
        elif path == "/plans/plan-Test":
            data = self.state["plan"]
        else:
            raise AssertionError("Unexpected fixture API request")
        return {"data": copy.deepcopy(data)}

    def upload(self, url, archive):
        self.uploaded = archive


class LifecycleSafeguards(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="radar-tf-test-", dir="/tmp")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.api = FixtureAPI(self.directory / "fixture.json")
        self.fixture = live.Fixture(self.api)
        self.configuration = Path(__file__).resolve().parent.parent / "testdata/terraform-live"

    def prepare_run(self):
        self.fixture.prepare(self.configuration)
        return self.fixture.create()

    def test_configuration_upload_contains_only_the_three_trusted_files(self):
        self.fixture.prepare(self.configuration)
        with tarfile.open(fileobj=io.BytesIO(self.api.uploaded), mode="r:gz") as archive:
            self.assertEqual(archive.getnames(), list(live.FILES))
            self.assertTrue(all(member.isfile() for member in archive.getmembers()))
        creation = next(body for path, body in self.api.requests if path.endswith("/configuration-versions"))
        self.assertEqual(creation["data"]["attributes"], {"auto-queue-runs": False, "speculative": False, "provisional": True})

    def test_invalid_configuration_is_rejected_before_remote_creation(self):
        self.api.requests.clear()
        with self.assertRaises(live.Failure):
            self.fixture.prepare(self.directory)
        self.assertFalse(any(body is not None for _, body in self.api.requests))
        for name in live.FILES:
            (self.directory / name).symlink_to(self.configuration / name)
        with self.assertRaises(live.Failure):
            self.fixture.prepare(self.directory)
        self.assertFalse(any(body is not None for _, body in self.api.requests))

    def test_locked_local_service_account_and_foreign_authentication_are_rejected(self):
        cases = [("workspace", "locked", True), ("workspace", "execution-mode", "local"),
                 ("workspace", "name", "production"), ("account", "is-service-account", True)]
        for resource, key, value in cases:
            with self.subTest(resource=resource, key=key), patch.dict(self.api.state[resource]["attributes"], {key: value}):
                with self.assertRaises(live.Failure):
                    self.fixture.prepare(self.configuration)
        self.api.state["account"]["relationships"] = {"authenticated-resource": relationship("teams", "team-Other")}
        with self.assertRaises(live.Failure):
            self.fixture.prepare(self.configuration)
        self.assertFalse(any(body is not None for _, body in self.api.requests))

    def test_preexisting_unfinished_run_is_left_untouched(self):
        run_id = self.prepare_run()
        self.api.state["workspace"]["relationships"]["current-run"] = relationship("runs", run_id)
        self.api.requests.clear()
        with self.assertRaises(live.Failure):
            live.Fixture(self.api).prepare(self.configuration)
        self.assertFalse(any(body is not None for _, body in self.api.requests))

    def test_every_action_requires_a_created_run_and_matching_scope(self):
        run_id = self.prepare_run()
        relationships = self.api.state["runs"][run_id]["relationships"]
        for key, kind, value in (("workspace", "workspaces", "ws-Other"), ("created-by", "users", "user-Other"),
                                 ("configuration-version", "configuration-versions", "cv-Other")):
            with self.subTest(key=key), patch.dict(relationships, {key: relationship(kind, value)}):
                with self.assertRaises(live.Failure):
                    self.fixture.action(run_id, "apply")
        with self.assertRaises(live.Failure):
            self.fixture.action("run-Unknown", "cancel")
        for action in ("force-cancel", "force-execute", "destroy", "unlock"):
            with self.subTest(action=action), self.assertRaises(live.Failure):
                self.fixture.action(run_id, action)
        attrs = self.api.state["runs"][run_id]["attributes"]
        for changes in ({"auto-apply": True}, {"plan-only": True}, {"permissions": {"can-apply": False}},
                        {"actions": {"is-confirmable": False}}):
            with self.subTest(changes=changes), patch.dict(attrs, changes), self.assertRaises(live.Failure):
                self.fixture.action(run_id, "apply")
        self.assertFalse(any("/actions/" in path for path, _ in self.api.requests))

    def test_destructive_missing_or_malformed_plan_counts_block_apply(self):
        run_id = self.prepare_run()
        for count in (1, None, "0", False, -1):
            with self.subTest(count=count), patch.dict(self.api.state["plan"]["attributes"], {"resource-destructions": count}):
                with self.assertRaises(live.Failure):
                    self.fixture.action(run_id, "apply")
        self.assertFalse(any("/actions/" in path for path, _ in self.api.requests))

    def test_run_defaults_disable_auto_apply_destroy_and_refresh(self):
        self.prepare_run()
        body = next(body for path, body in self.api.requests if path == "/runs")
        attrs = body["data"]["attributes"]
        self.assertTrue(all(attrs[key] is False for key in ("auto-apply", "is-destroy", "refresh", "plan-only", "save-plan")))
        self.assertEqual(attrs["variables"][0], {"key": "fail_plan", "value": "false"})
        self.assertTrue(json.loads(attrs["variables"][1]["value"]).startswith("ci-"))

    def test_cleanup_releases_only_created_runs_without_apply_or_force(self):
        first = self.prepare_run()
        second = self.fixture.create()
        self.api.requests.clear()
        self.assertTrue(self.fixture.cleanup())
        actions = [path for path, _ in self.api.requests if "/actions/" in path]
        self.assertEqual(actions, [f"/runs/{second}/actions/cancel", f"/runs/{first}/actions/discard"])

    def test_upload_never_receives_the_api_bearer_token(self):
        api = live.API("synthetic-token")
        with patch.object(api.opener, "open") as open_request:
            api.upload("https://upload.example/secret-signed-url", b"public configuration")
        request = open_request.call_args.args[0]
        self.assertFalse(request.has_header("Authorization"))
        self.assertEqual(request.get_method(), "PUT")
        for url in ("http://upload.example/file", "https://user:pass@upload.example/file", "file:///tmp/config"):
            with self.subTest(url=url), self.assertRaises(live.Failure):
                api.upload(url, b"configuration")

    def test_network_errors_are_private_and_posts_are_never_blindly_retried(self):
        api = live.API("synthetic-token")
        errors = [urllib.error.URLError("private provider response synthetic-token"),
                  urllib.error.HTTPError("https://private.example/synthetic-token", 403, "private response", {}, None)]
        for error in errors:
            with self.subTest(error_type=type(error).__name__), patch.object(api.opener, "open", side_effect=error) as request:
                with self.assertRaises(live.Failure) as caught:
                    api.request("/runs", {"data": {}})
            self.assertEqual(request.call_count, 1)
            self.assertNotIn("private", str(caught.exception))
            self.assertNotIn("synthetic-token", str(caught.exception))
            if isinstance(error, urllib.error.HTTPError):
                self.assertIn("HTTP 403", str(caught.exception))
        self.assertIsNone(live.NoRedirect().redirect_request(None, None, 307, "", {}, "https://other.example"))

    def test_missing_token_and_unbounded_waits_are_rejected(self):
        with self.assertRaises(live.Failure):
            live.API("")
        with patch.object(live.time, "monotonic", side_effect=[0, 10]), patch.object(live.time, "sleep") as sleep:
            with self.assertRaises(live.Failure):
                live.wait_for(lambda: None, lambda value: False, "fixture", timeout=5)
        sleep.assert_not_called()

    def test_fault_proxy_blocks_mutations_and_injects_only_the_selected_reads(self):
        fault = self.directory / "fault"
        env = {"RADAR_LIVE_TFCTL_FAULT": str(fault), "RADAR_LIVE_TFCTL_REAL": "/verified/tfctl"}
        with patch.dict(os.environ, env), patch.object(live.os, "execve") as execute:
            fault.write_text("listing")
            self.assertEqual(live.tfctl_proxy(["api", "/workspaces/ws-Test/runs?page[number]=1", "--method=GET"]), 1)
            self.assertEqual(live.tfctl_proxy(["api", "/runs/run-1", "--method=POST"]), 1)
            execute.assert_not_called()
            live.tfctl_proxy(["api", "/runs/run-1", "--method=GET"])
            self.assertEqual(execute.call_count, 1)
            fault.write_text("service")
            self.assertEqual(live.tfctl_proxy(["api", "/account/details", "--method=GET"]), 1)
            self.assertEqual(execute.call_count, 1)

    @unittest.skipUnless(os.environ.get("RADAR_TERRAFORM_TEST_BINARY"), "set RADAR_TERRAFORM_TEST_BINARY to exercise the real daemon offline")
    def test_complete_lifecycle_through_real_daemon_and_persisted_database(self):
        self.fixture.prepare(self.configuration)
        fake_bin = self.directory / "fake-bin"
        fake_bin.mkdir()
        cli = fake_bin / "tfctl"
        cli.write_text(f"#!/bin/sh\nexec {shlex.quote(sys.executable)} {shlex.quote(str(Path(__file__).resolve()))} fake-cli \"$@\"\n")
        cli.chmod(0o700)
        env = {**os.environ, "TFCTL_TOKEN": "synthetic-token", "TERRAFORM_FIXTURE_STATE": str(self.api.state_file),
               "PATH": str(fake_bin) + os.pathsep + os.environ["PATH"]}
        directory = self.directory / "daemon"
        directory.mkdir()
        daemon = live.Daemon(os.environ["RADAR_TERRAFORM_TEST_BINARY"], directory, self.fixture, env)
        self.api.on_apply = lambda: self.assertIsNone(daemon.process, "run must finish while the daemon is stopped")
        try:
            live.qualify(self.fixture, daemon)
        finally:
            daemon.stop()
            self.assertTrue(self.fixture.cleanup())
        self.assertEqual([run["attributes"]["status"] for run in self.api.state["runs"].values()], ["applied", "canceled"])
        self.assertTrue((directory / "state.db").is_file())


def fake_cli(arguments):
    """Read-only tfctl transport used only by the offline real-daemon test."""
    if arguments[0] == "--version":
        print("v0.4.0")
        return
    if arguments[:2] == ["auth", "status"]:
        print(json.dumps({"active": True, "hostname": live.HOST}))
        return
    if arguments[0] != "api" or "--method=GET" not in arguments:
        raise SystemExit(1)
    state = json.loads(Path(os.environ["TERRAFORM_FIXTURE_STATE"]).read_text())
    path = arguments[1].split("?", 1)[0]
    if path == "/account/details":
        data = state["account"]
    elif path == f"/organizations/{live.ORGANIZATION}/workspaces/{live.WORKSPACE}":
        data = state["workspace"]
    elif path == "/workspaces/ws-Test/runs":
        data = list(state["runs"].values())
    elif path.startswith("/runs/"):
        data = state["runs"][path.split("/")[2]]
    elif path == "/plans/plan-Test":
        data = state["plan"]
    else:
        raise SystemExit(1)
    print(json.dumps({"data": data, "links": {"next": None}}))


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "fake-cli":
        fake_cli(sys.argv[2:])
    else:
        unittest.main()
