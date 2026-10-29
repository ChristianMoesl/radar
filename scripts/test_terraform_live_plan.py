"""Offline boundaries for remote plan-only fixture creation."""

import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("terraform_live_plan", Path(__file__).with_name("terraform-live-plan.py"))
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)
RUN_URL = "https://app.terraform.io/app/radar-oss/radar-oauth-e2e/runs/run-Fixture123"


class FixturePlan(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.configuration = Path(self.temp.name)
        for name in plan.FILES:
            (self.configuration / name).write_text("fixture configuration")
        (self.configuration / "private-jira-fixtures.json").write_text("must not be uploaded")
        self.env = {"TF_TOKEN_app_terraform_io": "synthetic-token"}

    def run_plan(self, output=RUN_URL):
        with patch.object(plan.subprocess, "run", side_effect=[
                subprocess.CompletedProcess([], 0, "initialized", ""),
                subprocess.CompletedProcess([], 0, output, "")]) as execute:
            run_id = plan.create_plan(self.configuration, self.env)
        return run_id, execute

    def test_native_speculative_plan_never_applies_or_destroys(self):
        run_id, execute = self.run_plan()
        self.assertEqual(run_id, "run-Fixture123")
        commands = [call.args[0] for call in execute.call_args_list]
        self.assertEqual([command[2] for command in commands], ["init", "plan"])
        self.assertIn("-var=fail_plan=false", commands[1])
        for command in commands:
            self.assertNotIn("apply", command)
            self.assertNotIn("destroy", command)
            self.assertFalse(any(argument.startswith("-out") for argument in command))

    def test_only_configuration_files_enter_the_disposable_directory(self):
        def execute(arguments, **kwargs):
            directory = Path(arguments[1].split("=", 1)[1])
            self.assertEqual({path.name for path in directory.iterdir()}, set(plan.FILES))
            self.assertNotEqual(directory, self.configuration)
            return subprocess.CompletedProcess([], 0, RUN_URL, "")
        with patch.object(plan.subprocess, "run", side_effect=execute):
            plan.create_plan(self.configuration, self.env)

    def test_cli_flags_and_trace_logging_cannot_be_inherited(self):
        self.env.update({"TF_CLI_ARGS_plan": "-destroy", "TF_LOG": "TRACE", "TF_LOG_PATH": "/tmp/provider.log",
                         "TF_WORKSPACE": "production", "TF_CLI_CONFIG_FILE": "/tmp/other-credentials"})
        _, execute = self.run_plan()
        env = execute.call_args.kwargs["env"]
        self.assertFalse(any(key.startswith(("TF_CLI_ARGS", "TF_LOG")) for key in env))
        self.assertEqual(env["TF_WORKSPACE"], "radar-oauth-e2e")
        self.assertEqual(env["TF_CLOUD_ORGANIZATION"], "radar-oss")
        self.assertNotEqual(env["TF_CLI_CONFIG_FILE"], "/tmp/other-credentials")

    def test_missing_token_never_starts_a_cli(self):
        with patch.object(plan.subprocess, "run") as execute, self.assertRaises(plan.Failure):
            plan.create_plan(self.configuration, {})
        execute.assert_not_called()

    def test_missing_and_symlinked_configuration_never_starts_a_cli(self):
        target = self.configuration / "main.tf"
        target.unlink()
        for symlink in (False, True):
            if symlink:
                target.symlink_to(self.configuration / "outputs.tf")
            with self.subTest(symlink=symlink), patch.object(plan.subprocess, "run") as execute, self.assertRaises(plan.Failure):
                plan.create_plan(self.configuration, self.env)
            execute.assert_not_called()

    def test_local_wrong_scope_and_ambiguous_run_outputs_are_rejected(self):
        for output in ("Local plan succeeded", RUN_URL.replace("radar-oss", "other"),
                       RUN_URL.replace("radar-oauth-e2e", "production"), RUN_URL + "\n" + RUN_URL.replace("Fixture123", "Other456")):
            with self.subTest(output=output), self.assertRaises(plan.Failure):
                self.run_plan(output)

    def test_failure_never_exposes_private_cli_output(self):
        with patch.object(plan.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 1, "private provider response", "synthetic-token")):
            with self.assertRaises(plan.Failure) as caught:
                plan.create_plan(self.configuration, self.env)
        self.assertNotIn("private", str(caught.exception))
        self.assertNotIn("synthetic-token", str(caught.exception))

    def test_timed_out_plan_is_not_blindly_retried(self):
        with patch.object(plan.subprocess, "run", side_effect=[subprocess.CompletedProcess([], 0, "", ""),
                          subprocess.TimeoutExpired([], 360)]) as execute:
            with self.assertRaises(plan.Failure):
                plan.create_plan(self.configuration, self.env)
        self.assertEqual(execute.call_count, 2)


if __name__ == "__main__":
    unittest.main()
