# TWG/Pup through the SBX OAuth credential proxy

This opt-in test qualifies SBX **0.49.1**, TWG **1.3.8** and Pup **2.0.0** on a
disposable Linux amd64 GitHub runner with KVM. It does not change the production
kit or adapters. Results are characterization evidence, not assumed compatibility.

The isolated v2 kit declares one OAuth service and its exact token endpoint/API
hosts. `passthrough` is false, with explicit access/refresh sentinels. There is
no static-key fallback, host token export, credential-store copy, generated CLI
credential file or host execution fallback. Native login creates the CLI's own
profile; SBX is expected to mask token responses and keep real tokens on the host.

The test requires an empty sandbox inventory and no prior credential bindings.
It writes only a non-secret binding on the disposable runner. Never run it in a
normal user HOME or against existing sandboxes. The normal global policy remains
`balanced`; the kit declares the provider hosts. Docker Hub access uses the same
repository secrets as the existing sandbox runtime workflow.

## Start a test

Dispatch [SBX OAuth proxy qualification](../.github/workflows/sbx-oauth-proxy.yml)
on trusted `main`, operation `test`, provider `twg` or `pup`. Supply non-secret
approved selectors as the `configuration` JSON string:

```json
{"base_url":"https://example.atlassian.net","cloud_id":"discovered-cloud-id","account_id":"verified-account-id","issue_keys":["ABC-123"]}
```

For Pup:

```json
{"site":"datadoghq.eu","monitor_query":"tag:radar-oauth-e2e"}
```

The immutable Radar image is pinned to the previously tested publication. The
test grants Pup only `monitors_read`, performs Jira issue/monitor reads, tests
native refresh and stops/restarts its own sandbox. It changes no issues,
comments or monitors and revokes no remote grants.

Operation `diagnose` uses the same disposable kit without starting native login.
It sends one negative refresh request using only a dummy public client and the
known sentinel, checks network authorization, and summarizes the token host's
SBX policy log. Its `diagnosed` status is not an OAuth qualification pass. A
failed dummy refresh alone cannot establish the forwarding route; a matching
policy-log entry is needed. Output contains only fixed routing/decision labels, booleans
and an HTTP status. Provider bodies, policy rule text and raw logs stay private.

## Compare Pup outside SBX

Operation `control`, provider `pup`, compares native OAuth refresh outside the
SBX proxy on the same kind of disposable GitHub runner. It copies only the pinned
Pup executable from a fresh sandbox and removes that sandbox **before login**.
Pup then creates a new native profile in an isolated runner directory. No live
credential store or token is copied into or out of a sandbox. Proxy/CA settings
remain inherited, while static credential environment variables are excluded.
This is an explicit diagnostic test, not a product execution fallback.

Approve a fresh login and deliver the encrypted callback as below; the control
forwards it to the host's native Pup listener over stdin. It verifies OAuth and
approved monitor reads, performs native refresh **before** the dummy-token probe,
then refreshes and reads again **after** that probe. This checks whether the
negative probe interferes with a working native session. Its `control_passed`
status never qualifies SBX credential isolation, masking or restart.

## Approve login

Download `sbx-oauth-challenge-<run-id>` as soon as its upload step finishes.
`challenge.json` contains only the provider authorization URL, run ID and, for
Pup, an ephemeral public key. Native diagnostics stay on the private runner.
Open the URL and approve the intended account. TWG uses device authorization.
The test declines TWG's question about opening a browser inside the sandbox and
publishes its challenge only after native device-token polling starts. The
foreground TWG login clears sandbox agent-attribution metadata and sets
`TWG_AGENT_HOST=none` to select its normal interactive
**five-minute** window. Later headless reads keep their regular environment.
The observed native deadline is included as `approval_window_seconds` in the
challenge. Coordinate browser availability before starting the run; approving
after the native CLI exits cannot resume it. No provider grant or other
confirmation is answered automatically.
The same sanitized challenge is published as a neutral `SBX OAuth approval`
check on the tested commit, readable through GitHub's check-runs API while the
workflow is waiting. Private logs and credential values are never check output.

Pup redirects to `http://127.0.0.1:8000/oauth/callback?...`, which cannot reach
the remote runner. Return that URL through the encrypted callback channel:

1. Save `callback_public_key` from the challenge as a local public-key file.
2. Feed the callback URL through stdin to
   `node scripts/sbx-oauth-callback.mjs seal /path/to/public-key <run-id>`.
   Keep the plaintext out of shell arguments/history and CI inputs.
3. Dispatch the same workflow with operation `callback`, provider `pup`,
   `target_run` equal to the original run ID and `encrypted_callback` containing
   only the sealed output. The original runner decrypts it with its private key,
   verifies the localhost destination and OAuth state, and forwards it to Pup's
   loopback callback listener through `sbx exec` stdin. No network port is
   published, and the callback is never a process argument. Other runs cannot
   decrypt the packet.

No password, access token, refresh token or credential file is requested. The
authorization callback is short lived; do not paste it into a GitHub issue or
plaintext workflow input. Login has a bounded approval window. An expired test
must start a new login; it never reuses a captured grant from another runner.

## Assertions and artifacts

`sbx-oauth-result-<run-id>` contains only status and completed check names:

- Native OAuth login and the configured identity/site/profile.
- Actual reads of the approved provider fixtures.
- A token-endpoint request using the known refresh sentinel returns the expected
  sentinels, using the native public OAuth client ID and printing no token values.
  A real token or missing response fails. Probe failures expose only a fixed error
  category and HTTP status, with the failed verification step and completed checks.
- The native CLI can refresh, then repeat identity/read/masking checks.
- The same checks pass after stopping/restarting the sandbox.

A masking failure remains a failed qualification. The worker records it per
phase and continues the independent native refresh/restart checks; successful
later operations cannot erase an earlier masking failure.

## Observed characterization

[Datadog run 39518844101](https://github.com/ChristianMoesl/radar/actions/runs/39518844101)
on SBX 0.49.1 / Pup 2.0.0 completed native OAuth login, verified the configured
site/session/scope, and read the approved tagged monitors. The refresh-sentinel
probe, including the native public client ID, returned **HTTP 403**. Credential
isolation is unverified and the qualification failed. The worker retained that
failure and attempted native `pup auth refresh`, which also failed with **HTTP
403**. Sandbox restart was not reached. This result blocks qualification of the
tested SBX/Pup combination; it does not establish which component issued the
403. Pup's v2.0.0 DCR implementation
uses the same three form fields (`grant_type`, `client_id`, `refresh_token`),
without client-secret authentication.

[Jira run 39516056168](https://github.com/ChristianMoesl/radar/actions/runs/39516056168)
reached native TWG device-token polling with a confirmed five-minute window,
then expired without a completed token exchange. Jira inside SBX is unqualified.

No-login routing diagnostics for
[Pup](https://github.com/ChristianMoesl/radar/actions/runs/39520010751) and
[TWG](https://github.com/ChristianMoesl/radar/actions/runs/39520014320) found network
authorization allowed and dummy refresh returned HTTP 502. Neither produced a
matching policy-log entry, so these runs did not establish the forwarding route.

Only challenge/result JSON and encrypted callback packets are uploaded. No native
logs, CLI files, host keychain, private callback key, environment dump or provider
response is an artifact. The sandbox is removed after testing and the disposable
runner is destroyed. Tokens remain caller-authorized test grants; no server-side
revocation is performed. Natural expiry and daemon/host restart are separate
checks. Failure proves a compatibility gap; it must not weaken OAuth checks or
enable token passthrough automatically.
