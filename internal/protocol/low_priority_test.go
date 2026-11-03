package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func lowPriorityFixture() Task {
	task := Task{ID: 7, SourceRefs: []SourceRef{
		{ID: "note:one", Source: "notes", Kind: "task", Authored: true, Role: SourceRefRoleAuthoritative, Authority: SourceRefAuthorityPrimary, Signal: "low_priority", Metadata: map[string]string{"priority": "low"}},
		{ID: "remote:ABC-123", Source: "remote", Role: SourceRefRoleAuthoritative, Signal: "attention", ActionableSignals: []string{"reply:one"}},
	}}
	task.SourceRefs[0].LowPriorityBaseline = CaptureLowPriorityBaseline(task)
	return task
}

func TestLowPriorityResurfacesOnlyNewOutstandingOccurrences(t *testing.T) {
	task := lowPriorityFixture()
	check := func(want string) {
		t.Helper()
		before, _ := json.Marshal(task.SourceRefs)
		got, visible := ProjectAttention(task, nil)
		if !visible || got.Attention != want || got.Priority != "low" {
			t.Fatalf("projection = %+v; want visible %s with retained Low", got, want)
		}
		after, _ := json.Marshal(task.SourceRefs)
		if string(before) != string(after) {
			t.Fatal("priority projection changed raw source facts or baseline")
		}
		for range 3 {
			again, _ := ProjectAttention(got, nil)
			if !reflect.DeepEqual(got, again) {
				t.Fatal("polling/reprojection changed the effective category")
			}
		}
	}
	check("low_priority")
	task.SourceRefs[1].Title = "A title edit is not a new request"
	task.SourceRefs[1].Metadata = map[string]string{"updated_at": "another poll", "display": "different"}
	check("low_priority")
	task.SourceRefs[1].ActionableSignals = []string{"reply:two"}
	check("attention")
	task.SourceRefs[1].Signal = "in_progress"
	check("low_priority")
	task.SourceRefs[1].Signal = "attention"
	task.SourceRefs[1].ActionableSignals = []string{"reply:three"}
	check("attention")
	// Selecting Low again captures the current occurrence without ack effects.
	task.SourceRefs[0].LowPriorityBaseline = CaptureLowPriorityBaseline(task)
	check("low_priority")
}

func TestLowPriorityHonorsFiltersAcknowledgementAndLifecycle(t *testing.T) {
	task := lowPriorityFixture()
	task.SourceRefs[1].ActionableSignals = []string{"new:failure"}
	task.SourceRefs[1].Signal = "immediate"
	got, _ := ProjectAttention(task, nil)
	if got.Attention != "immediate" {
		t.Fatal("new immediate action did not break through")
	}
	for _, action := range []ContributionAction{ContributionMute, ContributionDeprioritize} {
		got, visible := ProjectAttention(task, func(ref SourceRef) ContributionAction {
			if ref.Source == "remote" {
				return action
			}
			return ContributionKeep
		})
		if !visible || got.Attention != "low_priority" || got.Priority != "low" {
			t.Fatalf("filtered activity broke through: %+v", got)
		}
	}
	task.AcknowledgementCursor = "2026-11-03T10:00:00Z"
	task.SourceRefs[1].Acknowledgement = &SourceRefAcknowledgement{Cursor: task.AcknowledgementCursor, FallbackSignal: "in_progress"}
	got, _ = ProjectAttention(task, nil)
	if got.Attention != "low_priority" || got.Priority != "low" {
		t.Fatal("acknowledged action continued breaking through")
	}
	task.SourceRefs[1].Acknowledgement = nil
	task.Muted = true
	got, _ = ProjectAttention(task, nil)
	if got.Attention != "immediate" || got.DisplayGroup() != "muted" || got.Priority != "low" {
		t.Fatal("new activity implicitly unmuted work or cleared its preference")
	}
	task.Attention = "done"
	got, _ = ProjectAttention(task, nil)
	if got.Attention != "done" || got.DisplayGroup() != "done" || got.Priority != "low" {
		t.Fatal("Low or activity overrode actual completion")
	}
}

func TestLowPriorityNormalAndUrgentRestoreSourceProjection(t *testing.T) {
	task := lowPriorityFixture()
	for _, preference := range []string{"normal", "urgent"} {
		task.SourceRefs[0].Metadata["priority"] = preference
		task.SourceRefs[0].Signal = "low_priority"
		want := "attention"
		if preference == "urgent" {
			task.SourceRefs[0].Signal, want = "immediate", "immediate"
		}
		got, _ := ProjectAttention(task, nil)
		if got.Priority != preference || got.Attention != want {
			t.Fatalf("%s did not restore source-derived projection: %+v", preference, got)
		}
	}
}

func TestLowPriorityBaselineUsesStableOccurrencesNotPresentation(t *testing.T) {
	task := lowPriorityFixture()
	task.SourceRefs[1].ActionableSignals = []string{"b", "", "a", "b"}
	baseline := CaptureLowPriorityBaseline(task)
	if !reflect.DeepEqual(baseline["remote:ABC-123"], []string{"a", "b", "reply:one"}) || len(task.SourceRefs[1].ActionableSignals) != 4 {
		t.Fatal("capture was nondeterministic or mutated provider-owned occurrences")
	}
	task.SourceRefs[1].ActionableSignals = nil
	task.SourceRefs[1].Acknowledgement = &SourceRefAcknowledgement{Cursor: "2026-11-03T10:00:00Z"}
	baseline = CaptureLowPriorityBaseline(task)
	if !reflect.DeepEqual(baseline["remote:ABC-123"], []string{"ack:2026-11-03T10:00:00Z", "reply:one"}) {
		t.Fatal("existing acknowledgement identity was not reused")
	}
}

func TestLowRedeferralRetainsKnownOccurrencesAcrossMissingAndQuietSources(t *testing.T) {
	task := lowPriorityFixture()
	original := task.SourceRefs[1]
	task.SourceRefs = task.SourceRefs[:1]
	task.SourceRefs[0].LowPriorityBaseline = CaptureLowPriorityBaseline(task)
	task.SourceRefs = append(task.SourceRefs, original)
	view, _ := ProjectAttention(task, nil)
	if view.Attention != "low_priority" {
		t.Fatal("re-deferral while a source was missing forgot its known activity")
	}
	task.SourceRefs[1].Signal = "in_progress"
	task.SourceRefs[0].LowPriorityBaseline = CaptureLowPriorityBaseline(task)
	task.SourceRefs[1].Signal = "attention"
	view, _ = ProjectAttention(task, nil)
	if view.Attention != "low_priority" {
		t.Fatal("a quiet snapshot erased a previously deferred occurrence")
	}
	task.SourceRefs[1].ActionableSignals = []string{"reply:second"}
	view, _ = ProjectAttention(task, nil)
	if view.Attention != "attention" {
		t.Fatal("retaining old deferral suppressed a genuinely new occurrence")
	}
}
