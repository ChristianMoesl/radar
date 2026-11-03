package state

import (
	"testing"

	"radar/internal/protocol"
)

func TestLowPrioritySnapshotClonesSourceOccurrencesAndBaseline(t *testing.T) {
	task := protocol.Task{SourceRefs: []protocol.SourceRef{{
		ID: "remote:ABC-123", ActionableSignals: []string{"reply:one"},
		LowPriorityBaseline: map[string][]string{"remote:ABC-123": {"reply:one"}},
	}}}
	cloned := cloneTask(task)
	cloned.SourceRefs[0].ActionableSignals[0] = "changed"
	cloned.SourceRefs[0].LowPriorityBaseline["remote:ABC-123"][0] = "changed"
	cloned.SourceRefs[0].LowPriorityBaseline["other"] = []string{"new"}
	if task.SourceRefs[0].ActionableSignals[0] != "reply:one" || task.SourceRefs[0].LowPriorityBaseline["remote:ABC-123"][0] != "reply:one" || len(task.SourceRefs[0].LowPriorityBaseline) != 1 {
		t.Fatal("mutating a served snapshot changed cached raw facts or authored Low intent")
	}
}
