"""Offline checks for mutation boundaries and failure reporting, never real logins."""

import importlib.util
import base64
import gzip
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("live_e2e", Path(__file__).with_name("live-e2e.py"))
live = importlib.util.module_from_spec(spec)
spec.loader.exec_module(live)
state_spec = importlib.util.spec_from_file_location("live_state", Path(__file__).with_name("live-e2e-state.py"))
state = importlib.util.module_from_spec(state_spec)
state_spec.loader.exec_module(state)


class Safeguards(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.config = json.loads(Path(__file__).with_name("live-e2e.example.json").read_text())
        with patch.dict("os.environ", {"RADAR_BINARY": "/tmp/radar"}):
            self.suite = live.Suite(self.config, self.directory)

    def test_wrong_second_monitor_prevents_all_writes(self):
        first = {"name": live.NAMES["states"], "type": "query alert", "tags": ["radar-oauth-e2e"], "message": "fixture"}
        second = {**first, "name": "An unrelated monitor"}
        with patch.object(self.suite, "api", side_effect=[first, second]) as api:
            with self.assertRaises(live.Failure):
                self.suite.approve_monitors()
        self.assertEqual(self.suite.approved, [])
        self.assertTrue(all(len(c.args) == 1 for c in api.call_args_list))

    def test_recovery_attempts_both_monitors_if_first_write_fails(self):
        self.suite.approved = list(live.NAMES)
        with patch.object(self.suite, "update_monitor", side_effect=[live.Failure("unavailable"), None]) as update:
            with self.assertRaises(live.Failure):
                self.suite.recover()
        self.assertEqual([c.args for c in update.call_args_list], [("states", "OK"), ("no_data", "OK")])

    def test_no_notification_recipients_and_high_recovery_threshold(self):
        with patch.object(self.suite, "api") as api:
            self.suite.update_monitor("states", "OK")
        body = api.call_args.args[1]
        self.assertNotIn("@", body["message"])
        self.assertEqual(body["options"]["on_missing_data"], "resolve")
        self.assertEqual(body["options"]["renotify_interval"], 0)
        self.assertEqual(body["options"]["thresholds"], {"critical": 1000000000000})

    def test_failure_after_mutation_always_restores_config_and_monitors(self):
        def approve():
            self.suite.approved = list(live.NAMES)
        with patch.object(self.suite, "datadog_auth"), patch.object(self.suite, "approve_monitors", side_effect=approve), \
                patch.object(self.suite, "save_config") as save, patch.object(self.suite, "start"), \
                patch.object(self.suite, "update_monitor", side_effect=live.Failure("API unavailable")), \
                patch.object(self.suite, "recover") as recover:
            with self.assertRaises(live.Failure):
                self.suite.datadog_lifecycle()
        recover.assert_called_once()
        self.assertEqual(save.call_count, 2)

    def test_remote_response_is_not_in_error(self):
        with patch.object(live.subprocess, "run", return_value=live.subprocess.CompletedProcess(
                ["pup"], 1, stdout="private remote body", stderr="private provider error")):
            with self.assertRaises(live.Failure) as caught:
                live.command(["pup"], {})
        self.assertNotIn("private", str(caught.exception))

    def test_failed_config_writes_failed_junit_and_json(self):
        path, report = self.directory / "invalid.json", self.directory / "report"
        path.write_text("{}")
        with patch("sys.argv", ["live-e2e.py", "--config", str(path), "--report-dir", str(report)]), patch("builtins.print"):
            self.assertEqual(live.main(), 1)
        self.assertEqual(json.loads((report / "results.json").read_text())["checks"][0]["status"], "failed")
        self.assertEqual(live.ET.parse(report / "junit.xml").getroot().attrib["failures"], "1")

    def test_opaque_state_round_trip_uses_private_file_permissions(self):
        for key, files in (("datadog", {"sessions.json": "fixture registry", "tokens_example.json": "synthetic fixture"}),
                           ("jira", {"auth-profiles.json": "synthetic fixture", "nested/context.json": "fixture metadata"})):
            root = self.directory / key
            self.config[key]["config_dir"] = str(root)
            for name, contents in files.items():
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(contents)
        destination = self.directory / "restored"
        state.unpack(state.pack(self.config), destination)
        self.assertEqual((destination / "pup/tokens_example.json").read_text(), "synthetic fixture")
        self.assertEqual((destination / "twg/nested/context.json").read_text(), "fixture metadata")
        self.assertEqual((destination / "pup/tokens_example.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual(destination.stat().st_mode & 0o777, 0o700)

    def test_state_restore_rejects_parent_traversal_before_writing(self):
        data = {"version": 1, "providers": {"pup": {"../outside": ""}, "twg": {"auth-profiles.json": ""}}}
        bundle = base64.b64encode(gzip.compress(json.dumps(data).encode())).decode()
        target = self.directory / "restored"
        with self.assertRaises(ValueError):
            state.unpack(bundle, target)
        self.assertFalse(target.exists())

    def test_state_save_uses_stdin_and_the_environment_secret(self):
        with patch.object(state, "pack", return_value="synthetic fixture"), \
                patch.object(state.subprocess, "run", return_value=state.subprocess.CompletedProcess([], 0)) as run:
            state.save(self.config, "acme/radar")
        self.assertNotIn("synthetic fixture", run.call_args.args[0])
        self.assertEqual(run.call_args.kwargs["input"], "synthetic fixture")
        self.assertIn("live-e2e", run.call_args.args[0])


if __name__ == "__main__":
    unittest.main()
