package datadog

import (
	"testing"

	"radar/internal/protocol"
)

func TestMonitorLowUsesNativeTriggerAndStateEventsNotPresentation(t *testing.T) {
	value := monitor{ID: 7, Name: "Service error", Status: "Alert", LastTriggeredUnix: 1793703600, OverallStateModified: 1793703600}
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{ID: "note:one", Role: protocol.SourceRefRoleAuthoritative, Authored: true, Authority: protocol.SourceRefAuthorityPrimary, Signal: "low_priority", Metadata: map[string]string{"priority": "low"}}}}
	check := func(want string) {
		t.Helper()
		observation, ok := observationFromMonitor(connection{AppBaseURL: "https://example.com"}, []string{"Alert", "Warn"}, value)
		if !ok || len(observation.Ref.ActionableSignals) != 1 {
			t.Fatal("monitor did not reuse native trigger/state occurrence")
		}
		ref := observation.Ref
		ref.Signal = string(observation.Signal)
		task.SourceRefs = []protocol.SourceRef{task.SourceRefs[0], ref}
		view, _ := protocol.ProjectAttention(task, nil)
		if view.Attention != want || view.Priority != "low" {
			t.Fatalf("monitor projection = %+v, want %s", view, want)
		}
	}
	check("immediate")
	task.SourceRefs[0].LowPriorityBaseline = protocol.CaptureLowPriorityBaseline(task)
	check("low_priority")
	value.Name, value.Tags = "Edited title", []string{"another:tag"}
	check("low_priority")
	value.LastTriggeredUnix++
	check("immediate")
	task.SourceRefs[0].LowPriorityBaseline = protocol.CaptureLowPriorityBaseline(task)
	check("low_priority")
	value.Status, value.OverallStateModified = "Warn", value.OverallStateModified+2
	check("attention")
}
