package github

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
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
  *"reviewQuery"*) echo '{"data":{"viewer":{"login":"me"},"reviewRequested":{"nodes":[]},"authored":{"nodes":[]},"participated":{"nodes":[]}}}' ;;
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
