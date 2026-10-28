"""Offline checks for the live credential and PR-revision approval boundary."""

import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("live_pr_gate", Path(__file__).with_name("live-pr-gate.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class ApprovalGate(unittest.TestCase):
    def setUp(self):
        self.sha = "a" * 40
        self.env = {"GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main",
                    "GITHUB_REPOSITORY": "acme/radar", "PR_NUMBER": "12"}
        self.root = "/repos/acme/radar"
        self.pr = {"state": "open", "merged": False,
                   "base": {"ref": "main", "repo": {"full_name": "acme/radar"}},
                   "head": {"sha": self.sha, "repo": {"full_name": "contributor/radar"}}}
        self.environment = {"protection_rules": [{"type": "required_reviewers", "reviewers": [
            {"type": "User", "reviewer": {"login": "maintainer"}}]}], "can_admins_bypass": False,
            "deployment_branch_policy": {"custom_branch_policies": True, "protected_branches": False}}
        self.policies = {"total_count": 1, "branch_policies": [{"name": "main", "type": "branch"}]}
        self.requests = []

    def read(self, path):
        self.requests.append(path)
        return copy.deepcopy({f"{self.root}/pulls/12": self.pr,
                              f"{self.root}/environments/live-e2e": self.environment,
                              f"{self.root}/environments/live-e2e/deployment-branch-policies?per_page=100": self.policies}[path])

    def select(self, **changes):
        return gate.select_revision({**self.env, **changes}, self.read)

    def test_open_fork_pr_is_pinned_to_its_exact_commit(self):
        self.assertEqual(self.select(), ("12", self.sha))
        self.assertEqual(self.select(EXPECTED_SHA=self.sha), ("12", self.sha))

    def test_automatic_and_non_main_dispatches_never_read_the_pr(self):
        for changes in ({"GITHUB_EVENT_NAME": "pull_request"}, {"GITHUB_EVENT_NAME": "pull_request_target"},
                        {"GITHUB_EVENT_NAME": "schedule"}, {"GITHUB_REF": "refs/heads/topic"},
                        {"GITHUB_REF": "refs/tags/main"}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.select(**changes)
        self.assertEqual(self.requests, [])

    def test_invalid_pr_inputs_never_become_api_paths_or_workflow_outputs(self):
        for value in ("", "0", "-1", "12\nsha=evil", "12/../13", "$(id)"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.select(PR_NUMBER=value)
        self.assertEqual(self.requests, [])

    def test_closed_merged_and_wrong_target_prs_are_rejected(self):
        for changes in ({"state": "closed"}, {"merged": True}, {"base": {"ref": "other"}},
                        {"base": {"ref": "main", "repo": {"full_name": "other/radar"}}},
                        {"head": {"sha": "main", "repo": {}}}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                with patch.dict(self.pr, changes):
                    self.select()

    def test_changed_head_after_approval_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "changed while awaiting approval"):
            self.select(EXPECTED_SHA="b" * 40)
        self.assertEqual(self.requests, [f"{self.root}/pulls/12"])

    def test_missing_reviewers_and_administrator_bypass_fail_closed(self):
        for changes in ({"protection_rules": []}, {"protection_rules": [{"type": "wait_timer"}]},
                        {"protection_rules": [{"type": "required_reviewers", "reviewers": []}]},
                        {"can_admins_bypass": True}, {"can_admins_bypass": None}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                with patch.dict(self.environment, changes):
                    self.select()

    def test_only_an_explicit_main_branch_rule_is_accepted(self):
        for policy in (None, {"custom_branch_policies": False, "protected_branches": True}):
            with self.subTest(policy=policy), self.assertRaises(ValueError):
                with patch.dict(self.environment, deployment_branch_policy=policy):
                    self.select()
        for branches in ([], [{"name": "*", "type": "branch"}], [{"name": "main", "type": "tag"}],
                         [{"name": "main", "type": "branch"}, {"name": "topic", "type": "branch"}]):
            with self.subTest(branches=branches), self.assertRaises(ValueError):
                with patch.dict(self.policies, total_count=len(branches), branch_policies=branches):
                    self.select()

    def test_resolve_outputs_the_revision_and_links_it_for_review(self):
        with tempfile.TemporaryDirectory() as directory:
            output, summary = Path(directory) / "outputs", Path(directory) / "summary"
            env = {**self.env, "GITHUB_OUTPUT": str(output), "GITHUB_STEP_SUMMARY": str(summary)}
            with patch.dict("os.environ", env, clear=True), patch("sys.argv", ["gate", "resolve"]), \
                    patch.object(gate, "select_revision", return_value=("12", self.sha)), patch("builtins.print"):
                self.assertEqual(gate.main(), 0)
            self.assertEqual(output.read_text(), f"pr_number=12\nsha={self.sha}\n")
            self.assertIn(f"https://github.com/acme/radar/commit/{self.sha}", summary.read_text())
            self.assertIn("**live-e2e**", summary.read_text())
            self.assertIn("Terraform test token", summary.read_text())

    def test_post_approval_verification_requires_the_pinned_revision(self):
        with patch.dict("os.environ", self.env, clear=True), patch("sys.argv", ["gate", "verify"]), \
                patch.object(gate, "select_revision") as select, patch("builtins.print"):
            self.assertEqual(gate.main(), 1)
            select.assert_not_called()

    def test_remote_errors_never_print_the_response_body_or_token(self):
        error = gate.urllib.error.HTTPError("https://api.github.com", 403, "private response", {}, None)
        with patch.dict("os.environ", {"GH_TOKEN": "synthetic-token"}), \
                patch.object(gate.urllib.request, "urlopen", side_effect=error):
            with self.assertRaises(ValueError) as caught:
                gate.github("/repos/acme/radar/environments/live-e2e")
        self.assertIn("HTTP 403", str(caught.exception))
        self.assertNotIn("private response", str(caught.exception))
        self.assertNotIn("synthetic-token", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
