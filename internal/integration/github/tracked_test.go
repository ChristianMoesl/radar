package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/integration/github/settings"
)

func trackingBudget(t *testing.T) {
	t.Helper()
	resetRateStateForTest(t)
	rateState.mu.Lock()
	rateState.fetched = time.Now()
	rateState.response.Resources.GraphQL = rateLimitResource{Limit: 5000, Remaining: 5000, Reset: time.Now().Add(time.Hour).Unix()}
	rateState.mu.Unlock()
	// os.UserCacheDir ignores XDG_CACHE_HOME on macOS and uses HOME instead.
	// Isolate both so tests never read or overwrite the real GitHub caches.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(home, cacheDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatalf("cache escaped temporary home: %q, %v", cacheDir, err)
	}
}

func trackedFixture(number int) searchPullRequest {
	pr := searchPullRequest{Number: number, NodeID: fmt.Sprintf("PR_%d", number), Title: "Work", URL: fmt.Sprintf("https://github.com/acme/app/pull/%d", number), State: "OPEN", HeadRefName: fmt.Sprintf("work-%d", number), Author: &user{Login: "renovate", Type: "Bot"}}
	pr.Repository.NameWithOwner = "acme/app"
	return pr
}

func trackedPage(t *testing.T, prs []searchPullRequest, next string) string {
	t.Helper()
	if prs == nil {
		prs = []searchPullRequest{}
	}
	data, err := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{"nodes": prs, "pageInfo": pageInfo{HasNextPage: next != "", EndCursor: next}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTrackedRepositoryPaginationCollectsMoreThan100PRsAndBranches(t *testing.T) {
	trackingBudget(t)
	first := make([]searchPullRequest, 100)
	for i := range first {
		first[i] = trackedFixture(i + 1)
	}
	last := trackedFixture(101)
	last.Draft = true
	installFakeGH(t, `#!/bin/sh
case "$*" in
  *"cursor=next"*) echo '`+trackedPage(t, []searchPullRequest{last}, "")+`' ;;
  *"RadarTrackedPullRequests"*) echo '`+trackedPage(t, first, "next")+`' ;;
  *) exit 1 ;;
esac
`)
	items, err := FetchTrackedPullRequests(context.Background(), []settings.TrackingScope{{Repos: []string{"acme/app"}}}, testLogger())
	if err != nil || len(items) != 101 {
		t.Fatalf("items=%d error=%v", len(items), err)
	}
	if items[100].SourceRefs[0].Branch != "work-101" || items[100].Reason != "tracked draft PR" {
		t.Fatalf("incomplete PR: %+v", items[100])
	}
}

func TestTrackedRepositoryFailurePreservesCacheAndReportsIncomplete(t *testing.T) {
	for _, failure := range []string{
		"exit 1", `echo '{"errors":[{"message":"denied"}]}'`,
		`echo '{"data":{"repository":{"pullRequests":{"nodes":[]}}}}'`,
		`echo '{"data":{"repository":null}}'`,
		`echo '{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":true}}}}}'`,
	} {
		t.Run(failure, func(t *testing.T) {
			trackingBudget(t)
			installFakeGH(t, "#!/bin/sh\n"+failure+"\n")
			cache := trackedPullRequestCacheFile{Targets: map[string]trackedPullRequestCacheEntry{"acme/app": {PRs: []searchPullRequest{trackedFixture(42)}}}}
			prs, changed, err := cachedRepositoryPullRequests(context.Background(), "acme/app", &cache, testLogger())
			if err == nil || changed || len(prs) != 1 || prs[0].HeadRefName != "work-42" {
				t.Fatalf("prs=%+v changed=%t err=%v", prs, changed, err)
			}
		})
	}
}

func TestTrackedRefreshesRecentCacheAndDeduplicatesOverlappingScopes(t *testing.T) {
	trackingBudget(t)
	marker := filepath.Join(t.TempDir(), "calls")
	installFakeGH(t, "#!/bin/sh\necho call >> '"+marker+"'\necho '"+trackedPage(t, []searchPullRequest{trackedFixture(43)}, "")+"'\n")
	cache := trackedPullRequestCacheFile{Targets: map[string]trackedPullRequestCacheEntry{"acme/app": {FetchedAt: time.Now().UTC().Format(time.RFC3339), PRs: []searchPullRequest{trackedFixture(42)}}}}
	if err := writeGitHubCache("github-tracked-prs.json", cache); err != nil {
		t.Fatal(err)
	}
	items, err := FetchTrackedPullRequests(context.Background(), []settings.TrackingScope{
		{Repos: []string{"acme/app"}, Authors: []string{"renovate[bot]"}}, {Repos: []string{"ACME/APP"}},
	}, testLogger())
	calls, _ := os.ReadFile(marker)
	if err != nil || len(items) != 1 || items[0].SourceRefs[0].Branch != "work-43" || string(calls) != "call\n" {
		t.Fatalf("items=%+v err=%v calls=%s", items, err, calls)
	}
}

func TestRepositoryPatternEnumerationPaginatesAndIgnoresUnrelatedRepos(t *testing.T) {
	trackingBudget(t)
	installFakeGH(t, `#!/bin/sh
case "$*" in
  *"RadarTrackedRepositories"*"cursor=next"*)
    echo '{"data":{"repositoryOwner":{"repositories":{"nodes":[{"nameWithOwner":"acme/app"}],"pageInfo":{"hasNextPage":false}}}}}' ;;
  *"RadarTrackedRepositories"*)
    echo '{"data":{"repositoryOwner":{"repositories":{"nodes":[{"nameWithOwner":"acme/unrelated"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}' ;;
  *"RadarTrackedPullRequests"*"name=app"*) echo '`+trackedPage(t, []searchPullRequest{trackedFixture(1)}, "")+`' ;;
  *) echo "unexpected request" >&2; exit 1 ;;
esac
`)
	items, err := FetchTrackedPullRequests(context.Background(), []settings.TrackingScope{{Repos: []string{"acme/app*"}}}, testLogger())
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestBudgetExhaustionKeepsCachedObservationsButNotCompleteness(t *testing.T) {
	trackingBudget(t)
	rateState.mu.Lock()
	rateState.response.Resources.GraphQL.Remaining = 0
	rateState.mu.Unlock()
	cache := trackedPullRequestCacheFile{Targets: map[string]trackedPullRequestCacheEntry{"acme/app": {PRs: []searchPullRequest{trackedFixture(42)}}}}
	if err := writeGitHubCache("github-tracked-prs.json", cache); err != nil {
		t.Fatal(err)
	}
	installFakeGH(t, "#!/bin/sh\necho should-not-call >&2\nexit 1\n")
	items, err := FetchTrackedPullRequests(context.Background(), []settings.TrackingScope{{Repos: []string{"acme/app"}}}, testLogger())
	if len(items) != 1 || err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestFailedLaterPageDoesNotReplaceCompleteCacheWithPartialResults(t *testing.T) {
	trackingBudget(t)
	installFakeGH(t, `#!/bin/sh
case "$*" in
  *"cursor=next"*) exit 1 ;;
  *) echo '`+trackedPage(t, []searchPullRequest{trackedFixture(1)}, "next")+`' ;;
esac
`)
	cache := trackedPullRequestCacheFile{Targets: map[string]trackedPullRequestCacheEntry{"acme/app": {PRs: []searchPullRequest{trackedFixture(42)}}}}
	prs, changed, err := cachedRepositoryPullRequests(context.Background(), "acme/app", &cache, testLogger())
	if err == nil || changed || len(prs) != 1 || prs[0].Number != 42 || cache.Targets["acme/app"].PRs[0].Number != 42 {
		t.Fatalf("cache replaced: %+v %t %v", prs, changed, err)
	}
}

func TestTrackingScopesUseRepositoryAndOptionalExactAuthor(t *testing.T) {
	pr := trackedFixture(1)
	for _, test := range []struct {
		repos, authors []string
		want           bool
	}{
		{[]string{"acme/*"}, nil, true}, {[]string{"acme/app"}, []string{"renovate[bot]"}, true},
		{[]string{"acme/app"}, []string{"human"}, false}, {[]string{"acme/other"}, []string{"renovate[bot]"}, false},
	} {
		if got := trackedByScopes(pr, []settings.TrackingScope{{Repos: test.repos, Authors: test.authors}}); got != test.want {
			t.Fatalf("scope %+v: %t", test, got)
		}
	}
}
