package collector

import (
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestTaskFromObservationProjectsStandaloneSourceRef(t *testing.T) {
	got := taskFromObservation(integration.Observation{
		Ref: protocol.SourceRef{
			ID:           "jira:issue:RAD-123",
			Source:       "jira",
			Kind:         "issue",
			Title:        "RAD-123 Ship integration boundary",
			URL:          "https://jira.example.test/browse/RAD-123",
			CanonicalKey: "jira:issue:RAD-123",
			LinkingKeys:  []string{"ticket:RAD-123"},
		},
		Signal: integration.SignalInProgress,
	})

	if got.Kind != "jira_issue" || got.Attention != "in_progress" || got.Reason != "jira issue" {
		t.Fatalf("task = %+v, want jira in-progress projection", got)
	}
	if len(got.SourceRefs) != 1 || got.SourceRefs[0].ID != "jira:issue:RAD-123" {
		t.Fatalf("source refs = %+v, want jira source ref", got.SourceRefs)
	}
}

func TestTaskFromObservationPreservesGitHubTaskCategories(t *testing.T) {
	tests := []struct {
		name   string
		signal integration.WorkSignal
		reason string
		want   string
	}{
		{name: "review", signal: integration.SignalAttention, reason: "review requested", want: "github_review_request"},
		{name: "activity", signal: integration.SignalAttention, reason: "1 unresolved review thread(s)", want: "github_pr_activity"},
		{name: "authored", signal: integration.SignalInProgress, reason: "open PR", want: "github_own_pr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskFromObservation(integration.Observation{
				Ref:    protocol.SourceRef{ID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request", Title: "Ship it", Repo: "acme/app", URL: "https://github.com/acme/app/pull/7"},
				Signal: tt.signal,
				Reason: tt.reason,
			})
			if got.Kind != tt.want || got.Attention != string(tt.signal) || got.Reason != tt.reason {
				t.Fatalf("task = %+v, want kind %s", got, tt.want)
			}
		})
	}
}
