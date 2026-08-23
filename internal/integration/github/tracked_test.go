package github

import (
	"context"
	"testing"
	"time"
)

func TestTrackedPullRequestCacheRefreshesEvenWhenRecent(t *testing.T) {
	resetRateStateForTest(t)
	rateState.mu.Lock()
	rateState.fetched = time.Now()
	rateState.response.Resources.Search = rateLimitResource{Limit: 30, Remaining: 30, Reset: time.Now().Add(time.Minute).Unix()}
	rateState.response.Resources.GraphQL = rateLimitResource{Limit: 5000, Remaining: 5000, Reset: time.Now().Add(time.Hour).Unix()}
	rateState.mu.Unlock()
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "search prs "*)
    cat <<'JSON'
[{"id":"PR_current","number":43,"title":"Current PR","url":"https://github.com/acme/app/pull/43","repository":{"nameWithOwner":"acme/app"},"isDraft":false,"state":"open","body":"","author":{"login":"renovate[bot]"}}]
JSON
    ;;
  "api graphql "*)
    cat <<'JSON'
{"data":{"nodes":[{"id":"PR_current","headRefName":"renovate/current"}]}}
JSON
    ;;
  *)
    echo "unexpected gh args: $*" >&2
    exit 1
    ;;
esac
`)
	cache := trackedPullRequestCacheFile{Targets: map[string]trackedPullRequestCacheEntry{
		trackedPullRequestCacheKey("acme", "renovate[bot]"): {
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
			PRs:       []searchPullRequest{{Number: 42, Title: "Stale PR"}},
		},
	}}

	prs, changed, err := cachedSearchPullRequestsByOwnerAndAuthor(context.Background(), "acme", "renovate[bot]", &cache, testLogger())
	if err != nil {
		t.Fatalf("cachedSearchPullRequestsByOwnerAndAuthor() error = %v", err)
	}
	if !changed || len(prs) != 1 || prs[0].Number != 43 || prs[0].HeadRefName != "renovate/current" {
		t.Fatalf("pull requests = %+v, changed = %t; want refreshed PR 43 with branch", prs, changed)
	}
	ref := trackedPullRequestTask(prs[0]).SourceRefs[0]
	if ref.Branch != "renovate/current" || ref.Presentation.WorkspaceName != "renovate/current" {
		t.Fatalf("tracked pull request ref = %+v, want workspace branch metadata", ref)
	}
}

func TestTrackedPullRequestCachePreservesPreviousBranchesWhenEnrichmentFails(t *testing.T) {
	resetRateStateForTest(t)
	rateState.mu.Lock()
	rateState.fetched = time.Now()
	rateState.response.Resources.Search = rateLimitResource{Limit: 30, Remaining: 30, Reset: time.Now().Add(time.Minute).Unix()}
	rateState.response.Resources.GraphQL = rateLimitResource{Limit: 5000, Remaining: 5000, Reset: time.Now().Add(time.Hour).Unix()}
	rateState.mu.Unlock()
	installFakeGH(t, `#!/bin/sh
case "$*" in
  "search prs "*)
    echo '[{"id":"PR_current","number":43,"repository":{"nameWithOwner":"acme/app"}}]'
    ;;
  "api graphql "*)
    echo 'branch lookup failed' >&2
    exit 1
    ;;
esac
`)
	cached := searchPullRequest{NodeID: "PR_cached", Number: 42, HeadRefName: "renovate/cached"}
	cache := trackedPullRequestCacheFile{Targets: map[string]trackedPullRequestCacheEntry{
		trackedPullRequestCacheKey("acme", "renovate[bot]"): {PRs: []searchPullRequest{cached}},
	}}

	prs, changed, err := cachedSearchPullRequestsByOwnerAndAuthor(context.Background(), "acme", "renovate[bot]", &cache, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if changed || len(prs) != 1 || prs[0].Number != 42 || prs[0].HeadRefName != "renovate/cached" {
		t.Fatalf("pull requests = %+v, changed = %t; want cached branch metadata", prs, changed)
	}
}

func TestSearchPullRequestsUsesSupportedGHJSONFields(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
expected='search prs --owner acme --author renovate[bot] --state open --limit 100 --json id,number,title,url,repository,isDraft,state,body,author'
if [ "$*" != "$expected" ]; then
  echo "unexpected gh args: $*" >&2
  exit 1
fi
cat <<'JSON'
[{"id":"PR_42","number":42,"title":"Update dependency","url":"https://github.com/acme/app/pull/42","repository":{"nameWithOwner":"acme/app"},"isDraft":false,"state":"open","body":"","author":{"login":"renovate[bot]"}}]
JSON
`)

	prs, err := searchPullRequestsByOwnerAndAuthor(context.Background(), "acme", "renovate[bot]")
	if err != nil {
		t.Fatalf("searchPullRequestsByOwnerAndAuthor() error = %v", err)
	}
	if len(prs) != 1 || repoName(prs[0]) != "acme/app" || prs[0].Number != 42 || prs[0].NodeID != "PR_42" {
		t.Fatalf("pull requests = %+v, want acme/app#42", prs)
	}
}
