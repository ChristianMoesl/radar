# Historical SBX OAuth proxy investigation

This experiment was superseded on 2026-10-31 when the user selected host MCP for
remote agent service access. Provider CLIs are no longer installed in Radar's
default sandbox image. The diagnostic workflow/scripts were removed; the complete
reproduction remains in [commit b2ff7ce](https://github.com/ChristianMoesl/radar/tree/b2ff7ce).
It is not a current OAuth migration release gate. See [agent MCP setup and
qualification](agent-mcp.md) for the current architecture.

## Evidence retained

- [Datadog inside SBX, 39518844101](https://github.com/ChristianMoesl/radar/actions/runs/39518844101): native login and approved monitor reads passed; refresh failed with HTTP 403, and restart/isolation were not qualified.
- [Outside-SBX control, 39572225254](https://github.com/ChristianMoesl/radar/actions/runs/39572225254): the same Pup executable passed login, reads and two native refreshes on a separate fresh grant.
- [Local public-client reproduction, 39578587979](https://github.com/ChristianMoesl/radar/actions/runs/39578587979): SBX added a bearer to token-endpoint requests that the client sent without Authorization. The synthetic endpoint rejected it; this does not establish Datadog's reason for its earlier 403.
- The [upstream report draft](sbx-oauth-header-report.md) was prepared but not submitted to Docker.

No real tokens, CLI credential files, callbacks or private logs are in this
report. The change to MCP does not turn these failed or incomplete experiments
into passing qualifications. Host MCP provider login, scopes, refresh/revocation,
completeness and reconnect require their own evidence.
