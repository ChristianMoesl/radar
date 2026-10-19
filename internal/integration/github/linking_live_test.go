package github

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"radar/internal/integration/github/identity"
	"radar/internal/integration/github/settings"
	"radar/internal/linking"
	"radar/internal/protocol"
)

// Opt-in, read-only provider qualification. The file contains non-secret,
// explicitly selected fixtures; gh continues to own all authentication.
func TestLivePullRequestReferences(t *testing.T) {
	path := os.Getenv("RADAR_TEST_GITHUB_LINK_FIXTURES")
	if path == "" {
		t.Skip("set RADAR_TEST_GITHUB_LINK_FIXTURES to a JSON fixture list")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Repository string   `json:"repository"`
		Number     int      `json:"number"`
		Prefixes   []string `json:"prefixes"`
		State      string   `json:"state"`
		Keys       []string `json:"keys"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("empty fixture list")
	}
	resetRateStateForTest(t)
	for _, fixture := range fixtures {
		t.Run(identity.PullRequestKey(fixture.Repository, fixture.Number), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pr, login, err := fetchBoundPullRequest(ctx, fixture.Repository, fixture.Number)
			if err != nil {
				t.Fatal(err)
			}
			if pr.State != fixture.State {
				t.Fatalf("state = %s, want %s", pr.State, fixture.State)
			}
			task, err := boundPullRequestTask(pr, login, nil, settings.Default(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			tasks := []protocol.Task{task}
			if err := collectLinkingMarks(ctx, tasks, nil, linking.NewMarkMatcher(fixture.Prefixes), testLogger()); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, key := range tasks[0].SourceRefs[0].LinkingKeys {
				if linking.IsMarkKey(key) {
					got = append(got, key)
				}
			}
			slices.Sort(got)
			slices.Sort(fixture.Keys)
			if !reflect.DeepEqual(got, fixture.Keys) {
				t.Fatalf("links = %v, want %v", got, fixture.Keys)
			}
			t.Logf("verified %s %s, branch %q, links %v", pr.URL, pr.State, pr.HeadRefName, got)
		})
	}
}
