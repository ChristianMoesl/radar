#!/usr/bin/env python3
"""Offline runner tests; fixture hooks and state never touch the user's home."""

import concurrent.futures
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("sandbox_startup", Path(__file__).with_name("startup.py"))
startup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(startup)
actual_boot_token = startup.boot_token


class StartupTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="sandbox-startup-test-")
        self.addCleanup(self.temporary.cleanup)
        self.home = Path(self.temporary.name) / "home"
        self.hooks = Path(self.temporary.name) / "startup scripts"
        self.hooks.mkdir()
        self.trace = Path(self.temporary.name) / "trace"
        environment = mock.patch.dict(os.environ, {"HOME": str(self.home), "TRACE_FILE": str(self.trace), "TEST_HOOK_VALUE": ""})
        environment.start()
        self.addCleanup(environment.stop)
        boot = mock.patch.object(startup, "boot_token", return_value="boot-A:100")
        self.boot = boot.start()
        self.addCleanup(boot.stop)
        identity = mock.patch.object(startup.os, "geteuid", return_value=1000)
        identity.start()
        self.addCleanup(identity.stop)

    def hook(self, name, body, mode=0o755):
        path = self.hooks / name
        path.write_text("#!/bin/sh\nset -eu\n" + body + "\n")
        path.chmod(mode)
        return path

    def state(self):
        return json.loads((self.home / ".cache/sandbox-startup/status.json").read_text())

    def test_unconfigured_is_immediate_noop(self):
        self.assertEqual(startup.run(""), 0)
        self.assertEqual(startup.wait("", 0.01), 0)
        self.boot.assert_not_called()
        self.assertFalse(self.home.exists())

    def test_empty_directory_is_ready(self):
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.assertEqual(startup.wait(str(self.hooks), 0.01), 0)
        self.assertEqual(self.state(), {"boot": "boot-A:100", "status": "ready"})

    def test_byte_order_regular_executables_and_separate_processes(self):
        self.hook("20-second script", 'test -z "$TEST_HOOK_VALUE"; printf "20\\n" >> "$TRACE_FILE"')
        self.hook("10-first", 'export TEST_HOOK_VALUE=child_only; printf "10\\n" >> "$TRACE_FILE"')
        self.hook("README", 'exit 99', mode=0o644)
        self.hook("30-executable-without-extension", 'printf "30\\n" >> "$TRACE_FILE"')
        (self.hooks / "00-directory").mkdir()
        (self.hooks / "00-link").symlink_to(self.hooks / "10-first")
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.assertEqual(self.trace.read_text(), "10\n20\n30\n")
        self.assertEqual(os.environ["TEST_HOOK_VALUE"], "")

    def test_ready_run_is_once_per_boot_and_new_boot_reruns(self):
        self.hook("10-trace", 'printf "run\\n" >> "$TRACE_FILE"')
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.assertEqual(self.trace.read_text(), "run\n")
        self.boot.return_value = "boot-B:100"
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.assertEqual(self.trace.read_text(), "run\nrun\n")
        self.boot.return_value = "boot-B:200"
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.assertEqual(self.trace.read_text(), "run\nrun\nrun\n")

    def test_stale_ready_does_not_pass_wait(self):
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.boot.return_value = "boot-B:100"
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertNotEqual(startup.wait(str(self.hooks), 0.01), 0)
        self.assertEqual(self.state()["boot"], "boot-A:100")

    def test_wait_before_startup_and_concurrent_startup(self):
        self.hook("10-slow", 'sleep 0.05; printf "run\\n" >> "$TRACE_FILE"')
        with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
            waiting = pool.submit(startup.wait, str(self.hooks), 2)
            time.sleep(0.03)
            self.assertFalse(waiting.done())
            first = pool.submit(startup.run, str(self.hooks))
            second = pool.submit(startup.run, str(self.hooks))
            self.assertEqual(first.result(timeout=2), 0)
            self.assertEqual(second.result(timeout=2), 0)
            self.assertEqual(waiting.result(timeout=2), 0)
        self.assertEqual(self.trace.read_text(), "run\n")

    def test_failure_stops_later_hooks_and_withholds_hook_output(self):
        self.hook("10-failing", 'printf "PRIVATE_FIXTURE_VALUE\\n"; exit 23')
        self.hook("20-must-not-run", 'printf "unexpected\\n" >> "$TRACE_FILE"')
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            self.assertEqual(startup.run(str(self.hooks)), 23)
            self.assertNotEqual(startup.wait(str(self.hooks), 1), 0)
        self.assertFalse(self.trace.exists())
        self.assertNotIn("PRIVATE_FIXTURE_VALUE", stderr.getvalue())
        self.assertEqual(self.state()["script"], "10-failing")
        self.assertEqual(self.state()["exit_code"], 23)
        output = self.home / ".cache/sandbox-startup/output.log"
        self.assertIn("PRIVATE_FIXTURE_VALUE", output.read_text())
        for name in ("status.json", "output.log", "lock"):
            self.assertEqual((output.parent / name).stat().st_mode & 0o777, 0o600)
        self.assertEqual(output.parent.stat().st_mode & 0o777, 0o700)

    def test_failed_run_can_be_retried_after_fix(self):
        self.hook("10-hook", 'exit 23')
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(startup.run(str(self.hooks)), 23)
        self.hook("10-hook", 'printf "fixed\\n" >> "$TRACE_FILE"')
        self.assertEqual(startup.run(str(self.hooks)), 0)
        self.assertEqual(startup.wait(str(self.hooks), 0.01), 0)
        self.assertEqual(self.trace.read_text(), "fixed\n")

    def test_invalid_directory_records_failure(self):
        for path in ("relative/path", str(self.hooks / "missing")):
            with self.assertRaises(startup.StartupError):
                startup.run(path)
            self.assertEqual(self.state()["status"], "failed")
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertNotEqual(startup.wait(path, 1), 0)

    def test_corrupt_or_missing_state_times_out(self):
        directory = startup.state_directory()
        for contents in ("{broken", "[]"):
            (directory / "status.json").write_text(contents)
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertNotEqual(startup.wait(str(self.hooks), 0.01), 0)

    def test_root_execution_is_refused(self):
        with mock.patch.object(startup.os, "geteuid", return_value=0):
            with self.assertRaises(startup.StartupError):
                startup.run(str(self.hooks))
            with self.assertRaises(startup.StartupError):
                startup.wait(str(self.hooks), 0.01)
        self.assertFalse(self.home.exists())

    def test_boot_identity_handles_process_name_spaces_and_parentheses(self):
        fields = ["S"] + ["0"] * 18 + ["456"]
        process = "1 (name with (parentheses)) " + " ".join(fields)
        with mock.patch.object(startup.Path, "read_text", side_effect=["boot-identity\n", process]):
            self.assertEqual(actual_boot_token(), "boot-identity:456")


if __name__ == "__main__":
    unittest.main()
