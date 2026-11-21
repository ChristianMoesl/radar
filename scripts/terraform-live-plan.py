#!/usr/bin/env python3
"""Create one speculative run in the approved disposable Terraform workspace."""

import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time

FILES = ("main.tf", "variables.tf", "outputs.tf")
# Terraform 1.17.0's native remote plan header uses this exact route.
RUN_LINK = re.compile(r"https://app\.terraform\.io/app/radar-oss/radar-oauth-e2e/runs/(run-[A-Za-z0-9]+)(?=\s|$)")
INTENTIONAL_FAILURE = "Intentional Radar E2E plan failure."
INSPECTION_GUIDANCE = (
    "A stopped local CLI does not cancel or prove completion of remote work. "
    "Inspect run history and current queue in radar-oss/radar-oauth-e2e, including any reported run, "
    "before retrying. Confirm creator, status and blockers; remote remediation and a manual live "
    "retry require separate authorization."
)


class Failure(Exception):
    pass


def failure_evidence(phase, started, timeout, stdout, stderr):
    # TimeoutExpired can retain bytes even when subprocess.run used text=True.
    # Never echo arbitrary CLI text, URLs, provider errors or account details.
    parts = (stdout or "", stderr or "")
    run, waiting = "unavailable", "unavailable"
    if sum(map(len, parts)) <= 65536:
        output = "\n".join(value.decode("utf-8", errors="replace") if isinstance(value, bytes) else value
                           for value in parts)
        runs = set(RUN_LINK.findall(output))
        if not runs:
            run = "not-observed"
        elif len(runs) > 1:
            run = "ambiguous"
        elif len(next(iter(runs))) <= 68:
            run = runs.pop()
        waiting = "yes" if "Waiting for the plan to start..." in output else "no"
    if phase != "plan":
        waiting = "not-applicable"
    elapsed = max(0, int(time.monotonic() - started))
    # The wait marker is only a CLI observation, never an API queue diagnosis.
    return (f"phase={phase}; elapsed={elapsed}s; limit={timeout}s; run={run}; "
            f"plan_start_wait={waiting}; queue=unverified")


def command(arguments, directory, env, timeout, expect_failure=False):
    phase = arguments[0] if arguments[0] in {"init", "plan"} else "unknown"
    started = time.monotonic()
    try:
        result = subprocess.run(["terraform", f"-chdir={directory}", *arguments],
                                capture_output=True, text=True, env=env, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        evidence = failure_evidence(phase, started, timeout, error.stdout, error.stderr)
        raise Failure(f"Terraform fixture {phase} timed out ({evidence}). {INSPECTION_GUIDANCE}") from None
    output = result.stdout + "\n" + result.stderr
    if expect_failure:
        if result.returncode != 1 or "Error: Resource precondition failed" not in output or INTENTIONAL_FAILURE not in output:
            evidence = failure_evidence(phase, started, timeout, result.stdout, result.stderr)
            raise Failure(f"Terraform did not report the intentional fixture precondition failure ({evidence}). {INSPECTION_GUIDANCE}")
    elif result.returncode != 0:
        evidence = failure_evidence(phase, started, timeout, result.stdout, result.stderr)
        raise Failure(f"Terraform fixture {phase} failed; check authentication and workspace access ({evidence}). {INSPECTION_GUIDANCE}")
    # The CLI owns authentication/upload. Capture provider output privately;
    # never log credentials, remote responses, plan contents or account details.
    return output


def create_plan(configuration, inherited, expect_failure=False):
    if not inherited.get("TF_TOKEN_app_terraform_io"):
        raise Failure("Configure TFCTL_TOKEN in the selected live validation environment; the plan step passes it to Terraform's native token environment.")
    with tempfile.TemporaryDirectory(prefix="radar-terraform-fixture-") as temporary:
        root = Path(temporary)
        directory = root / "configuration"
        directory.mkdir()
        # Upload only the trusted Terraform configuration, excluding repository
        # metadata, local state, tests and credentials.
        for name in FILES:
            source = configuration / name
            if source.is_symlink() or not source.is_file() or source.stat().st_size > 65536:
                raise Failure("The pinned Terraform fixture is missing or invalid.")
            shutil.copyfile(source, directory / name)
        config = root / "cli.tfrc"
        config.write_text("disable_checkpoint = true\n")
        env = {key: value for key, value in inherited.items()
               if not key.startswith(("TF_CLI_ARGS", "TF_LOG"))}
        env.update({"TF_CLI_CONFIG_FILE": str(config), "TF_DATA_DIR": str(root / "data"),
                    "TF_WORKSPACE": "radar-oauth-e2e", "TF_CLOUD_ORGANIZATION": "radar-oss",
                    "TF_IN_AUTOMATION": "1", "TF_INPUT": "0", "CHECKPOINT_DISABLE": "1"})
        command(["init", "-input=false", "-no-color"], directory, env, 90)
        # Native `terraform plan` creates a speculative remote run. No apply,
        # saved deployment plan, state mutation or destroy command is issued.
        output = command(["plan", "-input=false", "-no-color", "-lock-timeout=30s",
                          f"-var=fail_plan={str(expect_failure).lower()}", "-var=fixture_revision=ci"],
                         directory, env, 360, expect_failure=expect_failure)
        runs = set(RUN_LINK.findall(output))
        if len(runs) != 1:
            raise Failure("Terraform did not identify exactly one remote run in the approved fixture workspace.")
        return runs.pop()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--configuration", type=Path, required=True)
    parser.add_argument("--expect-failure", action="store_true", help="Create and require the intentional fixture precondition failure")
    args = parser.parse_args()
    try:
        run_id = create_plan(args.configuration, os.environ, expect_failure=args.expect_failure)
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
            output.write(f"run_id={run_id}\n")
        print("Fresh speculative Terraform fixture run prepared; continuing with production Radar qualification.")
        return 0
    except Failure as error:
        print(str(error), file=sys.stderr)
    except (OSError, KeyError):
        print("Could not prepare the disposable Terraform fixture; check the pinned checkout and installed CLI.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
