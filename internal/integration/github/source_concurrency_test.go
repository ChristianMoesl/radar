package github

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/filters"
	"radar/internal/integration"
)

func TestSourceCollectFetchesMainAndTrackedPullRequestsConcurrently(t *testing.T) {
	resetRateStateForTest(t)
	rateState.mu.Lock()
	rateState.fetched = time.Now()
	rateState.response.Resources.Search = rateLimitResource{Limit: 30, Remaining: 30, Reset: time.Now().Add(time.Minute).Unix()}
	rateState.mu.Unlock()

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	installFakeGH(t, `#!/bin/sh
set -eu
case "$*" in
  *"api graphql"*)
    touch "`+started+`.graphql"
    i=0
    while [ ! -f "`+release+`" ]; do
      i=$((i+1)); [ "$i" -lt 100 ] || exit 2; sleep 0.01
    done
    echo '{"data":{"viewer":{"login":"me"},"reviewRequested":{"nodes":[]},"authored":{"nodes":[]},"participated":{"nodes":[]}}}'
    ;;
  "search prs --owner acme --author renovate[bot] --state open --limit 100 --json id,number,title,url,repository,isDraft,state,body,author")
    touch "`+started+`.tracked"
    i=0
    while [ ! -f "`+release+`" ]; do
      i=$((i+1)); [ "$i" -lt 100 ] || exit 2; sleep 0.01
    done
    echo '[]'
    ;;
  *)
    echo "unexpected gh args: $*" >&2
    exit 1
    ;;
esac
`)

	resultCh := make(chan integration.CollectResult, 1)
	go func() {
		resultCh <- (Source{}).Collect(context.Background(), integration.CollectRequest{
			Filters: filters.Config{Rules: []filters.Rule{{
				Repos: []string{"acme/*"}, Users: []string{"renovate[bot]"}, Action: "deprioritize",
			}}},
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	deadline := time.Now().Add(time.Second)
	for {
		_, graphQLErr := os.Stat(started + ".graphql")
		_, trackedErr := os.Stat(started + ".tracked")
		if graphQLErr == nil && trackedErr == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = os.WriteFile(release, nil, 0o600)
			t.Fatal("GitHub requests did not start concurrently")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-resultCh:
		if !result.Complete {
			t.Fatalf("collection result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("GitHub collection did not finish")
	}
}
