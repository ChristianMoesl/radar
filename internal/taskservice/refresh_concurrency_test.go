package taskservice

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type snapshotLocalSource struct {
	ref              atomic.Pointer[protocol.SourceRef]
	block            atomic.Bool
	started, release chan struct{}
	calls            atomic.Int32
}

func (*snapshotLocalSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "runtime"}
}
func (*snapshotLocalSource) Local() bool { return true }
func (s *snapshotLocalSource) ReadActivity(context.Context, protocol.Task) (protocol.Activity, error) {
	return s.ref.Load().Activity, nil
}
func (s *snapshotLocalSource) Collect(ctx context.Context, _ integration.CollectRequest) integration.CollectResult {
	s.calls.Add(1)
	ref := *s.ref.Load()
	if s.block.Swap(false) {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return integration.CollectResult{}
		}
	}
	return integration.CollectResult{Complete: true, SourceStatus: &protocol.SourceStatus{Name: "runtime", Status: "ok"}, Observations: []integration.Observation{{Ref: ref, Signal: integration.SignalInProgress}}}
}
func concurrentRefreshFixture(t *testing.T) (*fixture, *testSource, *snapshotLocalSource) {
	remote := &testSource{name: "remote", started: make(chan struct{}, 1), release: make(chan struct{})}
	local := &snapshotLocalSource{started: make(chan struct{}), release: make(chan struct{})}
	f := newFixture(t, remote, local)
	remote.ref = f.linkedRef("remote", "done")
	ref := f.linkedRef("runtime", "in_progress")
	ref.Lifecycle, ref.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
	local.ref.Store(&ref)
	return f, remote, local
}

func TestFullRefreshKeepsNewerLocalActivityAndCannotCompleteBusyWork(t *testing.T) {
	f, remote, local := concurrentRefreshFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan struct{})
	go func() { defer close(finished); f.service.Refresh(ctx, false) }()
	t.Cleanup(func() { cancel(); <-finished })
	wait(t, remote.started)
	fresh := *local.ref.Load()
	fresh.Activity = protocol.ActivityBusy
	local.ref.Store(&fresh)
	f.service.Refresh(ctx, true)
	if f.store.Tasks()[0].Activity != protocol.ActivityBusy {
		t.Fatal("local activity did not publish while remote was blocked")
	}
	close(remote.release)
	wait(t, finished)
	task := f.store.Tasks()[0]
	if task.Activity != protocol.ActivityBusy || task.Attention == "done" {
		t.Fatalf("remote completion overwrote current busy activity: %+v", task)
	}
	if local.calls.Load() != 2 {
		t.Fatal("remote phase collected local sources again")
	}
}

func TestLocalReadOverlappingFullReconciliationCannotUndoCompletion(t *testing.T) {
	f, remote, local := concurrentRefreshFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fullDone, localDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(fullDone); f.service.Refresh(ctx, false) }()
	wait(t, remote.started)
	local.block.Store(true)
	go func() { defer close(localDone); f.service.Refresh(ctx, true) }()
	t.Cleanup(func() { cancel(); <-fullDone; <-localDone })
	wait(t, local.started)
	close(remote.release)
	wait(t, fullDone)
	if f.store.Tasks()[0].Attention != "done" {
		t.Fatal("fixture did not complete during full reconciliation")
	}
	close(local.release)
	wait(t, localDone)
	if f.store.Tasks()[0].Attention != "done" {
		t.Fatal("pre-completion local read revived completed work")
	}
}

func TestFullRefreshCannotCertifyLocalReadSupersededBeforeRemotePhase(t *testing.T) {
	f, remote, local := concurrentRefreshFixture(t)
	close(remote.release)
	local.block.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.service.Refresh(ctx, false) }()
	t.Cleanup(func() { cancel(); <-done })
	wait(t, local.started)
	f.service.MarkResourceRefreshPending()
	close(local.release)
	wait(t, done)
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("remote phase certified a pre-mutation local snapshot")
	}
	f.service.Refresh(ctx, true)
	if f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("healthy local collection did not clear pending state")
	}
}

type partialLocalSource struct{ testSource }

func (s *partialLocalSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{SourceStatus: &protocol.SourceStatus{Name: s.name, Status: "error", Detail: "unavailable"}}
}
func TestPartialLocalSourceDoesNotSuppressFreshAuthoringBindings(t *testing.T) {
	remote := &testSource{name: "remote"}
	partial := &partialLocalSource{testSource: testSource{name: "partial", local: true}}
	f := newFixture(t, remote, partial)
	remote.ref = f.linkedRef("remote", "in_progress")
	// Binding reconciliation applies to adopted notes. Adopt this one before
	// discovering the remote contributor, as a user mute action does.
	f.mutate(t, "task-mute", &protocol.TaskMutation{TaskID: f.task.ID})
	result := f.service.Refresh(context.Background(), false)
	author, ok := authoredRef(f.store.Tasks()[0], "obsidian")
	if !ok || len(author.Bindings) != 1 || author.Bindings[0] != remote.ref.Binding() {
		t.Fatalf("unrelated local failure suppressed fresh author binding reconciliation: complete=%v bindings=%v", result.Complete, author.Bindings)
	}
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("partial resource collection falsely claimed complete freshness")
	}
}
