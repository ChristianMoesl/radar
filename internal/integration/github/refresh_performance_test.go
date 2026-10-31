package github

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/integration/github/settings"
)

// This measures production tracked-repository discovery with a fixed 20ms
// subprocess latency. Empty, complete pages isolate scheduling and API count.
// It is not a live GitHub timing or a throughput target.
func BenchmarkRefreshProbeTrackedRepositories(b *testing.B) {
	for _, count := range []int{1, 8, 24} {
		b.Run(fmt.Sprintf("repos=%d", count), func(b *testing.B) {
			dir := b.TempDir()
			b.Setenv("HOME", dir)
			b.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
			marker := filepath.Join(dir, "requests")
			b.Setenv("RADAR_PROBE_REQUESTS", marker)
			script := `#!/bin/sh
set -eu
case "$*" in
  *RadarTrackedPullRequests*)
    printf 'request\n' >> "$RADAR_PROBE_REQUESTS"
    sleep 0.020
    printf '%s\n' '{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}'
    ;;
  *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0755); err != nil {
				b.Fatal(err)
			}
			b.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			repos := make([]string, count)
			for i := range repos {
				repos[i] = fmt.Sprintf("acme/repo-%02d", i)
			}
			rateState.mu.Lock()
			oldFetched, oldResponse, oldUntil := rateState.fetched, rateState.response, rateState.graphqlUntil
			rateState.fetched, rateState.graphqlUntil = time.Now(), time.Time{}
			rateState.response.Resources.GraphQL = rateLimitResource{Limit: 1000000, Remaining: 1000000, Reset: time.Now().Add(time.Hour).Unix()}
			rateState.mu.Unlock()
			b.Cleanup(func() {
				rateState.mu.Lock()
				defer rateState.mu.Unlock()
				rateState.fetched, rateState.response, rateState.graphqlUntil = oldFetched, oldResponse, oldUntil
			})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				items, err := FetchTrackedPullRequests(context.Background(), []settings.TrackingScope{{Repos: repos}}, testLogger())
				if err != nil || len(items) != 0 {
					b.Fatalf("items=%d error=%v", len(items), err)
				}
			}
			b.StopTimer()
			calls, err := os.ReadFile(marker)
			if err != nil {
				b.Fatal(err)
			}
			requests := strings.Count(string(calls), "request\n")
			if requests != b.N*count {
				b.Fatalf("requests=%d, want %d", requests, b.N*count)
			}
			b.ReportMetric(float64(requests)/float64(b.N), "requests/op")
		})
	}
}
