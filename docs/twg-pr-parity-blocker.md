# TWG 1.3.8: Jira PR relationship qualification blocker

Observed on 2026-10-19 using the official checksum-verified Linux amd64 binary,
a CLI-owned OAuth profile, a Jira software project and an explicitly authorized
private GitHub fixture repository. The user confirmed that GitHub for Atlassian
connects the organization and selects the repository. No credential files were
read and no tokens were exported.

This is a live qualification result, not a fixture simulation. The remaining
gap may involve CLI/backend support or graph permissions; these observations do
not establish that a particular upstream component is defective.

## Working control

The fixture set has six PRs: open, closed without merging, merged, three with no
Jira key in the title/source branch, an intentional branch-key conflict, and
controls with an unambiguous issue key. A fresh control was created after the
organization connection was confirmed, independently of initial backfill.
The user directly confirmed that this fresh control PR is visible in the Jira
issue's Development panel. This establishes the provider-side control for at
least that PR; the six opaque graph targets have not been matched individually.

Native Jira JQL now returns the fixture issue for:

```text
project = ABC AND development[pullrequests].all > 0
```

The generic context route also returns six explicit `issue_associated_pr`
targets of type `GraphPullRequest`. The IDs cannot yet be mapped to the six
GitHub fixture PRs, so matching counts alone are not proof of parity.

## Reproduction

Use an OAuth profile for a site with actual GitHub Development-panel links.
Replace the Cloud ID, issue key and graph ARI placeholders with that site's
non-secret identifiers. All commands below are reads.

Common options:

```sh
twg --profile radar --site <cloud-id> --output json --output-summary none \
  --timeout-ms 10000 --usage-counts <command>
```

### The product-specific route does not expose the observed links

```sh
twg --profile radar --site <cloud-id> -o json --output-summary none \
  context jira workitem ABC-123 --detail full --first 1 --since 36500d \
  --relationships jira_work_item_links_external_pull_request
```

It returned `data.relationships: []` and
`data.pagination: {hasNextPage: false, endCursor: null}` despite the native JQL
control and the generic graph edges. Using `issue_associated_pr` in this
product-specific route instead failed with `TWG_TOOL_EXECUTION_FAILED` and
`No relationships match the provided filters.`

### The generic route finds graph IDs without provider identities

```sh
twg --profile radar --site <cloud-id> -o json --output-summary none --usage-counts \
  context get ABC-123 --detail full --first 20 --since 36500d
```

The relevant group under `data.groups.code` was:

```json
{
  "relationshipName": "issue_associated_pr",
  "direction": "outbound",
  "targetType": "GraphPullRequest",
  "count": 6,
  "targets": [
    {
      "ari": "ari:cloud:graph::pull-request/activation/<activation-id>/<pr-id>",
      "type": ""
    }
  ],
  "provenance": {"kind": "explicit", "source": "generic-subgraph"}
}
```

The sample shows one target; all six returned targets had that same shape.
They did not carry PR URLs/numbers, repository, source branch, title or state.
Unrelated relationship groups were present in the unfiltered response and are
omitted from this excerpt.

The released PR-detail command could not hydrate a sampled returned ARI:

```sh
twg --profile radar --site <cloud-id> -o json --output-summary none \
  pull-requests get 'ari:cloud:graph::pull-request/activation/<activation-id>/<pr-id>'
```

It exited 1 with `TWG_TOOL_EXECUTION_FAILED` and `Pull request not found for ARI`.
The same command with the known private GitHub control-PR URL failed with HTTP
401, `Could not resolve URL`. This does not establish the reason for the denied
URL resolution.

`context get <returned-pr-ari> --detail full --first 1` did identify the anchor
as `GraphPullRequest` and return its inbound issue edge. The anchor still had no
provider identity. Its gaps included:

```text
No product-specific typed enrichment adapter is available for this anchor kind.
```

### Generic graph evidence cannot be cursor-paged

```sh
twg --profile radar --site <cloud-id> -o json --output-summary none --usage-counts \
  context get ABC-123 --relationships issue_associated_pr \
  --detail full --first 1 --since 36500d
```

This returned one PR target, exit 0, and:

```json
{
  "pagination": {"hasNextPage": false, "endCursor": null},
  "gaps": [
    "1 direct relationship group(s) have more matches in the scanned graph than --first 1 allows. Increase --first for more; --after does not page generic graph evidence."
  ]
}
```

The response also reported graph budgets (`maxNodes: 120`, `maxEdges: 80`). The
excerpt omits a separate typed-adapter gap. Neither a successful exit nor
`hasNextPage: false` proves completeness here; consumers must inspect gaps and
budgets. Increasing a fixed bound is not a demonstrated pagination contract.

The additional documented `subgraph get <issue-ari> --depth 1 --direction out
--edge-type issue_associated_pr --limit-nodes 200 --limit-edges 200` diagnostic
returned only the issue root and zero edges. It did not recover the six targets
seen through `context get`.

## Operational evidence

The tested generic issue-context requests and PR-anchor diagnostic each reported
two `ai-credit` usage units in `meta.billing.totalUsageCount`. The bounded
subgraph diagnostic reported seven. Earlier empty product-specific context
requests reported zero. These are per-request observations, not a pricing or
future billing guarantee; zero-cost empty responses cannot establish the cost
of polling populated associations.

## Required outcome before Radar cutover

Radar needs one canonical structured route that supplies complete, exact
issue-to-provider-PR identities. A stable PR URL or repository/PR number would
allow its existing GitHub transport to read source branches and state. The
route must expose pagination, coverage/truncation, freshness and permission
failures sufficiently to retain previous facts when a read is incomplete.

A supported CLI-owned OAuth raw request to Jira's existing Development endpoints
could also satisfy qualification if those endpoints accept the OAuth session and
return complete results. TWG 1.3.8's exposed help map has no generic `api` command;
that request contract must not be assumed.

Opaque graph IDs cannot be matched to GitHub PRs by counts, titles, branch text
or inferred ordering. Radar will not export tokens or read CLI credential files
to bridge this gap. Jira's production hard switch remains pending in
[#28](https://github.com/ChristianMoesl/radar/issues/28) and
[#27](https://github.com/ChristianMoesl/radar/issues/27).
