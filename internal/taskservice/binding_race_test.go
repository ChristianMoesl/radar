package taskservice

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
)

// bindingRaceSource discovers a second contributor only in the blocked refresh.
// After reset, normal search is empty and only persisted bindings can recover it.
type bindingRaceSource struct {
	normal   []protocol.SourceRef
	resolved []protocol.SourceRef
	started  chan struct{}
	release  chan struct{}
	calls    atomic.Int32
	requests []protocol.SourceBinding
}

func (*bindingRaceSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "remote", Label: "Temporary remote"}
}

func (s *bindingRaceSource) Collect(ctx context.Context, _ integration.CollectRequest) integration.CollectResult {
	s.calls.Add(1)
	s.started <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
		return integration.CollectResult{}
	}
	return bindingRaceObservations(s.normal)
}

func (s *bindingRaceSource) ResolveBindings(_ context.Context, req integration.BindingRequest) integration.CollectResult {
	s.requests = append(s.requests, req.Bindings...)
	refs := make([]protocol.SourceRef, 0)
	for _, binding := range req.Bindings {
		for _, ref := range s.resolved {
			if binding == ref.Binding() {
				refs = append(refs, ref)
			}
		}
	}
	return bindingRaceObservations(refs)
}

func bindingRaceObservations(refs []protocol.SourceRef) integration.CollectResult {
	result := integration.CollectResult{Complete: true}
	for _, ref := range refs {
		result.Observations = append(result.Observations, integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal)})
	}
	return result
}

func TestMutationFencedRefreshPersistsNewBindingsBeforeImmediateReset(t *testing.T) {
	for _, tc := range []struct {
		name, staleSignal, wantState string
		beforeRefresh, afterMute     string
	}{
		{name: "mute", staleSignal: "done", wantState: "open"},
		{name: "manual done", staleSignal: "in_progress", wantState: "done", afterMute: "task-done"},
		{name: "manual reopen", staleSignal: "done", wantState: "open", beforeRefresh: "task-done", afterMute: "task-reopen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &bindingRaceSource{started: make(chan struct{}, 8), release: make(chan struct{})}
			// The author is a real Obsidian provider rooted entirely in temporary
			// config/vault/workspace/state paths created by newFixture.
			f := newFixture(t, source)
			first := f.linkedRef("remote", tc.staleSignal)
			second := first
			second.ID, second.EntityID, second.CanonicalKey = "remote:second", "remote:second", "remote:second"
			f.store.SetTasksForSources([]protocol.Task{{Attention: first.Signal, SourceRefs: []protocol.SourceRef{first}}}, []string{"remote"})
			if tc.beforeRefresh != "" {
				f.mutate(t, tc.beforeRefresh, &protocol.TaskMutation{TaskID: f.task.ID})
			}
			source.normal = []protocol.SourceRef{first, second}
			for len(f.author.collected) > 0 {
				<-f.author.collected
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			finished := make(chan collector.Result, 1)
			joined := make(chan struct{})
			go func() {
				defer close(joined)
				finished <- f.service.Refresh(ctx, false)
			}()
			released := false
			defer func() {
				if !released {
					close(source.release)
				}
				<-joined
			}()
			wait(t, source.started)
			wait(t, f.author.collected) // stale author snapshot has already been captured

			muted := f.mutate(t, "task-mute", &protocol.TaskMutation{TaskID: f.task.ID})
			ref, ok := authoredRef(muted, "obsidian")
			if !ok || len(ref.Bindings) != 1 || ref.Bindings[0] != first.Binding() {
				t.Fatalf("mute must initially know only the cached first contributor: %+v", ref)
			}
			if tc.afterMute != "" {
				muted = f.mutate(t, tc.afterMute, &protocol.TaskMutation{TaskID: f.task.ID})
			}
			mutatedRef, _ := authoredRef(muted, "obsidian")
			completedAt := mutatedRef.Metadata["completed_at"]
			close(source.release)
			released = true
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("blocked refresh did not finish")
			}
			<-joined
			if source.calls.Load() != 1 {
				t.Fatal("mutation or binding reconciliation re-collected the remote source")
			}
			current, ok := taskByID(f.store.Tasks(), f.task.ID)
			ref, authored := authoredRef(current, "obsidian")
			if !ok || !authored || !current.Muted || ref.Metadata["state"] != tc.wantState || ref.Metadata["completed_at"] != completedAt {
				t.Fatalf("stale lifecycle undid the mutation: %+v", current)
			}
			if len(ref.Bindings) != 2 {
				t.Fatalf("new contributor was published but not durably bound: %+v", ref.Bindings)
			}
			for _, want := range []protocol.SourceBinding{first.Binding(), second.Binding()} {
				found := false
				for _, binding := range ref.Bindings {
					found = found || binding == want
				}
				if !found {
					t.Fatalf("binding %v missing from authored note: %+v", want, ref.Bindings)
				}
			}
			// Reopen's pending completion baseline is also authored intent: a
			// bindings-only reconciliation must not arm it with stale done facts.
			if tc.afterMute == "task-reopen" {
				data, err := os.ReadFile(ref.Metadata["note_path"])
				if err != nil || !strings.Contains(string(data), "radar-completion-baseline: pending") {
					t.Fatalf("stale lifecycle changed the manual-reopen baseline: %s, %v", data, err)
				}
			}

			// Reset immediately, with no intervening full refresh to repair any
			// omitted binding. Both sources now disappear from normal discovery.
			if err := f.service.Reset(); err != nil {
				t.Fatal(err)
			}
			source.normal = nil
			first.Signal, second.Signal = "done", "in_progress"
			first.LinkingKeys, second.LinkingKeys = nil, nil
			source.resolved = []protocol.SourceRef{first, second}
			f.service.Refresh(ctx, false)
			tasks := f.store.Tasks()
			if len(tasks) != 1 || !tasks[0].Muted || tasks[0].Attention == "done" || len(tasks[0].SourceRefs) != 3 || len(source.requests) != 2 {
				t.Fatalf("immediate reset detached the new contributor: tasks=%+v requests=%+v", tasks, source.requests)
			}
			persisted := f.author.Source.Collect(ctx, integration.CollectRequest{})
			if !persisted.Complete || len(persisted.Observations) != 1 || len(persisted.Observations[0].Ref.Bindings) != 2 || !persisted.Observations[0].Ref.Muted || persisted.Observations[0].Ref.Status != "open" {
				t.Fatalf("reset lost canonical note or unsupportedly completed it: %+v", persisted)
			}
		})
	}
}
