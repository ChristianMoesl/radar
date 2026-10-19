# GitHub integration

GitHub supplies code-review work items through the `gh` CLI.

## Getting started

GitHub integration uses the GitHub CLI. Its main GraphQL request includes the current viewer and runs concurrently with explicit tracked-PR collection. Make sure authentication works first:

```sh
gh auth status
```

Radar currently tracks:

- outstanding direct and team PR review requests as `needs attention`
- open PRs authored by you as `in progress`

Radar checks GitHub rate limits before collection. When a budget is low, Radar pauses GitHub collection until GitHub's reset time. TUI and CLI status reads use cached daemon state and do not trigger GitHub requests.

## Capabilities

`Source`, `StatusReporter`, `Reconciler`, `TaskFilterProvider`, `RateLimitReporter`, `WorkspaceSeedProvider`, `BoundSourceResolver`, `DevelopmentLinkResolver`, and the `CodeReviewProvider` composite role.

## Configuration and authentication

Authentication is the existing `gh auth` state. Radar does not store GitHub credentials. Omitted `github.enabled` activates collection when `gh` is installed and has a locally configured token; `false` skips collection and reconciliation, and `true` reports missing prerequisites as an error. Expired credentials and API failures are errors, not missing prerequisites.

Tracking and presentation use three independent lists:

- `github.track`: additional open PR discovery. Each entry requires `repos` and optionally exact `authors`. Omit authors to collect everyone's open PRs, including drafts. Repository patterns require a concrete owner, e.g. `acme/platform-*`, not `*/platform`. Entries are additive.
- `github.pull_request_rules`: match `repos`, `authors`, or both on **the same PR**. Actions: `keep`, `mute`, `deprioritize`.
- `github.activity_rules`: match `repos`, `actors`, or both for relevant comment/review activity. Actions: `keep`, `ignore`.

Rule lists use first-match precedence independently per PR/actor. Place specific exceptions before broad policies. Fields use AND, list values use OR; omitted selectors are unrestricted, but at least one nonempty selector is required. Rules can have a descriptive `name`. Rule patterns are case-insensitive and support `*`. Confirmed bots match `name` and `name[bot]`; humans do not acquire bot aliases. Unknown keys, actions, empty patterns and unsupported tracking scopes fail validation.

Fresh configuration contains empty lists, explanatory comments and commented-out examples. Setup preserves hand-authored comments and never rewrites an unchanged document merely to add guidance.

## Reviews after merge

By default, merging a PR completes its GitHub contribution. Opt specific
repositories into post-merge review work when the team's workflow still requires
requested reviews after merge:

```yaml
github:
  review_requests:
    include_merged_pull_requests: ["acme/team-*", "acme/shared-library"]
    merged_pull_request_max_age_days: 30
```

`include_merged_pull_requests` is a repository-pattern list, not a boolean. It is
empty by default. Patterns are case-insensitive, support `*` in repository names,
and require a concrete owner, just like `github.track`.

`merged_pull_request_max_age_days` defaults to **30** and accepts positive whole
days (1–36500). The cutoff is measured from the merge timestamp, not PR creation,
last activity, or the review-request timestamp. It applies to **both discovery and
already-tracked reviews**: at the cutoff an outstanding merged review stops
keeping the task active, even if GitHub still requests it. A request on an older
merged PR is therefore intentionally outside this policy. Increasing the cutoff
can bring such work back into scope. Open PRs are unaffected.

Within the configured scope and age, a merged PR with a current direct or team
review request contributes `attention`, with **merged — review requested**.
Submitting a review for the current request or withdrawing that request completes
that review contribution; an earlier review does not satisfy a later re-request.
A retained request that a later submitted review already satisfied does not keep
work active. Requests on the viewer's own PR do not create self-review work.
Closed-but-unmerged PRs remain done.

This uses the existing task model, not a separate review inbox. An outstanding
review can reopen a linked authored task and delays automatic workspace cleanup.
The remote PR remains merged and Jira is not reopened. Once a tracked review is
completed or expires, its new completion observation starts the cleanup grace
period rather than backdating it to merge. Other active linked work still keeps
the task open. Acknowledging activity does not complete a requested review, and
existing mute/deprioritization rules retain their normal semantics.

Merged review searches are paginated and scoped to configured repository owners,
then matched against the repository patterns. They do not expand authored-PR or
explicit tracking discovery to all merged PRs, or subscribe to unrelated
historical discussions. Search truncation (including GitHub's 1,000-result search
ceiling), incomplete review facts, and API failures retain previous observations
and report incomplete collection instead of implying completion. Cached Done
facts are rechecked while a bound PR is eligible for post-merge reviews.

