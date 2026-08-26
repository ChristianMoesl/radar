# GitHub integration

GitHub supplies code-review work items through the `gh` CLI.

## Capabilities

`Source`, `StatusReporter`, `Reconciler`, `TaskFilterProvider`, `RateLimitReporter`, `WorkspaceSeedProvider`, `DevelopmentLinkResolver`, and the `CodeReviewProvider` composite role.

## Configuration and authentication

`github.filters` owns repository and actor mute/deprioritize rules. Authentication is the existing `gh auth` state. Radar does not store GitHub credentials.

## Collection and refs

Full refreshes collect review requests, authored pull requests, participation activity, and configured tracked pull requests. Refs use `github:pr:<owner/repository>:<number>` identities, directly openable URLs, provider-owned task kinds/metadata, branch linking keys, and typed acknowledgement contracts for comment/review activity.

Missing active pull requests are reconciled through GitHub and become done only after a confirmed terminal state. Partial searches and branch-resolution failures preserve complete previous observations. Core/search budgets come from `gh api rate_limit`; low budgets defer collection until reset.

## Workspace behavior

The workspace seed capability finds a matching local repository, resolves the PR head, fetches pull refs when necessary, and returns a generic existing-branch seed. Jira development links are resolved here so Jira never parses GitHub identities.

## Validation

```sh
go test ./internal/integration/github
radar rate-limit
```
