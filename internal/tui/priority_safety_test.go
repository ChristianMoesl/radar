package tui

import (
	"testing"

	"radar/internal/protocol"
)

func TestPriorityActionsRefuseDoneInformationalAndMissingTargets(t *testing.T) {
	for _, target := range []string{"done", "informational", "missing"} {
		for _, key := range []rune{'p', 'P'} {
			m := priorityFixture()
			switch target {
			case "done":
				m.tasks[0].Attention = "done"
			case "informational":
				m.tasks[0].SourceRefs[0].Role = protocol.SourceRefRoleInformational
			case "missing":
				m.tasks = nil
			}
			updated, cmd := m.Update(runeKey(key))
			got := updated.(model)
			if cmd != nil || got.loading || got.mode == "task_priority" {
				t.Fatalf("priority action %c escaped the %s safeguard", key, target)
			}
		}
	}
}