Enabling this policy can surface older requests within the age limit. Preview
matching requests before rollout if the team has an existing backlog; Radar does
not automatically change configuration, clear requests, or reset cached tasks.

## Contribution, not whole-task policy

PR policy changes only the matching PR's effective presentation contribution:

- `keep`: normal contribution.
- `mute`: no independent visibility or attention from that PR.
- `deprioritize`: cap that PR's active contribution at low priority.

A Jira issue, authored note, other PR or local workspace keeps its own contribution. Informational refs do not independently surface work. Local-only tasks cannot match GitHub rules. A task supported only by muted PRs is hidden, including from Done and counts; linked tasks retain every PR for inspection and opening. Explicit whole-task muting (`radar-muted`, the Muted section) remains separate.

Muting does not delete refs, break linking, change completion or authorize cleanup. A muted open PR still prevents automatic completion and can reopen a completed authored note. Done remains terminal for attention. Acknowledgements affect only the source feedback they acknowledge, never another source's signal.

## Involvement and activity

Normal relevance comes first: feedback on your own PRs and replies in unresolved review threads you joined can raise attention. Actor rules then filter that activity. `keep` means normal relevance, not a subscription to all discussions. `ignore` does not cancel a direct request for your review. PR policy applies after activity rules; an activity exception cannot override PR mute/deprioritization.

Explicit tracking adds visibility but does not fetch every tracked PR's discussion stream. Existing authored, review-requested and participated collection supplies personal activity. Collected participation refs without relevant activity remain linkable and lifecycle-aware without surfacing independently.

## Collection and refs

Refs use `github:pr:<owner/repository>:<number>` identities, directly openable URLs, branch linking keys, per-PR author metadata and typed acknowledgement contracts. Authored, review, participation, explicit tracking and bound-PR resolution share the same policy semantics.

Extra tracking runs concurrently with the personal GraphQL request. Repository patterns expand through paginated owner repository connections, cached for 24 hours. Each concrete repository's open-PR connection is paginated, including branch and author facts; no global search limit lets unrelated repositories crowd out the configured scope. Overlapping scopes reuse requests and deduplicate PRs.

Missing active PRs become done only after a confirmed terminal state. Failed pages, inaccessible repositories and exhausted API budgets retain prior observations and report incomplete collection. A partial response never replaces a complete repository cache. Core/GraphQL budgets come from `gh api rate_limit`; collection pauses when low.

## Workspace behavior

The workspace seed capability finds a matching local repository, resolves the PR head, fetches pull refs when necessary, and returns a generic existing-branch seed. Jira development links are resolved here so Jira never parses GitHub identities.

## Configuration example

```yaml
github:
  track:
    - repos: ["acme/platform-*"]
      authors: ["renovate[bot]"]
  pull_request_rules:
    - repos: ["acme/important"]
      authors: ["renovate[bot]"]
      action: keep
    - authors: ["renovate[bot]"]
      action: deprioritize
    - repos: ["acme/noisy-repo"]
      action: mute
  activity_rules:
    - actors: ["review-bot[bot]"]
      action: ignore
```

Tracking is not a subscription to unrelated discussions. For matching rules,
fields combine with AND and values within a field with OR; the first match wins.
See [configuration](../configuration.md) for other settings.

## Rollout

This is an intentional configuration and policy change, not a compatibility layer. Edit existing `github.filters` configuration manually before installing/running the new binary:

1. Put additional repository/author discovery into `track`.
2. Convert repository/user presentation rules to `pull_request_rules`, using `authors`.
3. Put comment/review suppression into `activity_rules`, using `actors` and `ignore`.
4. Reorder policies for first-match precedence. Old unconditional mute behavior requires mute rules first, but now applies only to PR contributions.
5. Remove the old fields. They are rejected rather than silently ignored.

The rebuildable task cache stays at version 8. `SourceRef.Presentation.Hidden` and `Task.AttentionSourceRefID` are additive fields; existing records, IDs, bindings and acknowledgements remain readable. No note, workspace registry or authored preference schema changes. A full refresh supplies new author/activity metadata to old cached refs. Do not assume old cached authorless observations can match author rules before that refresh.

Repository and tracked-PR cache files keep their JSON envelopes. The tracked cache now keys observations by concrete repository rather than owner/author pairs; old entries are unused and can be removed manually, but no reset is required for correctness. New queries repopulate those entries. Before installation, inventory the actual config/state/cache paths and perform a read-only preflight; do not reset user data or install an incompatible config without an explicit rollout decision.

## Validation

```sh
go test ./internal/integration/github/... ./internal/state ./internal/protocol ./internal/collector ./internal/notification ./internal/config ./internal/server ./cmd/radar
radar rate-limit
```
