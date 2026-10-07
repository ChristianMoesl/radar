package protocol

import (
	"reflect"
	"testing"
)

func TestAcknowledgementOnlyChangesItsOwnContribution(t *testing.T) {
	feedback := SourceRef{ID: "feedback", Source: "remote", Kind: "feedback", Role: SourceRefRoleAuthoritative, Signal: "attention", Status: "feedback",
		Acknowledgement: &SourceRefAcknowledgement{Cursor: "2026-10-01T10:00:00Z", FallbackSignal: "in_progress", FallbackReason: "open PR"}}
	for _, otherSignal := range []string{"immediate", "attention", "in_progress", "low_priority"} {
		other := SourceRef{ID: "other", Source: "other", Kind: "work", Role: SourceRefRoleAuthoritative, Signal: otherSignal, Status: "other work"}
		task := Task{Attention: "attention", AcknowledgementCursor: "2026-10-01T11:00:00Z", SourceRefs: []SourceRef{feedback, other}}
		got, visible := ProjectAttention(task, func(ref SourceRef) ContributionAction {
			if ref.ID == feedback.ID {
				return ContributionMute
			}
			return ContributionKeep
		})
		if !visible || got.Attention != otherSignal || got.Reason != "other work" || !reflect.DeepEqual(got.SourceRefs, task.SourceRefs) {
			t.Fatalf("muted acknowledged ref affected other source: %+v", got)
		}
		if otherSignal == "attention" || otherSignal == "immediate" {
			got, visible = ProjectAttention(task, nil)
			if !visible || got.Attention != otherSignal {
				t.Fatalf("acknowledgement capped other source: %+v", got)
			}
		}
	}
}

func TestHiddenAndAcknowledgedContributionsCannotHideOtherWork(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		ref := SourceRef{ID: "feedback", Role: SourceRefRoleAuthoritative, Signal: "attention", Presentation: SourceRefPresentation{Hidden: hidden},
			Acknowledgement: &SourceRefAcknowledgement{Cursor: "old", HideWhenAcknowledged: true}}
		task := Task{Attention: "attention", AcknowledgementCursor: "seen", SourceRefs: []SourceRef{ref}}
		if _, visible := ProjectAttention(task, nil); visible {
			t.Fatal("hidden/acknowledged source visible alone")
		}
		task.SourceRefs = append(task.SourceRefs, SourceRef{ID: "note", Source: "notes", Role: SourceRefRoleAuthoritative, Signal: "low_priority", Status: "open note"})
		if got, visible := ProjectAttention(task, nil); !visible || got.Attention != "low_priority" || got.Reason != "open note" {
			t.Fatalf("note hidden by acknowledgement: %+v", got)
		}
	}
}

func TestContributionPolicyDoesNotInheritStaleAttentionOrChangeDone(t *testing.T) {
	pr := SourceRef{ID: "pr", Role: SourceRefRoleAuthoritative, Signal: "attention"}
	resource := SourceRef{ID: "local", Role: SourceRefRoleAuthoritative, ProvidesWorkspace: true}
	task := Task{Attention: "attention", Reason: "stale PR reason", SourceRefs: []SourceRef{pr, resource}}
	policy := func(ref SourceRef) ContributionAction {
		if ref.ID == "pr" {
			return ContributionMute
		}
		return ContributionKeep
	}
	got, visible := ProjectAttention(task, policy)
	if !visible || got.Attention != "low_priority" || got.Reason == task.Reason {
		t.Fatalf("resource inherited muted signal: %+v", got)
	}
	task.Attention, task.Reason = "done", "completed"
	if got, visible = ProjectAttention(task, policy); !visible || got.Attention != "done" || got.Reason != "completed" {
		t.Fatalf("done changed: %+v", got)
	}
}

func TestContributionReasonNeverFallsBackToAnotherSourcesProjection(t *testing.T) {
	for _, action := range []ContributionAction{ContributionMute, ContributionDeprioritize} {
		for _, signal := range []string{"in_progress", "attention"} {
			t.Run(string(action)+"/"+signal, func(t *testing.T) {
				task := Task{Attention: signal, Reason: "PR activity", SourceRefs: []SourceRef{
					{ID: "pr", Source: "github", Kind: "pull_request", Role: SourceRefRoleAuthoritative, Signal: signal, Status: "PR activity"},
					{ID: "local", Source: "workspace", Kind: "workspace", Role: SourceRefRoleAuthoritative, Signal: signal},
				}}
				// State projects without policy first; integration policy then reprojects.
				base, _ := ProjectAttention(task, nil)
				policy := func(ref SourceRef) ContributionAction {
					if ref.ID == "pr" {
						return action
					}
					return ContributionKeep
				}
				got, visible := ProjectAttention(base, policy)
				if !visible || got.AttentionSourceRefID != "local" || got.Attention != signal || got.Reason != "workspace workspace" {
					t.Fatalf("local work inherited suppressed PR reason: %+v", got)
				}
				again, _ := ProjectAttention(got, policy)
				if !reflect.DeepEqual(got, again) || !reflect.DeepEqual(got.SourceRefs, task.SourceRefs) {
					t.Fatal("projection changed source facts or was not idempotent")
				}
			})
		}
	}
}
