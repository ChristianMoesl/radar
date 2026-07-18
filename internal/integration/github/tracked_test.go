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
	rateState.mu.Unlock()
	installFakeGH(t, `#!/bin/sh
cat <<'JSON'
[{"number":43,"title":"Current PR","url":"https://github.com/acme/app/pull/43","repository":{"nameWithOwner":"acme/app"},"isDraft":false,"state":"open","body":"","author":{"login":"renovate[bot]"}}]
JSON
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
	if !changed || len(prs) != 1 || prs[0].Number != 43 {
		t.Fatalf("pull requests = %+v, changed = %t; want refreshed PR 43", prs, changed)
	}
}

func TestSearchPullRequestsUsesSupportedGHJSONFields(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
expected='search prs --owner acme --author renovate[bot] --state open --limit 100 --json number,title,url,repository,isDraft,state,body,author'
if [ "$*" != "$expected" ]; then
  echo "unexpected gh args: $*" >&2
  exit 1
fi
cat <<'JSON'
[{"number":42,"title":"Update dependency","url":"https://github.com/acme/app/pull/42","repository":{"nameWithOwner":"acme/app"},"isDraft":false,"state":"open","body":"","author":{"login":"renovate[bot]"}}]
JSON
`)

	prs, err := searchPullRequestsByOwnerAndAuthor(context.Background(), "acme", "renovate[bot]")
	if err != nil {
		t.Fatalf("searchPullRequestsByOwnerAndAuthor() error = %v", err)
	}
	if len(prs) != 1 || repoName(prs[0]) != "acme/app" || prs[0].Number != 42 {
		t.Fatalf("pull requests = %+v, want acme/app#42", prs)
	}
}
