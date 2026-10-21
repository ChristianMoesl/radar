#!/usr/bin/env python3
"""Read-only TWG/Pup qualification through the real SBX OAuth proxy.

Run only on a disposable host. Provider CLIs create their own sandbox profiles;
the kit intercepts token responses. Never read/copy credential files or export a
host access token. Logs stay private; artifacts contain challenges/results only.
"""
import argparse
import io
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
from urllib.parse import parse_qs, urlparse
import zipfile

ROOT = Path(__file__).resolve().parent.parent
BRIDGE = ROOT / "scripts/sbx-oauth-callback.mjs"
LOGIN = ROOT / "scripts/sbx-oauth-login.py"
ACCESS = "radar-proxy-access-sentinel"
REFRESH = "radar-proxy-refresh-sentinel"
PROFILE = "radar-proxy-test"


class Failure(Exception):
    pass


def require(value, message):
    if not value:
        raise Failure(message)


def run(argv, *, body=None, timeout=90, parse=False):
    try:
        result = subprocess.run(argv, input=body, text=True, capture_output=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise Failure(f"{Path(argv[0]).name} could not complete within its deadline") from None
    hints = diagnostics(result.stdout + result.stderr) if result.returncode else []
    require(result.returncode == 0, f"{Path(argv[0]).name} failed (exit {result.returncode}; {','.join(hints) or 'unclassified'}); private diagnostics withheld")
    if not parse:
        return result.stdout
    try:
        return json.loads(result.stdout)
    except ValueError:
        raise Failure("Invalid structured response; private output withheld") from None


def validate(provider, config):
    if provider == "twg":
        site = urlparse(config["base_url"])
        require(site.scheme == "https" and site.hostname and not site.username and not site.password
                and site.path in ("", "/") and not site.query and not site.fragment, "Select an HTTPS Jira site")
        require(re.fullmatch(r"[A-Za-z0-9-]+", config["cloud_id"]) and config["account_id"], "Select Jira Cloud ID/account")
        require(config["issue_keys"] and all(re.fullmatch(r"[A-Z][A-Z0-9_]*-[1-9][0-9]*", k)
                for k in config["issue_keys"]), "Select approved Jira issue keys")
    else:
        require(config["site"] == "datadoghq.eu", "This qualification selects the approved Datadog EU site")
        require(config["monitor_query"], "Select the approved test-monitor query")


def kit(provider, config, image):
    require(re.fullmatch(r"docker.io/christianmoesl/radar-sandbox(?:@sha256:[a-f0-9]{64}|:[A-Za-z0-9_.-]+)", image),
            "Use the trusted Radar sandbox image")
    if provider == "twg":
        endpoint = {"host": "auth.atlassian.com", "path": "/oauth/token"}
        resources = ["api.atlassian.com", "teamwork-graph.atlassian.com"]
        network = [*resources, endpoint["host"], "id.atlassian.com", urlparse(config["base_url"]).hostname]
    else:
        endpoint = {"host": "api.datadoghq.eu", "path": "/api/v2/oauth2/token"}
        resources = ["api.datadoghq.eu"]
        network = [*resources, "app.datadoghq.eu"]
    return {"schemaVersion": "2", "kind": "sandbox", "name": "radar-oauth-proxy-test",
            "sandbox": {"image": image, "entrypoint": ["bash"]},
            "permissions": {"network": {"allow": sorted(set(network))}},
            "credentials": [{"service": "radar-" + provider, "required": True, "oauth": {
                "tokenEndpoint": endpoint, "resourceHosts": resources,
                "sentinels": {"accessToken": ACCESS, "refreshToken": REFRESH}, "passthrough": False}}]}


def challenge(provider, text):
    # Only the provider's authorization URL leaves private native CLI logs.
    text = re.sub(r"\x1b\[[0-9;]*m", "", text)
    pattern = (r'''https://auth\.atlassian\.com/oauth/activate\?[^\s\x1b"'<>]+''' if provider == "twg" else
               r'''https://app\.datadoghq\.eu/oauth2/v1/authorize\?[^\s\x1b"'<>]+''')
    match = re.search(pattern, text)
    return match.group(0) if match else None


def callback_url(value, authorization):
    require(len(value) < 8192 and not any(ord(c) < 32 for c in value), "Invalid callback")
    parsed, expected = urlparse(value), parse_qs(urlparse(authorization).query)
    query = parse_qs(parsed.query)
    require(parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port == 8000
            and parsed.path == "/oauth/callback" and not parsed.username and not parsed.password
            and query.get("state") == expected.get("state") and query.get("state")
            and (query.get("code") or query.get("error")), "Callback does not match this login")
    return value


def write(path, value):
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.replace(path)


def diagnostics(value):
    # Fixed labels only: provider bodies/headers/URLs never escape.
    patterns = {"terminal_required": r"not a tty|interactive terminal|is not a terminal|stdin.*closed",
                "image_pull": r"pull access denied|failed to pull|manifest unknown|failed to resolve reference",
                "network": r"connection refused|network.*denied|network.*unreachable|network timeout|connection timed out",
                "credential_binding": r"binding.*(?:missing|denied|not approved)|credential.*(?:missing|unavailable|denied)",
                "authorization_expired": r"expired_token|device code.*expired|authorization.*expired",
                "authorization_denied": r"access_denied|authorization.*denied",
                "login_timeout": r"login\s+timed\s+out|login_timeout",
                "grant_rejected": r"invalid_grant|invalid_client",
                "http_401": r"HTTP 401|\(401\)", "http_403": r"HTTP 403|\(403\)",
                "http_404": r"HTTP 404|\(404\)", "http_422": r"HTTP 422|\(422\)",
                "http_5xx": r"HTTP 5[0-9]{2}",
                "option": r"unknown flag|unknown option|unexpected argument",
                "auth_site": r"AUTH_SITE_UNRESOLVED", "auth_profile": r"AUTH_PROFILE_MISSING"}
    return [name for name, pattern in patterns.items() if re.search(pattern, value, re.I)]


def login_progress(value):
    value = ' '.join(re.sub(r'\x1b\[[0-9;?]*[A-Za-z]', '', value).split())
    return {"polling_started": "Waiting for authentication in browser" in value or "Waiting for browser authentication" in value,
            "token_exchange_completed": "Authentication successful!" in value,
            "profile_saved": "Saved profile " in value,
            "native_timeout": "Login timed out after " in value}


def approval_window_seconds(value):
    # TWG's interactive progress reports its native deadline. This is metadata,
    # never a change to the provider's authorization or token lifetime.
    value = re.sub(r'\x1b\[[0-9;?]*[A-Za-z]', '', value)
    remaining = [int(n) for n in re.findall(r'\((\d+)s remaining\)', value)]
    return max(remaining) if remaining and 0 < max(remaining) <= 300 else None


def publish(directory, value):
    # Check output is immediately readable through GitHub's API, including when
    # this execution environment cannot download Actions artifact blobs.
    path = directory / "check-id"
    output = {"title": "SBX OAuth proxy " + ("approval" if "authorization_url" in value else "result"),
              "summary": json.dumps(value)}
    if path.exists():
        run(["gh", "api", "--method", "PATCH", f"repos/{os.environ['GITHUB_REPOSITORY']}/check-runs/{path.read_text()}",
             "--input", "-"], body=json.dumps({"output": output}))
    else:
        result = run(["gh", "api", "--method", "POST", f"repos/{os.environ['GITHUB_REPOSITORY']}/check-runs", "--input", "-"],
                     body=json.dumps({"name": "SBX OAuth approval " + os.environ["GITHUB_RUN_ID"],
                                      "head_sha": os.environ["GITHUB_SHA"], "external_id": os.environ["GITHUB_RUN_ID"],
                                      "status": "completed", "conclusion": "neutral", "output": output}), parse=True)
        path.write_text(str(result["id"]))


def sbx(name, *args, timeout=90, body=None, parse=False):
    return run(["sbx", "exec", "-i", "-e", "TWG_CONFIG_DIR=/home/agent/.config/radar-proxy-twg",
                "-e", "DD_TOKEN_STORAGE=file", "-e", "PUP_CONFIG_DIR=/home/agent/.config/radar-proxy-pup",
                "-e", "BROWSER=/bin/false", name, *args], timeout=timeout, body=body, parse=parse)


# This private probe uses known sentinels, never obtains a host access token,
# and returns only boolean assertions. It rejects a real token in the response.
MASK_PROBE = r'''
import json,ssl,sys,urllib.error,urllib.request,urllib.parse
r=json.loads(sys.argv[1]); body=urllib.parse.urlencode({"client_id":r["client_id"],"grant_type":"refresh_token","refresh_token":r["refresh"]}).encode()
request=urllib.request.Request(r["endpoint"],data=body,headers={"Content-Type":"application/x-www-form-urlencoded"})
try:
 with urllib.request.urlopen(request,timeout=20) as response: data=json.load(response)
 masked=data.get("access_token")==r["access"] and data.get("refresh_token")==r["refresh"]
 print(json.dumps({"sentinel_response":masked,"failure":None if masked else "sentinel_mismatch"}))
except urllib.error.HTTPError as error:
 print(json.dumps({"sentinel_response":False,"failure":"http","http_status":error.code}))
except urllib.error.URLError as error:
 print(json.dumps({"sentinel_response":False,"failure":"tls" if isinstance(error.reason,ssl.SSLError) else "network"}))
except Exception:
 print(json.dumps({"sentinel_response":False,"failure":"response_format"}))
'''


def public_client_id(provider, authorization):
    # Public client identifier from the pinned TWG release / native Pup URL.
    if provider == "twg":
        return "zjAE9OPtU9ercrK02sZowEPwoPlrRPTg"
    values = parse_qs(urlparse(authorization or "").query).get("client_id", [])
    require(len(values) == 1 and re.fullmatch(r'[A-Za-z0-9_-]{1,128}', values[0]), "Native public OAuth client ID missing")
    return values[0]


def verify(provider, cfg, name, spec, report, client_id):
    report["verification_step"] = "oauth_identity"
    report["verified"] = {"oauth_identity": False, "approved_fixture_read": False, "sentinel_token_response": False}
    if provider == "twg":
        prefix = ["twg", "--profile", PROFILE, "--site", cfg["cloud_id"], "-o", "json", "--output-summary", "none"]
        listing = sbx(name, *prefix, "auth", "profile", "list", parse=True)
        selected = [p for p in listing["data"]["profiles"] if p["name"] == PROFILE]
        require(len(selected) == 1 and selected[0]["authMethod"] == "oauth"
                and selected[0]["cloudId"] == cfg["cloud_id"] and selected[0]["userId"] == cfg["account_id"],
                "TWG OAuth site/account mismatch")
        doctor = sbx(name, *prefix, "doctor", parse=True)["data"]
        require(doctor["auth"]["resolved"]["authMethod"] == "oauth" and doctor["connectivity"]["ok"]
                and doctor["connectivity"]["userId"].endswith("/" + cfg["account_id"]), "TWG OAuth connectivity failed")
        report["verified"]["oauth_identity"] = True
        report["verification_step"] = "approved_fixture_read"
        for key in cfg["issue_keys"]:
            result = sbx(name, *prefix, "jira", "workitem", "get", key, "--fields", "summary,status", "--strict", parse=True)
            require(result.get("command") == "jira.workitem.get" and len(result["data"]) == 1
                    and result["data"][0]["key"] == key and not result.get("error"), "Native Jira fixture read failed")
    else:
        prefix = ["pup", "--org", PROFILE, "--no-envelope", "--output", "json"]
        status = sbx(name, *prefix, "auth", "status", parse=True)
        require(status.get("authenticated") and status.get("has_refresh") and not status.get("auth_method")
                and status.get("token_type", "").lower() == "bearer" and status.get("site") == cfg["site"]
                and status.get("org") == PROFILE and "monitors_read" in status.get("scopes", []), "Pup OAuth session unavailable")
        report["verified"]["oauth_identity"] = True
        report["verification_step"] = "approved_fixture_read"
        monitors = sbx(name, *prefix, "api", "/api/v1/monitor/search", "-f", "query=" + cfg["monitor_query"], parse=True)
        require(monitors.get("monitors") and all("radar-oauth-e2e" in m.get("tags", [])
                for m in monitors["monitors"]), "Expected approved tagged monitors")
    report["verified"]["approved_fixture_read"] = True
    report["verification_step"] = "token_response_masking"
    endpoint = spec["credentials"][0]["oauth"]["tokenEndpoint"]
    result = sbx(name, "python3", "-c", MASK_PROBE, json.dumps({"endpoint": "https://" + endpoint["host"] + endpoint["path"],
                 "access": ACCESS, "refresh": REFRESH, "client_id": client_id}), parse=True)
    require(result.get("sentinel_response") is True, "Proxy token-response masking is unverified (" +
            str(result.get("failure")) + "; HTTP " + str(result.get("http_status")) + ")")
    report["verified"]["sentinel_token_response"] = True
    report.pop("verification_step", None)


def verify_phase(provider, cfg, name, spec, report, client_id):
    try:
        verify(provider, cfg, name, spec, report, client_id)
    except Failure as error:
        # Keep this qualification failed, but characterize independent native
        # refresh/restart behavior without requiring more browser approvals.
        if report.get("verification_step") != "token_response_masking":
            raise
        report.setdefault("masking_failures", []).append({"phase": report["phase"], "error": str(error)})


def relay(directory, authorization, name):
    repository, run_id = os.environ["GITHUB_REPOSITORY"], os.environ["GITHUB_RUN_ID"]
    artifact_name = "sbx-oauth-callback-" + run_id
    result = run(["gh", "api", f"repos/{repository}/actions/artifacts?name={artifact_name}"], parse=True)
    for artifact in sorted(result["artifacts"], key=lambda a: a["id"], reverse=True):
        if artifact.get("expired"):
            continue
        try:
            require(artifact.get("size_in_bytes", 65536) < 65536, "Oversized callback artifact")
            binary = subprocess.run(["gh", "api", f"repos/{repository}/actions/artifacts/{artifact['id']}/zip"],
                                    capture_output=True, timeout=30, check=True).stdout
            require(len(binary) < 65536, "Oversized callback packet")
            with zipfile.ZipFile(io.BytesIO(binary)) as archive:
                require(archive.getinfo("callback.txt").file_size <= 32768, "Oversized callback packet")
                packet = archive.read("callback.txt").decode()
            plain = run(["node", str(BRIDGE), "open", str(directory / "callback.key"), run_id], body=packet)
            value = callback_url(plain, authorization)
            # Native Pup owns the callback listener. Forward over exec stdin,
            # so neither the callback nor its code appears in process arguments.
            forward = "import sys,urllib.request; r=urllib.request.urlopen(sys.stdin.read().strip(),timeout=15); assert r.status==200"
            sbx(name, "python3", "-c", forward, body=value, timeout=30)
            return True
        except Exception:
            # Never echo decrypted callback URLs or untrusted artifact contents.
            continue
    return False


def worker(directory):
    state = json.loads((directory / "state.json").read_text())
    provider, cfg, name = state["provider"], state["config"], state["sandbox"]
    report = {"provider": provider, "status": "failed", "sbx_version": "0.49.1", "checks": []}
    child = None
    try:
        spec = state["kit"]
        report["phase"] = "validate_kit"
        (directory / "kit").mkdir()
        write(directory / "kit/spec.yaml", spec)
        run(["sbx", "kit", "validate", "--offline", str(directory / "kit")])
        report["phase"] = "create_sandbox"
        run(["sbx", "create", "--name", name, "--no-progress", str(directory / "kit")], timeout=300)
        report["phase"] = "native_login"
        args = (["twg", "login", "--new-profile", PROFILE, "--site", cfg["cloud_id"]] if provider == "twg" else
                ["pup", "auth", "login", "--site", cfg["site"], "--org", PROFILE, "--scopes", "monitors_read", "--callback-port", "8000"])
        # Both native flows receive approval out of band. Keep the sandbox-owned
        # terminal, but close host stdin so exec cannot wait on an unused pipe.
        argv = ["sbx", "exec", "-e", "TWG_CONFIG_DIR=/home/agent/.config/radar-proxy-twg",
                "-e", "TWG_BACKGROUND_UPDATE_CHECK=0", "-e", "TWG_AGENT_HOST=none", "-e", "DD_TOKEN_STORAGE=file",
                "-e", "PUP_CONFIG_DIR=/home/agent/.config/radar-proxy-pup", "-e", "BROWSER=/bin/false", name,
                "python3", "-c", LOGIN.read_text(), *args]
        with (directory / "native-login.log").open("w") as log:
            child = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=log, stderr=log, text=True)
            deadline, authorization, delivered = time.monotonic() + 600, None, False
            last_progress = None
            while child.poll() is None and time.monotonic() < deadline:
                if authorization is None:
                    authorization = challenge(provider, (directory / "native-login.log").read_text())
                    # TWG displays the URL before its local-browser question.
                    # Do not ask the user until that question is answered and
                    # the native CLI has actually entered device-token polling.
                    console = (directory / "native-login.log").read_text()
                    window = approval_window_seconds(console)
                    if authorization and (provider != "twg" or (login_progress(console)["polling_started"] and window == 300)):
                        value = {"provider": provider, "run_id": os.environ["GITHUB_RUN_ID"],
                                 "authorization_url": authorization}
                        if provider == "twg" and window:
                            value["approval_window_seconds"] = window
                        if provider == "pup":
                            value["callback_public_key"] = (directory / "callback.key.pub").read_text()
                        write(directory / "challenge.json", value)
                        publish(directory, value)
                    else:
                        authorization = None
                progress = login_progress((directory / "native-login.log").read_text()) if provider == "twg" else None
                if progress and progress != last_progress:
                    report["login_progress"] = progress
                    if authorization:
                        value["login_progress"] = progress
                        publish(directory, value)
                    last_progress = progress
                if provider == "pup" and authorization and not delivered:
                    try:
                        delivered = relay(directory, authorization, name)
                        report.pop("callback_delivery_error", None)
                    except Failure as error:
                        report["callback_delivery_error"] = str(error)
                time.sleep(3)
            final_console = (directory / "native-login.log").read_text()
            hints = diagnostics(final_console)
            if provider == "twg":
                report["login_progress"] = login_progress(final_console)
            report["native_exit_code"] = child.poll()
            require(child.poll() is not None, "Native CLI login approval window timed out after 600 seconds; start a new login")
            require(not report.get("login_progress", {}).get("native_timeout"),
                    "Native CLI device approval deadline expired; start a new login")
            require(child.poll() == 0, "Native CLI login did not complete (" + ','.join(hints) + "); private diagnostics withheld")
        report["checks"].append("native_cli_login")
        client_id = public_client_id(provider, authorization)
        report["phase"] = "identity_read_and_masking"
        verify_phase(provider, cfg, name, spec, report, client_id)
        report["checks"].append("native_oauth_identity_and_read")
        report["phase"] = "refresh"
        report["verification_step"] = "native_refresh"
        if provider == "twg":
            sbx(name, "twg", "--profile", PROFILE, "--site", cfg["cloud_id"], "auth", "refresh", "--force")
        else:
            sbx(name, "pup", "--org", PROFILE, "auth", "refresh")
        verify_phase(provider, cfg, name, spec, report, client_id)
        report["checks"].append("explicit_refresh")
        report["phase"] = "sandbox_restart"
        report["verification_step"] = "sandbox_stop"
        run(["sbx", "stop", name])
        verify_phase(provider, cfg, name, spec, report, client_id)
        report["checks"].append("sandbox_restart")
        if report.get("masking_failures"):
            report["phase"] = "token_response_masking"
            raise Failure("Proxy token-response masking failed; native refresh/restart evidence retained")
        report["checks"].append("sentinel_token_response")
        report["status"] = "passed"
        report["phase"] = "completed"
    except Exception as error:
        report["error"] = str(error) if isinstance(error, Failure) else "Unexpected qualification failure; private details withheld"
    finally:
        if child is not None and child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.kill()
        try:
            run(["sbx", "rm", "--force", name])
        except Failure:
            report.update(status="failed", cleanup="Disposable sandbox cleanup failed")
        write(directory / "result.json", report)
        try:
            publish(directory, report)
        except Failure:
            pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=["prepare", "worker", "wait"])
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--provider", choices=["twg", "pup"])
    parser.add_argument("--config", type=Path)
    parser.add_argument("--image")
    args = parser.parse_args()
    os.umask(0o077)
    directory = args.directory.resolve()
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("RADAR_SBX_PROXY_DISPOSABLE") == "1",
            "Use the dedicated disposable GitHub workflow")
    require(directory.is_relative_to(Path(os.environ["RUNNER_TEMP"]).resolve()), "Use the runner's private temporary directory")
    if args.operation == "worker":
        worker(directory)
    elif args.operation == "prepare":
        require(not directory.exists(), "Test directory must be new")
        cfg = json.loads(args.config.read_text())
        validate(args.provider, cfg)
        spec = kit(args.provider, cfg, args.image)
        inventory = run(["sbx", "ls", "--json"], parse=True)
        require(inventory in ([], {"sandboxes": []}), "Use a host without existing sandboxes")
        bindings = Path.home() / ".config/sbx/credentials.yaml"
        require(not bindings.exists(), "Use a host without existing credential bindings")
        bindings.parent.mkdir(parents=True, exist_ok=True)
        oauth = spec["credentials"][0]["oauth"]
        write(bindings, {"bindings": {"radar-" + args.provider: {"oauth": {
            "domains": sorted(set([oauth["tokenEndpoint"]["host"], *oauth["resourceHosts"]]))}}}})
        directory.mkdir(mode=0o700)
        if args.provider == "pup":
            run(["node", str(BRIDGE), "keygen", str(directory / "callback.key"), os.environ["GITHUB_RUN_ID"]])
        write(directory / "state.json", {"provider": args.provider, "config": cfg, "kit": spec,
                                        "sandbox": "radar-proxy-" + os.environ["GITHUB_RUN_ID"]})
        with (directory / "worker.log").open("w") as log:
            subprocess.Popen([sys.executable, __file__, "worker", "--directory", str(directory)],
                             stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
        deadline = time.monotonic() + 330
        while time.monotonic() < deadline:
            if (directory / "challenge.json").exists():
                print("Login challenge prepared; approve using the private test artifact")
                return 0
            if (directory / "result.json").exists():
                print((directory / "result.json").read_text())
                raise Failure("Test stopped before authorization; inspect sanitized result")
            time.sleep(2)
        raise Failure("Sandbox login challenge was not ready within its deadline")
    else:
        deadline = time.monotonic() + 900
        while time.monotonic() < deadline:
            if (directory / "result.json").exists():
                result = json.loads((directory / "result.json").read_text())
                print(json.dumps(result))
                return 0 if result["status"] == "passed" else 1
            time.sleep(3)
        raise Failure("Proxy qualification exceeded its deadline")
    return 0


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(Failure("Proxy test cancelled")))
    try:
        raise SystemExit(main())
    except Failure as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
