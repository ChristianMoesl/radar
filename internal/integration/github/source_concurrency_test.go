package github

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/integration"
)

func TestSourceCollectFetchesMainAndTrackedPullRequestsConcurrently(t *testing.T) {
	trackingBudget(t)

	dir := t.TempDir()
	configHome := filepath.Join(dir, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`linking_mark_prefixes:
  - ABC
github:
  track:
    - repos: [acme/app]
      authors: ["renovate[bot]"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	installFakeGH(t, `#!/bin/sh
set -eu
case "$*" in
  *"reviewQuery"*)
    touch "`+started+`.graphql"
    i=0
    while [ ! -f "`+release+`" ]; do
      i=$((i+1)); [ "$i" -lt 100 ] || exit 2; sleep 0.01
    done
    echo '{"data":{"viewer":{"login":"me"},"reviewRequested":{"nodes":[]},"authored":{"nodes":[]},"participated":{"nodes":[]}}}'
    ;;
  *"RadarTrackedPullRequests"*)
    touch "`+started+`.tracked"
    i=0
    while [ ! -f "`+release+`" ]; do
      i=$((i+1)); [ "$i" -lt 100 ] || exit 2; sleep 0.01
    done
    echo '{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}'
    ;;
  *)
    echo "unexpected gh args: $*" >&2
    exit 1
    ;;
esac
`)

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan integration.CollectResult, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		// Finish cache-writing goroutines before Setenv restores the real home.
		cancel()
		<-finished
	})
	go func() {
		defer close(finished)
		resultCh <- (Source{}).Collect(ctx, integration.CollectRequest{
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
