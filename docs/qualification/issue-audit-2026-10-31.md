# Radar issue audit — 2026-10-31

All 46 Radar issues were reviewed against the current implementation, tests and
CI evidence: 36 closed and 10 open at the start of the review. A closed issue's
historical unchecked boxes alone are not evidence of a current defect. The
remaining qualification gates below must stay open until their own evidence is
recorded.

| Issue | Implementation and remaining work |
| --- | --- |
| [#5](https://github.com/ChristianMoesl/radar/issues/5) | Guided setup now offers the official SBX installer with consent, rechecks PATH/version and retries post-login readiness after two seconds. The arrow/space picker already has no text filter. Regression tests cover installation failures, cancellation and unavailable runtime. |
| [#7](https://github.com/ChristianMoesl/radar/issues/7) | Durable-agent runtime and examples are implemented. The overall epic still depends on #10/#22. |
| [#10](https://github.com/ChristianMoesl/radar/issues/10) | Review source, editable definition, recovery, guide and real-SBX fixtures are implemented. Final POC qualification depends on #22. |
| [#22](https://github.com/ChristianMoesl/radar/issues/22) | Remaining: authenticated real GitHub review with a configured model, useful/revision-accurate findings, live example behavior and initial public host-package release. The package is not public in the npm registry; this environment has no configured model. Maintainer npm bootstrap/trusted publisher and manual stage approval remain necessary. |
| [#27](https://github.com/ChristianMoesl/radar/issues/27) | OAuth collector and host MCP implementation is present. Keep the epic open for the outstanding #28/#30/#31/#32 qualification and rollout gates. |
| [#28](https://github.com/ChristianMoesl/radar/issues/28) | Native MCP reads, populated issue/comment pagination and reconnect now pass on all three platforms. Remaining: natural expiry/server revocation, the broader collector/headless and PR-link matrix, and fresh interactive onboarding on supported platforms. |
| [#30](https://github.com/ChristianMoesl/radar/issues/30) | Install/login/verify/retry/defer and credential hard-switch paths have automated coverage. Remaining: actual missing-CLI assisted/manual setup, first-run consent, browser/TTY/callback and failure/defer qualification per platform. Fixture success is not browser qualification. |
| [#31](https://github.com/ChristianMoesl/radar/issues/31) | Host MCP migration and the three-platform live read/reconnect gate are complete. Datadog natural expiry/renewal passed on Linux amd64; Jira expiry was not observed. Jira natural expiry and provider-side revocation remain open. Dedicated CI sessions must not be reused for separate interactive tests. |
| [#32](https://github.com/ChristianMoesl/radar/issues/32) | Packaging, automated contracts and read-only rollout preflight are implemented. Release still depends on expiry/revocation and foreground platform onboarding, final diagnostic/artifact review and consistent release notes. Do not publish the breaking release based only on successful read tests. |
| [#42](https://github.com/ChristianMoesl/radar/issues/42) | Reply posting remains disabled. The supported service transport is now host MCP; the historical TWG metadata gap is not a reason to restore sandbox CLIs. Qualify native comment operation-property write/read contracts and complete pagination, then implement exact human-approved text, durable reconciliation and restart/cancellation recovery. Live writes need an explicitly authorized dedicated test grant. |

## Evidence and next qualification

- [Host-package staging](https://github.com/ChristianMoesl/radar/actions/runs/41222552495)
  passed host checks, relevant Go integration tests and the packed isolated
  installation, then npm returned `ENEEDAUTH`. No package was staged or
  published; publisher setup remains a maintainer step.
- The separate [Terraform lifecycle run](https://github.com/ChristianMoesl/radar/actions/runs/41222566350)
  reproduced a regression after its collection and acknowledgement checks
  passed. The first daemon response lacked a Terraform status; the next run
  reached restart catch-up and encountered persisted old facts. The harness now
  retries mismatches only in explicitly pending snapshots, at most three times,
  and fails clearly for non-pending or persistent mismatches. Its complete
  offline real-daemon lifecycle and both pending-snapshot regressions pass.
  #41 tracks live revalidation.
- [Normal CI for the MCP revision](https://github.com/ChristianMoesl/radar/actions/runs/41216568689) passed.
- [Selected MCP read matrix](mcp-reads-2026-10-31.json) passed on Linux amd64,
  Linux arm64 and macOS arm64. It used read-only Jira and Datadog requests,
  complete populated pagination, reopened clients and serialized CI state
  checkpoint/writeback. The earlier allowlist and session-seeding blockers are
  resolved.
- [The 448773f follow-up](https://github.com/ChristianMoesl/radar/actions/runs/41223219100)
  passed again on all three platforms. Datadog's token was expired before the
  Linux amd64 reads and its renewed future expiry was saved. Jira's token was
  not expired; its natural-expiry check was explicitly skipped. No timestamps
  were edited and no grant was revoked.
- [Collector evidence](oauth-collectors-2026-10-31.json) records the separately
  qualified live behavior and its exclusions. The independent MCP run does not
  establish a full collector CI pass.
- [CLI cutover](../cli-oauth-migration.md), [host MCP setup](../agent-mcp.md) and
  [dedicated live CI setup](../mcp-live.md) describe the current boundaries.

The MCP workflow can qualify natural expiry when its own stored access token is
already expired: it observes the expiry before any read and requires renewed
future expiry after reads/reconnect. It skips this check when expiry was not
observed. Use a separate disposable OAuth grant for manual expiry/revocation
qualification; do not revoke or refresh copies of the CI-owned grant. Revoke
only the disposable grant through its provider and prove
that background calls fail without opening a browser, leaking diagnostics or
discarding prior facts. Do not edit token timestamps to claim natural expiry.
Fresh onboarding needs a real terminal/browser on each supported platform;
record CLI versions, install choice, verified identity/site and retry/defer
outcomes without saving credentials or callback URLs in the report.

This audit changes no persisted schema, credentials, grants, task state or
workspace layout. It does not authorize a package release or provider writes.
