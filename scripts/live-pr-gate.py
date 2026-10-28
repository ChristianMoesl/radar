#!/usr/bin/env python3
"""Select an approved live-test PR revision using only trusted workflow code."""

import argparse
import json
import os
from pathlib import Path
import re
import sys
import urllib.error
import urllib.request

ENVIRONMENT = "live-e2e"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def github(path):
    token = os.environ.get("GH_TOKEN", "")
    require(token, "The gate needs the workflow's read-only GitHub token.")
    request = urllib.request.Request(
        os.environ.get("GITHUB_API_URL", "https://api.github.com") + path,
        headers={"Authorization": "Bearer " + token,
                 "Accept": "application/vnd.github+json",
                 "X-GitHub-Api-Version": "2022-11-28"},
    )
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise ValueError(f"GitHub metadata request failed (HTTP {error.code}); check the PR, environment setup and read permissions.") from None
    except (OSError, ValueError):
        raise ValueError("GitHub metadata could not be read; no live tests were authorized.") from None


def select_revision(env, read=github):
    require(env.get("GITHUB_EVENT_NAME") == "workflow_dispatch"
            and env.get("GITHUB_REF") == "refs/heads/main",
            "Dispatch the live workflow manually from trusted main.")
    repository, number = env.get("GITHUB_REPOSITORY", ""), env.get("PR_NUMBER", "")
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "Invalid repository.")
    require(re.fullmatch(r"[1-9][0-9]*", number), "Enter a positive PR number.")
    root = f"/repos/{repository}"
    pr = read(f"{root}/pulls/{number}")
    require(pr.get("state") == "open" and not pr.get("merged"), "Select an open, unmerged PR.")
    base, head = pr.get("base", {}), pr.get("head", {})
    require(base.get("ref") == "main" and base.get("repo", {}).get("full_name") == repository,
            "The PR must target this repository's main branch.")
    sha = head.get("sha", "")
    require(re.fullmatch(r"[0-9a-f]{40}", sha) and head.get("repo"), "The PR head must still exist.")
    require(not env.get("EXPECTED_SHA") or env["EXPECTED_SHA"] == sha,
            "The PR changed while awaiting approval. Dispatch a new run and approve its new revision.")

    # Referencing a missing environment would otherwise create an unprotected
    # environment automatically. Fail before even requesting the live job.
    environment = read(f"{root}/environments/{ENVIRONMENT}")
    rules = environment.get("protection_rules", [])
    require(any(rule.get("type") == "required_reviewers" and rule.get("reviewers") for rule in rules),
            "Configure a required reviewer on the live-e2e GitHub Environment before running live tests.")
    require(environment.get("can_admins_bypass") is False,
            "Disable administrator bypass on the live-e2e GitHub Environment.")
    policy = environment.get("deployment_branch_policy") or {}
    require(policy.get("custom_branch_policies") is True and policy.get("protected_branches") is False,
            "Restrict live-e2e to a selected branch rule for main, with no tag rules.")
    policies = read(f"{root}/environments/{ENVIRONMENT}/deployment-branch-policies?per_page=100")
    branches = policies.get("branch_policies", [])
    require(policies.get("total_count") == 1 and len(branches) == 1
            and branches[0].get("name") == "main" and branches[0].get("type") == "branch",
            "live-e2e must allow exactly the main branch and no tags.")
    return number, sha


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("resolve", "verify"))
    args = parser.parse_args()
    try:
        if args.operation == "verify":
            require(re.fullmatch(r"[0-9a-f]{40}", os.environ.get("EXPECTED_SHA", "")),
                    "The approval must identify an exact PR commit.")
        number, sha = select_revision(os.environ)
        if args.operation == "resolve":
            with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
                output.write(f"pr_number={number}\nsha={sha}\n")
            repository = os.environ["GITHUB_REPOSITORY"]
            server = os.environ.get("GITHUB_SERVER_URL", "https://github.com")
            with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
                summary.write(f"## Terraform live approval\n\nPR [#{number}]({server}/{repository}/pull/{number}), "
                              f"commit [`{sha}`]({server}/{repository}/commit/{sha}).\n\n"
                              "Review this exact revision before approving the **live-e2e** deployment. "
                              "Approval allows its code and dependencies to use the dedicated Terraform test token. "
                              "A changed PR head requires a new dispatch and approval.\n")
        print(f"PR #{number} at {sha}: metadata and required approval settings verified.")
        return 0
    except (ValueError, KeyError, TypeError, OSError) as error:
        # All emitted errors are local gate messages, never remote bodies/tokens.
        print(f"Live validation blocked: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
