package github

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/integration/github/settings"
)

func TestTrackedRepositoriesBoundWorkersAndCancelQueuedRequests(t *testing.T) {
	for _, cancelQueued := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelQueued), func(t *testing.T) {
			trackingBudget(t)
			dir := t.TempDir()
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			if err := os.Mkdir(started, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RADAR_TEST_STARTED", started)
			t.Setenv("RADAR_TEST_RELEASE", release)
			installFakeGH(t, `#!/bin/sh
set -eu
for arg in "$@"; do case "$arg" in name=*) repo=${arg#name=} ;; esac; done
touch "$RADAR_TEST_STARTED/$repo"
while [ ! -f "$RADAR_TEST_RELEASE" ]; do sleep 0.01; done
printf '%s\n' '{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}'
`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			joined := make(chan struct{})
			t.Cleanup(func() { cancel(); <-joined })
			repos := make([]string, 8)
			for i := range repos {
				repos[i] = fmt.Sprintf("acme/repo-%d", i)
			}
			go func() {
				defer close(joined)
				_, err := FetchTrackedPullRequests(ctx, []settings.TrackingScope{{Repos: repos}}, testLogger())
				done <- err
			}()
			for {
				files, err := os.ReadDir(started)
				if err != nil {
					t.Fatal(err)
				}
				if len(files) >= 4 {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("four repositories did not start concurrently")
				}
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(30 * time.Millisecond)
			files, _ := os.ReadDir(started)
			if len(files) != 4 {
				t.Fatalf("started=%d, want worker limit 4", len(files))
			}
			if cancelQueued {
				cancel()
			} else if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if (err != nil) != cancelQueued {
					t.Fatalf("cancel=%t, error=%v", cancelQueued, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("workers did not finish after release/cancellation")
			}
			files, _ = os.ReadDir(started)
			want := 8
			if cancelQueued {
				want = 4
			}
			if len(files) != want {
				t.Fatalf("requests=%d, want %d", len(files), want)
			}
		})
	}
}

func TestConcurrentTrackedFailurePreservesCacheAndRepositoryOrder(t *testing.T) {
	trackingBudget(t)
	fixture := func(repo string, number int) string {
		pr := trackedFixture(number)
		pr.Repository.NameWithOwner, pr.URL = repo, fmt.Sprintf("https://github.com/%s/pull/%d", repo, number)
		return trackedPage(t, []searchPullRequest{pr}, "")
	}
	old := trackedFixture(42)
	old.Repository.NameWithOwner, old.URL = "acme/b", "https://github.com/acme/b/pull/42"
	cache := trackedPullRequestCacheFile{Targets: map[string]trackedPullRequestCacheEntry{"acme/b": {FetchedAt: "2000-01-01T00:00:00Z", PRs: []searchPullRequest{old}}}}
	if err := writeGitHubCache("github-tracked-prs.json", cache); err != nil {
		t.Fatal(err)
	}
	installFakeGH(t, `#!/bin/sh
case "$*" in
 *"name=a"*) sleep 0.02; echo '`+fixture("acme/a", 1)+`' ;;
 *"name=b"*) exit 1 ;;
 *"name=c"*) echo '`+fixture("acme/c", 3)+`' ;;
 *) exit 2 ;;
esac
`)
	items, err := FetchTrackedPullRequests(context.Background(), []settings.TrackingScope{{Repos: []string{"acme/c", "acme/b", "acme/a"}}}, testLogger())
	if err == nil || !strings.Contains(err.Error(), "track acme/b") || len(items) != 3 {
		t.Fatalf("items=%d error=%v", len(items), err)
	}
	for i, repo := range []string{"acme/a", "acme/b", "acme/c"} {
		if items[i].SourceRefs[0].Repo != repo {
			t.Fatalf("item %d repo=%s, want %s", i, items[i].SourceRefs[0].Repo, repo)
		}
	}
	var persisted trackedPullRequestCacheFile
	if err := readGitHubCache("github-tracked-prs.json", &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Targets["acme/b"].FetchedAt != cache.Targets["acme/b"].FetchedAt || len(persisted.Targets) != 3 {
		t.Fatal("failed repository replaced its cache or successful cache was lost")
	}
}

func TestConcurrentGraphQLReservationsDoNotOverspend(t *testing.T) {
	trackingBudget(t)
	rateState.mu.Lock()
	rateState.response.Resources.GraphQL.Remaining = minGraphQLRemaining + 20
	rateState.mu.Unlock()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var allowed atomic.Int32
	for range 32 {
		wg.Go(func() {
			<-start
			if EnsureGraphQLBudget(context.Background(), testLogger()) {
				allowed.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	if allowed.Load() != 2 {
		t.Fatalf("allowed=%d, want exactly 2 reservations", allowed.Load())
	}
	rateState.mu.Lock()
	defer rateState.mu.Unlock()
	if rateState.response.Resources.GraphQL.Remaining != minGraphQLRemaining {
		t.Fatal("shared budget was overspent")
	}
}
