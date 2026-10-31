#!/usr/bin/env python3
"""Route trusted main and explicitly approved PR code to separate live environments."""

import argparse
import json
import os
from pathlib import Path
import re
import sys
import urllib.error
import urllib.request

PR_ENVIRONMENT = "live-e2e"
MAIN_ENVIRONMENT = "live-e2e-main"


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


def validate_environment(root, name, read):
    # Referencing a missing environment would otherwise create an unprotected
    # environment automatically. Fail before even requesting the live job.
    environment = read(f"{root}/environments/{name}")
    rules = environment.get("protection_rules", [])
    if name == PR_ENVIRONMENT:
        require(any(rule.get("type") == "required_reviewers" and rule.get("reviewers") for rule in rules),
                "Configure a required reviewer on the live-e2e GitHub Environment before testing PR code.")
    else:
        require(all(rule.get("type") == "branch_policy" for rule in rules),
                "live-e2e-main must have no reviewer, wait timer or custom approval rules for trusted main runs.")
    require(environment.get("can_admins_bypass") is False,
            f"Disable administrator bypass on the {name} GitHub Environment.")
    policy = environment.get("deployment_branch_policy") or {}
    require(policy.get("custom_branch_policies") is True and policy.get("protected_branches") is False,
            f"Restrict {name} to a selected branch rule for main, with no tag rules.")
    policies = read(f"{root}/environments/{name}/deployment-branch-policies?per_page=100")
    branches = policies.get("branch_policies", [])
    require(policies.get("total_count") == 1 and len(branches) == 1
            and branches[0].get("name") == "main" and branches[0].get("type") == "branch",
            f"{name} must allow exactly the main branch and no tags.")


def select_revision(env, read=github):
    event = env.get("GITHUB_EVENT_NAME")
    require(event in ("push", "workflow_dispatch", "schedule") and env.get("GITHUB_REF") == "refs/heads/main",
            "Live validation must run from trusted main, never a PR event, topic branch or tag.")
    repository, number = env.get("GITHUB_REPOSITORY", ""), env.get("PR_NUMBER", "")
    require(re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository), "Invalid repository.")
    require(not number or (event == "workflow_dispatch" and re.fullmatch(r"[1-9][0-9]*", number)),
            "Select a positive PR number only through an explicit workflow dispatch.")
    root = f"/repos/{repository}"
    if number:
        pr = read(f"{root}/pulls/{number}")
        require(pr.get("state") == "open" and not pr.get("merged"), "Select an open, unmerged PR.")
        base, head = pr.get("base", {}), pr.get("head", {})
        require(base.get("ref") == "main" and base.get("repo", {}).get("full_name") == repository,
                "The PR must target this repository's main branch.")
        sha = head.get("sha", "")
        require(re.fullmatch(r"[0-9a-f]{40}", sha) and head.get("repo"), "The PR head must still exist.")
        require(not env.get("EXPECTED_SHA") or env["EXPECTED_SHA"] == sha,
                "The PR changed while awaiting approval. Dispatch a new run and approve its new revision.")
        name = PR_ENVIRONMENT
    else:
        sha = env.get("GITHUB_SHA", "")
        require(re.fullmatch(r"[0-9a-f]{40}", sha), "Trusted main validation requires the exact workflow commit.")
        require(not env.get("EXPECTED_SHA") or env["EXPECTED_SHA"] == sha,
                "Main validation must check out the trusted workflow commit, not another revision.")
        name = MAIN_ENVIRONMENT
    validate_environment(root, name, read)
    return {"pr_number": number, "sha": sha, "environment": name}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("resolve", "verify"))
    args = parser.parse_args()
    try:
        if args.operation == "verify":
            require(re.fullmatch(r"[0-9a-f]{40}", os.environ.get("EXPECTED_SHA", "")),
                    "Live verification must identify an exact commit.")
        revision = select_revision(os.environ)
        number, sha, name = (revision[key] for key in ("pr_number", "sha", "environment"))
        if args.operation == "resolve":
            with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
                for key, value in revision.items():
                    output.write(f"{key}={value}\n")
            repository = os.environ["GITHUB_REPOSITORY"]
            server = os.environ.get("GITHUB_SERVER_URL", "https://github.com")
            with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
                summary.write("## Terraform live validation\n\n")
                if number:
                    summary.write(f"PR [#{number}]({server}/{repository}/pull/{number}), "
                                  f"commit [`{sha}`]({server}/{repository}/commit/{sha}).\n\n"
                                  "Review this exact revision before approving the **live-e2e** deployment. "
                                  "Approval allows its code and dependencies to use the dedicated Terraform test token. "
                                  "A changed PR head requires a new dispatch and approval.\n\n")
                else:
                    summary.write(f"Trusted main commit [`{sha}`]({server}/{repository}/commit/{sha}) "
                                  "uses **live-e2e-main** automatically, without an approval. "
                                  "No PR code is selected on this path.\n\n")
                summary.write("The trusted fixture harness uploads configuration, applies a built-in `terraform_data` fixture "
                              "and cancels another run in `radar-oss/radar-oauth-e2e`. It checks zero planned resource "
                              "destructions before apply. No external infrastructure is created or destroyed. "
                              "Recovery checks interrupt only the isolated daemon's read connection.\n")
        target = f"PR #{number}" if number else "Trusted main"
        print(f"{target} at {sha}: revision and {name} environment settings verified.")
        return 0
    except (ValueError, KeyError, TypeError, OSError) as error:
        # All emitted errors are local gate messages, never remote bodies/tokens.
        print(f"Live validation blocked: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
