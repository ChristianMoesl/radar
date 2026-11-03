package protocol

import (
	"reflect"
	"testing"
)

func TestLowPromptBreakthroughsDoNotReinterpretOrdinaryRuntimeActivity(t *testing.T) {
	task := lowPriorityFixture()
	ref := SourceRef{ID: "tmux:one", Role: SourceRefRoleAuthoritative, Signal: "in_progress", Activity: ActivityWaiting, ActionableSignals: []string{"prompt:pane:first"}}
	task.SourceRefs = append(task.SourceRefs[:1], ref)
	task.SourceRefs[0].Metadata["priority"] = "normal"
	normal, _ := ProjectAttention(task, nil)
	if normal.Attention != "in_progress" {
		t.Fatal("normal runtime activity changed category")
	}
	task.SourceRefs[0].LowPriorityBaseline = CaptureLowPriorityBaseline(task)
	task.SourceRefs[0].Metadata["priority"] = "low"
	check := func(want string) {
		t.Helper()
		view, _ := ProjectAttention(task, nil)
		if view.Attention != want || view.Priority != "low" || !reflect.DeepEqual(view.SourceRefs, task.SourceRefs) {
			t.Fatalf("prompt projection = %+v, want %s with unchanged raw facts", view, want)
		}
	}
	check("low_priority")
	task.SourceRefs[1].Activity = ActivityBusy
	check("low_priority")
	task.SourceRefs[1].Activity = ActivityIdle
	check("low_priority")
	task.SourceRefs[1].Activity = ActivityWaiting
	check("low_priority") // Re-publishing the same prompt is not a new request.
	task.SourceRefs[1].ActionableSignals = []string{"prompt:pane:second"}
	check("attention")
	task.SourceRefs[1].Activity = ActivityBusy
	check("low_priority")
	task.SourceRefs[1].Activity = ActivityWaiting
	task.SourceRefs[1].ActionableSignals = nil
	check("low_priority") // Missing identity is not evidence of a new prompt.
}
