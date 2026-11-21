"""Offline boundaries for remote plan-only fixture creation."""

import importlib.util
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("terraform_live_plan", Path(__file__).with_name("terraform-live-plan.py"))
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)
RUN_URL = "https://app.terraform.io/app/radar-oss/radar-oauth-e2e/runs/run-Fixture123"
EXPECTED_FAILURE = f"Error: Resource precondition failed\n{plan.INTENTIONAL_FAILURE}\n"


class FixturePlan(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.configuration = Path(self.temp.name)
        for name in plan.FILES:
            (self.configuration / name).write_text("fixture configuration")
        (self.configuration / "private-jira-fixtures.json").write_text("must not be uploaded")
        self.env = {"TF_TOKEN_app_terraform_io": "synthetic-token"}

    def run_plan(self, output=RUN_URL, returncode=0, expect_failure=False):
        with patch.object(plan.subprocess, "run", side_effect=[
                subprocess.CompletedProcess([], 0, "initialized", ""),
                subprocess.CompletedProcess([], returncode, output, "")]) as execute:
            run_id = plan.create_plan(self.configuration, self.env, expect_failure=expect_failure)
        return run_id, execute

    def test_native_speculative_plan_never_applies_or_destroys(self):
        run_id, execute = self.run_plan()
        self.assertEqual(run_id, "run-Fixture123")
        commands = [call.args[0] for call in execute.call_args_list]
        self.assertEqual([command[2] for command in commands], ["init", "plan"])
        self.assertEqual([call.kwargs["timeout"] for call in execute.call_args_list], [90, 360])
        self.assertIn("-var=fail_plan=false", commands[1])
        for command in commands:
            self.assertNotIn("apply", command)
            self.assertNotIn("destroy", command)
            self.assertFalse(any(argument.startswith("-out") for argument in command))

    def test_intentional_failure_requires_its_diagnostic_and_exact_remote_run(self):
        run_id, execute = self.run_plan(RUN_URL + "\n" + EXPECTED_FAILURE, returncode=1, expect_failure=True)
        self.assertEqual(run_id, "run-Fixture123")
        self.assertIn("-var=fail_plan=true", execute.call_args.args[0])

    def test_unrelated_errors_and_unexpected_success_cannot_satisfy_failure_scenario(self):
        for returncode, output in ((0, RUN_URL + "\n" + EXPECTED_FAILURE),
                                   (1, RUN_URL + "\nAuthentication failed"),
                                   (1, RUN_URL + "\nError: Resource precondition failed\nA different failure"),
                                   (1, EXPECTED_FAILURE),
                                   (1, RUN_URL.replace("radar-oauth-e2e", "production") + "\n" + EXPECTED_FAILURE),
                                   (-9, RUN_URL + "\n" + EXPECTED_FAILURE)):
            with self.subTest(returncode=returncode, output=output), self.assertRaises(plan.Failure):
                self.run_plan(output, returncode=returncode, expect_failure=True)

    def test_expected_failure_does_not_mask_initialization_or_normal_plan_errors(self):
        with patch.object(plan.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 1, RUN_URL, EXPECTED_FAILURE)) as execute, self.assertRaises(plan.Failure):
            plan.create_plan(self.configuration, self.env, expect_failure=True)
        self.assertEqual(execute.call_count, 1)
        with self.assertRaises(plan.Failure):
            self.run_plan(RUN_URL + "\n" + EXPECTED_FAILURE, returncode=1)

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

    def command_failure(self, phase="plan", output=None, stderr=None, timeout=360, returncode=None, elapsed=None):
        result = subprocess.TimeoutExpired([], timeout, output=output, stderr=stderr)
        if returncode is not None:
            result = subprocess.CompletedProcess([], returncode, output or "", stderr or "")
        with patch.object(time, "monotonic", side_effect=[10, 10 + (timeout if elapsed is None else elapsed)]), \
                patch.object(plan.subprocess, "run", side_effect=[result]) as execute:
            with self.assertRaises(plan.Failure) as caught:
                plan.command([phase], self.configuration, self.env, timeout)
        self.assertEqual(execute.call_count, 1)
        self.assertEqual(execute.call_args.kwargs["timeout"], timeout)
        return str(caught.exception)

    def test_timeout_retains_only_bounded_phase_and_approved_run_evidence(self):
        output = (RUN_URL + "\nWaiting for the plan to start...\nprivate plan contents").encode()
        message = self.command_failure(output=output, stderr=b"synthetic-token private provider error\xff")
        for expected in ("phase=plan", "elapsed=360s", "limit=360s", "run=run-Fixture123",
                         "plan_start_wait=yes", "queue=unverified", "does not cancel",
                         "creator, status and blockers", "separate authorization"):
            self.assertIn(expected, message)
        for private in ("synthetic-token", "private plan", "private provider"):
            self.assertNotIn(private, message)
        self.assertLess(len(message), 1024)

    def test_init_timeout_does_not_claim_a_remote_plan_or_queue_diagnosis(self):
        message = self.command_failure(phase="init", timeout=90, output=b"private init output")
        for expected in ("phase=init", "elapsed=90s", "limit=90s", "run=not-observed",
                         "plan_start_wait=not-applicable", "queue=unverified"):
            self.assertIn(expected, message)
        self.assertNotIn("private init", message)

    def test_missing_partial_output_does_not_claim_no_remote_work(self):
        message = self.command_failure()
        self.assertIn("run=not-observed", message)
        self.assertIn("plan_start_wait=no", message)
        self.assertIn("run history and current queue", message)
        self.assertNotIn("no remote run", message)

    def test_ambiguous_foreign_and_oversized_output_fail_closed(self):
        cases = [
            (RUN_URL + "\n" + RUN_URL.replace("Fixture123", "Other456"), "ambiguous"),
            (RUN_URL.replace("radar-oauth-e2e", "production"), "not-observed"),
            (RUN_URL.replace("Fixture123", "X" * 1000), "unavailable"),
            ("private" * 20000 + RUN_URL, "unavailable"),
        ]
        for output, expected in cases:
            with self.subTest(expected=expected):
                message = self.command_failure(output=output)
                self.assertIn("run=" + expected, message)
                self.assertIn("queue=unverified", message)
                self.assertNotIn("run-Fixture123", message)
                self.assertNotIn("Other456", message)
                self.assertNotIn("private", message)
                self.assertLess(len(message), 1024)

    def test_wait_marker_is_literal_cli_evidence_not_a_queue_state(self):
        for suffix, expected in (("Waiting for the plan to start...", "yes"),
                                 ("private provider says queued", "no")):
            with self.subTest(expected=expected):
                message = self.command_failure(output=RUN_URL + "\n" + suffix)
                self.assertIn("plan_start_wait=" + expected, message)
                self.assertIn("queue=unverified", message)
                self.assertNotIn("provider says", message)

    def test_nonzero_failure_keeps_the_same_sanitized_inspection_evidence(self):
        message = self.command_failure(output=RUN_URL, stderr="synthetic-token", returncode=1, elapsed=12.9)
        self.assertIn("phase=plan", message)
        self.assertIn("elapsed=12s", message)
        self.assertIn("limit=360s", message)
        self.assertIn("run=run-Fixture123", message)
        self.assertIn("separate authorization", message)
        self.assertNotIn("synthetic-token", message)

    def test_repeated_same_run_link_is_not_ambiguous(self):
        message = self.command_failure(output=RUN_URL + "\n" + RUN_URL)
        self.assertIn("run=run-Fixture123", message)
        self.assertNotIn("ambiguous", message)

    def test_failed_expected_failure_check_has_inspection_evidence_without_raw_text(self):
        with patch.object(time, "monotonic", side_effect=[10, 12]), \
                patch.object(plan.subprocess, "run", return_value=subprocess.CompletedProcess(
                    [], 1, RUN_URL, "synthetic-token private provider response")) as execute:
            with self.assertRaises(plan.Failure) as caught:
                plan.command(["plan"], self.configuration, self.env, 360, expect_failure=True)
        self.assertEqual(execute.call_count, 1)
        message = str(caught.exception)
        self.assertIn("did not report the intentional fixture precondition failure", message)
        self.assertIn("phase=plan; elapsed=2s; limit=360s; run=run-Fixture123", message)
        self.assertNotIn("private provider", message)
        self.assertNotIn("synthetic-token", message)


if __name__ == "__main__":
    unittest.main()
