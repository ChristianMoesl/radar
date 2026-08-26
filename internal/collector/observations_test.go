package collector

import (
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestTaskFromObservationProjectsStandaloneSourceRef(t *testing.T) {
	got := taskFromObservation(integration.Observation{
		Ref: protocol.SourceRef{
			ID:           "jira:issue:XYZ-123",
			Busy:         true,
			Source:       "jira",
			Kind:         "issue",
			Title:        "XYZ-123 Ship integration boundary",
			URL:          "https://jira.example.test/browse/XYZ-123",
			CanonicalKey: "jira:issue:XYZ-123",
			LinkingKeys:  []string{"mark:XYZ-123"},
		},
		TargetTaskID: 42,
		Signal:       integration.SignalInProgress,
	})

	if got.Kind != "jira_issue" || got.Attention != "in_progress" || got.Reason != "jira issue" {
		t.Fatalf("task = %+v, want jira in-progress projection", got)
	}
	if len(got.SourceRefs) != 1 || got.SourceRefs[0].ID != "jira:issue:XYZ-123" {
		t.Fatalf("source refs = %+v, want jira source ref", got.SourceRefs)
	}
	if got.TargetTaskID != 42 {
		t.Fatalf("target task ID = %d, want 42", got.TargetTaskID)
	}
	if !got.Busy {
		t.Fatalf("task = %+v, want busy source activity projected", got)
	}
}

func TestTaskFromObservationPreservesProviderTaskProjection(t *testing.T) {
	got := taskFromObservation(integration.Observation{
		Ref:          protocol.SourceRef{ID: "review:7", Source: "review", Kind: "change", Title: "Ship it"},
		Signal:       integration.SignalAttention,
		Reason:       "review requested",
		TaskKind:     "requested_review",
		TaskMetadata: map[string]string{"author": "someone"},
	})
	if got.Kind != "requested_review" || got.Attention != "attention" || got.Reason != "review requested" || got.Metadata["author"] != "someone" {
		t.Fatalf("task = %+v, want provider-owned projection", got)
	}
}
