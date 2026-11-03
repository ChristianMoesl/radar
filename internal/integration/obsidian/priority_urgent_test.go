package obsidian

import (
	"context"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestLoweringUrgencyDoesNotPredeferAFutureResultHandoff(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "Future result"); err != nil {
		t.Fatal(err)
	}
	collect := func() protocol.SourceRef {
		t.Helper()
		result := source.Collect(ctx, integration.CollectRequest{})
		if !result.Complete || len(result.Observations) != 1 {
			t.Fatal("authored task unavailable")
		}
		ref := result.Observations[0].Ref
		ref.Signal = string(result.Observations[0].Signal)
		return ref
	}
	ref := collect()
	if _, err := source.SetPriority(ctx, protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, "urgent"); err != nil {
		t.Fatal(err)
	}
	ref = collect()
	if ref.Signal != "immediate" || ref.AgentReview {
		t.Fatal("fixture is not urgent work without a review hold")
	}
	if _, err := source.SetPriority(ctx, protocol.Task{Attention: "immediate", SourceRefs: []protocol.SourceRef{ref}}, "low"); err != nil {
		t.Fatal(err)
	}
	ref = collect()
	observation, err := source.ReconcileLifecycle(ctx, ref, integration.TaskLifecycleRequest{RequireAgentReview: true, Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
	if err != nil || observation == nil {
		t.Fatal("new handoff unavailable", err)
	}
	view, _ := protocol.ProjectAttention(protocol.Task{SourceRefs: []protocol.SourceRef{observation.Ref}}, nil)
	if view.Attention != "attention" || view.Priority != "low" || !observation.Ref.AgentReview {
		t.Fatalf("clearing urgency pre-deferred a future result review: %+v", view)
	}
}
