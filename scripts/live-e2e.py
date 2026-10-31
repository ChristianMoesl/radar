#!/usr/bin/env python3
"""Real-account CI checks. Credentials stay in the provider CLIs' own stores."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import time
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent.parent
METRIC = "avg:datadog.apis.rate_limit.usage.per_org_pct{*}"
NAMES = {"states": "Radar OAuth E2E - States", "no_data": "Radar OAuth E2E - No Data"}


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


def command(argv, env, *, body=None, timeout=120, parse=True, cwd=ROOT):
    # Capture output privately. Never publish raw provider errors, responses,
    # environment dumps or credential files as CI logs/artifacts.
    try:
        result = subprocess.run(argv, env=env, cwd=cwd, input=body,
                                text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise Failure(f"{Path(argv[0]).name} exceeded its deadline") from None
    require(result.returncode == 0,
            f"{Path(argv[0]).name} failed (exit {result.returncode}); check CLI login, permissions and fixture setup on the runner")
    if not parse:
        return result.stdout.strip()
    try:
        return json.loads(result.stdout)
    except ValueError:
        raise Failure(f"{Path(argv[0]).name} returned invalid JSON") from None


def validate(config):
    dd, jira, github = (config[k] for k in ("datadog", "jira", "github"))
    for provider in (dd, jira):
        require(Path(provider["config_dir"]).is_absolute(), "CLI config_dir must be absolute")
    require(dd["org"] and dd["site"], "Datadog requires an explicit org and site")
    ids = [dd[k + "_monitor_id"] for k in NAMES]
    require(all(type(i) is int and i > 0 for i in ids) and len(set(ids)) == 2,
            "select two distinct approved monitor IDs")
    require(jira["profile"] and jira["site"], "Jira requires an explicit OAuth profile and Cloud ID")
    keys = jira["issue_keys"]
    require(len(keys) >= 2 and len(set(keys)) == len(keys)
            and all(re.fullmatch(r"[A-Z][A-Z0-9_]*-[1-9][0-9]*", k) for k in keys),
            "select at least two distinct Jira issue keys for pagination")
    require(len(github["fixtures"]) > 0, "select approved GitHub PR fixtures")


class Suite:
    def __init__(self, config, directory):
        self.config, self.directory = config, directory
        self.dd, self.jira = config["datadog"], config["jira"]
        self.env = dict(os.environ)
        # Provider process overrides must not replace explicit OAuth selection.
        for key in list(self.env):
            if key.startswith(("TWG_", "PUP_")) or key in (
                "DD_ACCESS_TOKEN", "DD_API_KEY", "DD_APP_KEY", "DD_SITE", "DD_ORG", "DD_OUTPUT",
            ) or key.startswith("RADAR_TEST_"):
                self.env.pop(key)
        self.env.update(PUP_CONFIG_DIR=self.dd["config_dir"], DD_SITE=self.dd["site"],
                        DD_TOKEN_STORAGE="file", DD_API_KEY="\n", DD_APP_KEY="\n",
                        TWG_CONFIG_DIR=self.jira["config_dir"])
        self.binary = str(Path(os.environ["RADAR_BINARY"]).resolve())
        self.env.update(RADAR_BINARY=self.binary, XDG_CONFIG_HOME=str(directory / "config"),
                        XDG_DATA_HOME=str(directory / "data"), XDG_STATE_HOME=str(directory / "state"),
                        RADAR_SOCKET=str(directory / "r.sock"), RADAR_PID=str(directory / "r.pid"),
                        RADAR_STATE=str(directory / "state.db"), RADAR_AGENT_ROOT=str(directory / "agents"),
                        RADAR_AGENT_HOST=str(ROOT / "agent-host/src/runner.ts"),
                        GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null", TMUX="")
        self.env.pop("RADAR_DISABLE_COLLECTION", None)
        self.daemon = None
        self.approved = []
        self.identities = None
        self.radar_config = {
            "repository_dirs": [str(directory / "repo")], "linking_mark_prefixes": ["ABC"],
            "workspace": {"root_dir": str(directory / "workspaces"), "auto_confirm": True},
            "obsidian": {"vault_path": str(directory / "vault")},
            "github": {"enabled": False}, "jira": {"enabled": False}, "sbx": {"enabled": False},
            "datadog": {"enabled": True, "site": self.dd["site"], "org": self.dd["org"],
                        "monitor_query": " OR ".join(f"id:{self.dd[k + '_monitor_id']}" for k in NAMES)},
        }

    def pup(self, *args, parse=True):
        return command(["pup", "--org", self.dd["org"], "--no-envelope", "--output", "json", *args],
                       self.env, parse=parse, timeout=40)

    def api(self, path, body=None, method="GET", fields=()):
        argv = ["pup", "--org", self.dd["org"], "--no-envelope", "--output", "json",
                "api", path, "-X", method, *fields]
        if body is not None:
            argv += ["--input", "-"]
        return command(argv, self.env, body=json.dumps(body) if body is not None else None, timeout=40)

    def twg(self, *args):
        return command(["twg", "--profile", self.jira["profile"], "--site", self.jira["site"],
                        "--output", "json", "--output-summary", "none", "--timeout-ms", "20000", *args], self.env, timeout=40)

    def jira_reads(self):
        require(command(["twg", "--version"], self.env, parse=False) == "1.3.8", "install qualified TWG 1.3.8")
        profiles = self.twg("auth", "profile", "list")["data"]["profiles"]
        selected = [p for p in profiles if p["name"] == self.jira["profile"]]
        require(len(selected) == 1 and selected[0]["authMethod"] == "oauth", "selected TWG profile must use OAuth")
        require(selected[0]["cloudId"] == self.jira["site"], "TWG profile Cloud ID does not match the selected site")
        self.twg("auth", "refresh", "--force")
        keys, seen, cursor = set(self.jira["issue_keys"]), set(), None
        cursors = set()
        # Page size one deliberately exercises real cursor continuation.
        for _ in range(len(keys) + 1):
            args = ["jira", "workitem", "query", "--jql", "key in (" + ",".join(sorted(keys)) + ") ORDER BY key ASC",
                    "--fields", "summary,status", "--first", "1"]
            if cursor:
                args += ["--after", cursor]
            result = self.twg(*args)
            rows = result["data"]["issues"]
            page = result.get("pageInfo", {})
            # Jira may advertise one final cursor after the last full page.
            if not rows:
                require(seen == keys and not page.get("hasNextPage", False),
                        "TWG pagination stopped before all fixtures or advertised another empty page")
                break
            require(len(rows) == 1 and rows[0]["key"] in keys and rows[0]["key"] not in seen,
                    "TWG issue pagination returned missing, duplicate or unexpected fixtures")
            require(rows[0]["status"]["statusCategory"]["key"], "TWG issue status category is missing")
            seen.add(rows[0]["key"])
            if not page.get("hasNextPage", False):
                break
            cursor = page.get("nextCursor")
            require(cursor and cursor not in cursors, "TWG pagination has no fresh cursor")
            cursors.add(cursor)
        require(seen == keys and len(cursors) > 0, "TWG did not return every selected issue across pages")
        profile = selected[0]
        require(profile.get("domain") and profile.get("userId"), "TWG profile must identify its site and account")
        selectors = {"base_url": "https://" + profile["domain"], "cloud_id": self.jira["site"],
                     "profile": self.jira["profile"], "account_id": profile["userId"],
                     "issue_keys": self.jira["issue_keys"]}
        path = self.directory / "jira-fixtures.json"
        path.write_text(json.dumps(selectors))
        command(["go", "test", "./internal/integration/jira", "-run", "^TestLiveTWGOAuthCollection$", "-count=1"],
                {**self.env, "RADAR_TEST_JIRA_FIXTURES": str(path)}, parse=False, timeout=240)
        self.jira_daemon(selectors)

    def jira_daemon(self, selectors):
        # Keep Jira task persistence independent of the Datadog lifecycle run.
        directory = self.directory / "jira-run"
        directory.mkdir()
        run = Suite(self.config, directory)
        run.radar_config["datadog"]["enabled"] = False
        run.radar_config["jira"] = {"enabled": True, **{k: v for k, v in selectors.items() if k != "issue_keys"}}
        run.radar_config["linking_mark_prefixes"] = sorted({k.split("-")[0] for k in selectors["issue_keys"]})
        for name in ("repo", "vault"):
            (directory / name).mkdir()
        identities = None

        def snapshot(error=False):
            nonlocal identities
            refresh = run.radar("refresh", "--json")
            source = next(s for s in refresh["sources"] if s["name"] == "jira")
            require(source["status"] == ("error" if error else "ok"), "unexpected Radar Jira source status")
            tasks = run.radar("tasks", "--json")
            rows = tasks.get("tasks", []) if isinstance(tasks, dict) else tasks
            observed = {}
            for task in rows:
                for ref in task.get("source_refs", []):
                    if ref.get("source") == "jira" and ref.get("metadata", {}).get("key") in selectors["issue_keys"]:
                        require(ref["id"] not in observed, "duplicate Jira task")
                        observed[ref["id"]] = (task["id"], ref["status"], ref["title"])
            require(len(observed) >= 2, "select at least two assigned open Jira fixtures for daemon retention")
            if identities is None:
                identities = observed
            require(observed == identities, "Jira tasks changed across failed login or daemon restart")

        try:
            run.save_config()
            run.start()
            snapshot()
            run.start()
            snapshot()
            run.radar_config["jira"]["profile"] += "-missing-e2e"
            run.save_config()
            snapshot(error=True)
            run.start()
            snapshot(error=True)
            run.radar_config["jira"]["profile"] = selectors["profile"]
            run.save_config()
            snapshot()
        finally:
            run.stop()

    def github_links(self):
        path = self.directory / "github-fixtures.json"
        path.write_text(json.dumps(self.config["github"]["fixtures"]))
        env = {**self.env, "RADAR_TEST_GITHUB_LINK_FIXTURES": str(path)}
        command(["go", "test", "./internal/integration/github", "-run", "^TestLivePullRequestReferences$", "-count=1"],
                env, parse=False, timeout=240)

    def datadog_auth(self):
        require(command(["pup", "--version"], self.env, parse=False) == "pup 2.0.0", "install qualified Pup 2.0.0")
        self.pup("auth", "refresh", parse=False)
        status = self.pup("auth", "status")
        require(status.get("authenticated") and status.get("has_refresh")
                and status.get("token_type", "").lower() == "bearer"
                and status.get("org") == self.dd["org"] and status.get("site") == self.dd["site"],
                "Pup needs a refreshable named OAuth session for the selected site")
        require({"monitors_read", "monitors_write"} <= set(status.get("scopes", [])),
                "Pup OAuth needs monitors_read and monitors_write")
        with ThreadPoolExecutor(max_workers=6) as pool:
            list(pool.map(lambda _: self.pup("auth", "refresh", parse=False), range(6)))
            list(pool.map(lambda _: self.api("/api/v1/monitor/search", fields=("-f", "query=" + self.radar_config["datadog"]["monitor_query"])), range(6)))

    def approve_monitors(self):
        for kind, name in NAMES.items():
            monitor = self.api(f"/api/v1/monitor/{self.dd[kind + '_monitor_id']}")
            require(monitor.get("name") == name and monitor.get("type") == "query alert"
                    and "radar-oauth-e2e" in monitor.get("tags", []) and "@" not in monitor.get("message", ""),
                    "monitor ID must select the approved named, tagged fixture without notification recipients")
        # Approve both before any write, so a misconfigured ID never causes a partial mutation.
        self.approved = list(NAMES)

    def update_monitor(self, kind, state):
        critical = -1 if state == "Alert" else 1 if state == "No Data" else 1000000000000
        thresholds = {"critical": critical}
        if state == "Alert":
            thresholds["warning"] = -2
        if state == "Warn":
            thresholds["warning"] = -1
        metric = "avg:datadog.apis.rate_limit.usage.per_org_pct{radar_oauth_e2e:missing}" if state == "No Data" else METRIC
        body = {"name": NAMES[kind], "type": "query alert", "query": f"avg(last_1m):{metric} > {critical}",
                "message": "Radar OAuth E2E fixture. No notification recipients.", "tags": ["radar-oauth-e2e"],
                "options": {"thresholds": thresholds, "on_missing_data": "resolve" if state == "OK" else "show_no_data",
                            "require_full_window": False, "include_tags": False,
                            "notify_audit": False, "renotify_interval": 0}}
        self.api(f"/api/v1/monitor/{self.dd[kind + '_monitor_id']}", body, "PUT")

    def wait_monitors(self, expected):
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            result = self.api("/api/v1/monitor/search", fields=("-f", "query=" + self.radar_config["datadog"]["monitor_query"]))
            states = {int(m["id"]): m["status"] for m in result["monitors"]}
            if all(states.get(self.dd[k + "_monitor_id"]) == v for k, v in expected.items()):
                return
            time.sleep(10)
        raise Failure("Datadog indexed monitor states did not reach the expected values within five minutes")

    def save_config(self, org=None):
        self.radar_config["datadog"]["org"] = org or self.dd["org"]
        path = self.directory / "config/radar/config.yaml"
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_suffix(".tmp")
        temporary.write_text(json.dumps(self.radar_config))
        temporary.replace(path)

    def radar(self, *args, parse=True):
        return command([self.binary, *args], self.env, parse=parse, timeout=90)

    def stop(self):
        if self.daemon is not None:
            try:
                self.radar("stop", parse=False)
            finally:
                try:
                    self.daemon.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    self.daemon.terminate()
                    self.daemon.wait(timeout=10)
                self.daemon = None

    def start(self):
        self.stop()
        self.daemon = subprocess.Popen([self.binary, "daemon"], env=self.env, cwd=ROOT,
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            require(self.daemon.poll() is None, "isolated Radar daemon exited at startup")
            if Path(self.env["RADAR_SOCKET"]).exists():
                return
            time.sleep(.1)
        raise Failure("isolated Radar daemon did not become ready")

    def snapshot(self, expected, error=False):
        refresh = self.radar("refresh", "--json")
        source = next(s for s in refresh["sources"] if s["name"] == "datadog")
        require(source["status"] == ("error" if error else "ok"), "unexpected Radar Datadog source status")
        tasks = self.radar("tasks", "--json")
        rows = tasks.get("tasks", []) if isinstance(tasks, dict) else tasks
        observed, identities = {}, {}
        for task in rows:
            for ref in task.get("source_refs", []):
                if ref.get("source") == "datadog":
                    require(ref["id"] not in observed, "duplicate monitor task")
                    observed[ref["id"]] = (task["attention"], ref["status"])
                    identities[ref["id"]] = task["id"]
        wanted = {f"datadog:monitor:{self.dd[k + '_monitor_id']}": v for k, v in expected.items()}
        require(observed == wanted, "Radar task signals or monitor identities do not match the live states")
        if self.identities is None:
            self.identities = identities
        require(identities == self.identities, "Radar task IDs changed across transition or restart")

    def recover(self):
        errors = []
        for kind in self.approved:
            try:
                self.update_monitor(kind, "OK")
            except Exception:
                errors.append(kind)
        require(not errors, "could not restore one or both test monitors; run the documented recovery command")
        if self.approved:
            self.wait_monitors({k: "OK" for k in self.approved})

    def datadog_lifecycle(self):
        self.datadog_auth()
        self.approve_monitors()
        try:
            for name in ("repo", "vault"):
                (self.directory / name).mkdir()
            self.save_config()
            self.start()
            self.update_monitor("states", "Alert")
            self.update_monitor("no_data", "No Data")
            self.wait_monitors({"states": "Alert", "no_data": "No Data"})
            self.snapshot({"states": ("immediate", "Alert"), "no_data": ("attention", "No Data")})
            print("Datadog: live Alert and No Data reached Radar", flush=True)
            self.update_monitor("states", "Warn")
            self.wait_monitors({"states": "Warn", "no_data": "No Data"})
            active = {"states": ("attention", "Warn"), "no_data": ("attention", "No Data")}
            self.snapshot(active)
            print("Datadog: live warning preserved task IDs", flush=True)
            # Exercise the actual collector, including its static-auth guards.
            env = {**self.env, "RADAR_TEST_DATADOG_SITE": self.dd["site"], "RADAR_TEST_DATADOG_ORG": self.dd["org"],
                   "RADAR_TEST_DATADOG_QUERY": self.radar_config["datadog"]["monitor_query"]}
            command(["go", "test", "./internal/integration/datadog", "-run", "^TestLivePupOAuthCollection$", "-count=1"], env, parse=False)
            self.save_config(self.dd["org"] + "-missing-e2e-session")
            self.snapshot(active, error=True)
            self.start()
            self.snapshot(active, error=True)
            self.save_config()
            self.snapshot(active)
            print("Datadog: failed-login retention and daemon restart passed", flush=True)
        finally:
            self.save_config()
            self.recover()
        done = {k: ("done", "Recovered") for k in NAMES}
        self.snapshot(done)
        self.start()
        self.snapshot(done)



def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path, help="non-secret fixture selectors and CLI store paths")
    parser.add_argument("--report-dir", required=True, type=Path, help="only sanitized results are written here")
    parser.add_argument("--recover-only", action="store_true", help="return the two approved monitors to healthy settings")
    args = parser.parse_args()
    args.report_dir.mkdir(parents=True, exist_ok=True)
    results = []

    def check(name, operation):
        started = time.monotonic()
        row = {"name": name, "status": "passed"}
        try:
            operation()
        except Exception as error:
            row.update(status="failed", error=str(error) if isinstance(error, Failure) else "unexpected harness error; verify configuration and CLI versions")
        row["seconds"] = round(time.monotonic() - started, 2)
        results.append(row)
        print(f"{row['status'].upper()}: {name}" + (f" — {row['error']}" if "error" in row else ""), flush=True)

    # Also serialize local/manual invocations, not just GitHub workflow runs.
    # Never inspect, copy, back up or upload the CLI stores themselves.
    lock = None
    try:
        config = json.loads(args.config.read_text())
        validate(config)
        lock = args.config.with_suffix(".lock").open("a")
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Failure("another live E2E run is using these fixtures") from None
        with tempfile.TemporaryDirectory(prefix="r-e2e-") as temporary:
            suite = Suite(config, Path(temporary))
            try:
                if args.recover_only:
                    def recover():
                        suite.approve_monitors()
                        suite.recover()
                    check("Datadog monitor recovery", recover)
                else:
                    check("Jira OAuth production collection, pagination and daemon retention", suite.jira_reads)
                    check("GitHub PR references through Radar's provider", suite.github_links)
                    check("Datadog OAuth, live monitor lifecycle and daemon persistence", suite.datadog_lifecycle)
            finally:
                suite.stop()
    except Exception as error:
        check("runner configuration", lambda: (_ for _ in ()).throw(error))
    finally:
        if lock:
            lock.close()
        (args.report_dir / "results.json").write_text(json.dumps({"checks": results}, indent=2) + "\n")
        xml = ET.Element("testsuite", name="live-provider-e2e", tests=str(len(results)),
                         failures=str(sum(r["status"] == "failed" for r in results)))
        for row in results:
            case = ET.SubElement(xml, "testcase", name=row["name"], time=str(row["seconds"]))
            if row["status"] == "failed":
                ET.SubElement(case, "failure", message=row["error"])
        ET.ElementTree(xml).write(args.report_dir / "junit.xml", encoding="unicode")
    return 1 if not results or any(r["status"] == "failed" for r in results) else 0


if __name__ == "__main__":
    # GitHub cancellation normally sends SIGINT, then SIGTERM. Unwind finally
    # blocks on either so recovery is attempted before the runner kills the job.
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    raise SystemExit(main())
