package collector

import (
	"reflect"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestIncompleteCollectionCannotResolveLowPriorityBreakthrough(t *testing.T) {
	ref := protocol.SourceRef{ID: "remote:ABC-123", Source: "remote", Role: protocol.SourceRefRoleAuthoritative, Signal: "attention", ActionableSignals: []string{"reply:two"}}
	task := protocol.Task{Priority: "low", Attention: "attention", SourceRefs: []protocol.SourceRef{
		{ID: "note:one", Authored: true, Role: protocol.SourceRefRoleAuthoritative, Authority: protocol.SourceRefAuthorityPrimary, Metadata: map[string]string{"priority": "low"}, LowPriorityBaseline: map[string][]string{ref.ID: {"reply:one"}}}, ref,
	}}
	for _, partial := range []bool{false, true} {
		result := integration.CollectResult{}
		if partial {
			quiet := ref
			quiet.Signal = "in_progress"
			result.Observations = []integration.Observation{{Ref: quiet, Signal: integration.SignalInProgress}}
		}
		retainLowPriorityActions([]protocol.Task{task}, "remote", &result)
		if result.Complete || len(result.Observations) != 1 || !reflect.DeepEqual(result.Observations[0].Ref, ref) || result.Observations[0].Signal != integration.SignalAttention {
			t.Fatalf("incomplete collection falsely resolved action or gained completeness: %+v", result)
		}
	}
	// Confirmed resolution is still authoritative; no perpetual review latch.
	result := integration.CollectResult{Complete: true}
	retainLowPriorityActions([]protocol.Task{task}, "remote", &result)
	if len(result.Observations) != 0 {
		t.Fatal("complete collection retained a resolved action")
	}
	// Neither old deferred actions nor acknowledged actions are resurrected.
	for _, mode := range []string{"baseline", "acknowledged", "done", "hidden"} {
		previous := cloneTasks([]protocol.Task{task})
		switch mode {
		case "baseline":
			previous[0].SourceRefs[1].ActionableSignals = []string{"reply:one"}
		case "acknowledged":
			previous[0].AcknowledgementCursor = "2026-11-03T10:00:00Z"
			previous[0].SourceRefs[1].Acknowledgement = &protocol.SourceRefAcknowledgement{Cursor: previous[0].AcknowledgementCursor, FallbackSignal: "in_progress"}
		case "done":
			previous[0].Attention = "done"
		case "hidden":
			previous[0].SourceRefs[1].Presentation.Hidden = true
		}
		result := integration.CollectResult{}
		retainLowPriorityActions(previous, "remote", &result)
		if len(result.Observations) != 0 {
			t.Fatalf("%s action was fabricated as outstanding", mode)
		}
	}
}
