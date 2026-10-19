package github

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/github/settings"
	"radar/internal/protocol"
)

func TestTrackedRepositoryPatternFeedsEventsFromTheExistingCollection(t *testing.T) {
	trackingBudget(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("github:\n  track:\n    - repos: [acme/app*]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pr := trackedFixture(42)
	pr.HeadRefOID = "abc123"
	installFakeGH(t, `#!/bin/sh
case "$*" in
  *"reviewQuery"*) echo '{"data":{"viewer":{"login":"me"},"reviewRequested":{"nodes":[],"pageInfo":{"hasNextPage":false}},"authored":{"nodes":[]},"participated":{"nodes":[]}}}' ;;
  *"RadarTrackedRepositories"*) echo '{"data":{"repositoryOwner":{"repositories":{"nodes":[{"nameWithOwner":"acme/app"},{"nameWithOwner":"acme/unrelated"}],"pageInfo":{"hasNextPage":false}}}}}' ;;
  *"RadarTrackedPullRequests"*"name=app"*) echo '`+trackedPage(t, []searchPullRequest{pr}, "")+`' ;;
  *) exit 1 ;;
esac
`)
	result := (Source{}).Collect(context.Background(), integration.CollectRequest{Logger: testLogger()})
	items := (Source{}).CollectionItems(result.Observations)
	if !result.Complete || len(items) != 1 || items[0].Key != "github:pr:acme/app:42" {
		t.Fatalf("tracked collection did not feed events: %+v, %+v", result, items)
	}
}

func TestReviewDiscoveryRetainsFactsForAgentEvents(t *testing.T) {
	for _, state := range []string{"OPEN", "MERGED"} {
		t.Run(state, func(t *testing.T) {
			trackingBudget(t)
			pr := mergedReviewFixture(t, time.Now())
			pr.State, pr.NodeID, pr.HeadRefOID = state, "PR_7", "abc123"
			installFakeGH(t, `#!/bin/sh
case "$*" in
  *"id number title url state isDraft"*"headRefOid"*) echo '`+reviewSearchPage(t, []searchPullRequest{pr}, "")+`' ;;
  *) echo 'review query omitted durable-agent identity fields' >&2; exit 1 ;;
esac
`)
			var tasks []protocol.Task
			if state == "OPEN" {
				first := graphQLSearchResult{Nodes: []searchPullRequest{}, Page: &pageInfo{HasNextPage: true, EndCursor: "next"}}
				prs, err := remainingReviewPages(context.Background(), reviewPageQuery, "is:pr is:open review-requested:@me", first, testLogger())
				if err != nil {
					t.Fatal(err)
				}
				tasks = reviewRequestTasks(prs)
			} else {
				var err error
				tasks, err = FetchMergedReviewRequests(context.Background(), nil, mergedReviewSettings().ReviewRequests, testLogger())
				if err != nil {
					t.Fatal(err)
				}
			}
			items := (Source{}).CollectionItems(observationsFromTasks(tasks))
			if len(items) != 1 {
				t.Fatalf("review discovery lost agent facts: %+v", items)
			}
			var facts struct{ NodeID, HeadOID, State string }
			if err := json.Unmarshal(items[0].Payload, &facts); err != nil {
				t.Fatal(err)
			}
			if facts.NodeID != pr.NodeID || facts.HeadOID != pr.HeadRefOID || facts.State != state {
				t.Fatalf("review facts: %+v", facts)
			}
		})
	}
}

func TestResolvedTerminalFactsRetainReviewCleanupGrace(t *testing.T) {
	for _, tc := range []struct {
		state   string
		pending bool
	}{
		{state: "CLOSED"},
		{state: "MERGED"},
		{state: "MERGED", pending: true},
	} {
		name := tc.state
		if tc.pending {
			name += " review no longer configured"
		}
		t.Run(name, func(t *testing.T) {
			resetRateStateForTest(t)
			before := time.Now().UTC().Truncate(time.Second)
			closedAt := before.Add(-48 * time.Hour).Format(time.RFC3339)
			body, _ := json.Marshal(map[string]any{"state": "closed", "merged": tc.state == "MERGED", "closed_at": closedAt})
			installFakeGH(t, `#!/bin/sh
case "$*" in
  "api rate_limit") echo '{"resources":{"core":{"limit":5000,"remaining":5000,"reset":4102444800},"graphql":{"limit":5000,"remaining":5000,"reset":4102444800}}}' ;;
  "api /repos/acme/app/pulls/34") echo '`+string(body)+`' ;;
  *) exit 1 ;;
esac
`)
			metadata := map[string]string{"pr_state": "OPEN", "pr_node_id": "PR_34", "head_oid": "abc123"}
			if tc.pending {
				metadata["pr_state"], metadata["merged_review_pending"] = "MERGED", "true"
			}
			previous := []protocol.Task{{Attention: "attention", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:34", Source: "github", Kind: "pull_request", Metadata: metadata}}}}
			tasks := ResolveDonePullRequests(context.Background(), previous, nil, true, settings.Default().ReviewRequests, testLogger())
			if len(tasks) != 1 || tasks[0].Attention != "done" || tasks[0].Metadata["pr_state"] != tc.state || tasks[0].SourceRefs[0].Metadata["pr_state"] != tc.state {
				t.Fatalf("explicit terminal facts lost: %+v", tasks)
			}
			if tc.pending {
				completed, err := time.Parse(time.RFC3339, tasks[0].DoneAt)
				if err != nil || completed.Before(before) || tasks[0].Metadata["completed_at"] != tasks[0].DoneAt {
					t.Fatalf("review completion backdated cleanup: %+v", tasks[0])
				}
				if tasks[0].Metadata["merged_review_pending"] != "" || tasks[0].SourceRefs[0].Metadata["merged_review_pending"] != "" || metadata["merged_review_pending"] != "true" {
					t.Fatal("pending flag was retained or previous facts mutated")
				}
			} else if tasks[0].DoneAt != closedAt {
				t.Fatalf("ordinary closure lost its timestamp: %+v", tasks[0])
			}
			items := (Source{}).CollectionItems(observationsFromTasks(tasks))
			if len(items) != 1 {
				t.Fatalf("explicit closure did not feed agent events: %+v", items)
			}
		})
	}
}

func TestCollectionItemsContainStablePRFactsBeforeDisplayPolicies(t *testing.T) {
	pr := trackedFixture(42)
	pr.HeadRefOID = "abc123"
	pr.Draft = true
	observations := observationsFromTasks([]protocol.Task{trackedPullRequestTask(pr)})
	// Feed the same hidden/muted contribution that remains available to storage.
	observations[0].Ref.Presentation.Hidden = true
	items := (Source{}).CollectionItems(observations)
	if len(items) != 1 || items[0].Key != "github:pr:acme/app:42" {
		t.Fatalf("source identity: %+v", items)
	}
	var facts struct {
		NodeID, Repository, Author, AuthorType, State, HeadOID, HeadRef string
		Number                                                          int
		Draft                                                           bool
	}
	if err := json.Unmarshal(items[0].Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.NodeID != "PR_42" || facts.Repository != "acme/app" || facts.Author != "renovate" || facts.AuthorType != "Bot" || facts.State != "OPEN" || facts.HeadOID != "abc123" || facts.HeadRef != "work-42" || facts.Number != 42 || !facts.Draft {
		t.Fatalf("facts: %+v", facts)
	}
	terminal := observations[0]
	terminal.Ref.Metadata = cloneMetadata(terminal.Ref.Metadata)
	terminal.Ref.Metadata["pr_state"] = "CLOSED"
	// Retained done facts cannot override fresh reopening evidence.
	combined := (Source{}).CollectionItems(append(observations, terminal))
	if string(combined[0].Payload) != string(items[0].Payload) {
		t.Fatal("cached closed observation overrode fresh open PR")
	}
	closed := (Source{}).CollectionItems([]integration.Observation{terminal})
	_ = json.Unmarshal(closed[0].Payload, &facts)
	if facts.State != "CLOSED" {
		t.Fatal("explicit closure was lost")
	}
	delete(terminal.Ref.Metadata, "head_oid")
	if len((Source{}).CollectionItems([]integration.Observation{terminal})) != 0 {
		t.Fatal("incomplete cached fact emitted an event")
	}
}
