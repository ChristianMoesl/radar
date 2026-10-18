package taskservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

type handoffRuntime struct {
	testSource
	activity  protocol.Activity
	readError error
	observed  *protocol.Activity
}

func (s *handoffRuntime) ReadActivity(context.Context, protocol.Task) (protocol.Activity, error) {
	return s.activity, s.readError
}
func (s *handoffRuntime) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	result := s.testSource.Collect(ctx, req)
	result.Observations[0].Ref.Activity = s.activity
	if s.observed != nil {
		result.Observations[0].Ref.Activity = *s.observed
	}
	return result
}
func newHandoffFixture(t *testing.T) (*fixture, *testSource, *handoffRuntime) {
	remote := &testSource{name: "remote"}
	runtime := &handoffRuntime{testSource: testSource{name: "runtime", local: true}}
	f := newFixture(t, remote, runtime)
	remote.ref = f.linkedRef("remote", "in_progress")
	runtime.ref = f.linkedRef("runtime", "in_progress")
	runtime.ref.Lifecycle, runtime.ref.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
	runtime.ref.RetainInactive = false
	return f, remote, runtime
}
func handoffTask(t *testing.T, f *fixture) protocol.Task {
	t.Helper()
	tasks := f.store.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v", tasks)
	}
	return tasks[0]
}
func assertHandoff(t *testing.T, f *fixture, attention string) protocol.Task {
	t.Helper()
	task := handoffTask(t, f)
	ref, found := authoredRef(task, "obsidian")
	if !found || !ref.AgentReview || ref.Signal == "done" || task.Attention != attention || task.DoneAt != "" {
		t.Fatalf("handoff = %+v, note = %+v, want %s", task, ref, attention)
	}
	data, err := os.ReadFile(ref.Metadata["note_path"])
	if err != nil || !strings.Contains(string(data), "radar-agent-review: true") || !strings.Contains(string(data), "radar-state: open") {
		t.Fatalf("handoff not durable: %s %v", data, err)
	}
	return task
}

func TestCompletionCollisionRequiresExplicitDoneAfterIdleAndRestart(t *testing.T) {
	for _, activity := range []protocol.Activity{protocol.ActivityBusy, protocol.ActivityWaiting} {
		t.Run(activity.String(), func(t *testing.T) {
			f, remote, runtime := newHandoffFixture(t)
			f.service.Refresh(context.Background(), false)
			runtime.activity, remote.ref.Signal = activity, "done"
			f.service.Refresh(context.Background(), false)
			held := assertHandoff(t, f, "in_progress")
			author, _ := authoredRef(held, "obsidian")
			bound := false
			for _, binding := range author.Bindings {
				bound = bound || binding == remote.ref.Binding()
			}
			if !bound {
				t.Fatal("handoff did not durably retain the completed contributor identity")
			}
			if held.Activity != activity {
				t.Fatalf("activity = %s", held.Activity)
			}
			if _, err := f.service.MutateTask(context.Background(), "task-done", &protocol.TaskMutation{TaskID: held.ID}); err == nil {
				t.Fatal("completed while agent active")
			}
			runtime.activity = protocol.ActivityIdle
			f.service.Refresh(context.Background(), true)
			ready := assertHandoff(t, f, "attention")
			if ready.Reason != "Agent finished — review result" {
				t.Fatalf("reason = %s", ready.Reason)
			}
			f.store.Acknowledge("1") // Existing acknowledgements must not dismiss the result.
			f.service.Refresh(context.Background(), false)
			assertHandoff(t, f, "attention")
			store, err := state.NewStore(f.service.logger)
			if err != nil {
				t.Fatal(err)
			}
			f.store, f.service = store, New(store, f.service.logger, f.service.integrations)
			assertHandoff(t, f, "attention")
			// Cache loss reconstructs the hold from the canonical note.
			if err := f.service.Reset(); err != nil {
				t.Fatal(err)
			}
			f.service.Refresh(context.Background(), false)
			ready = assertHandoff(t, f, "attention")
			// A new completed contributor must not bypass the existing review hold.
			remote.ref.ID += ":new"
			f.service.Refresh(context.Background(), false)
			ready = assertHandoff(t, f, "attention")
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: ready.ID})
			ref, _ := authoredRef(done, "obsidian")
			if done.Attention != "done" || done.DoneAt == "" || ref.AgentReview {
				t.Fatalf("explicit Done = %+v", done)
			}
			f.service.Refresh(context.Background(), false)
			if handoffTask(t, f).Attention != "done" {
				t.Fatal("idle refresh undid explicit Done")
			}
		})
	}
}

func TestLocalActivityReopensCompletedNoteWithoutRemoteRefresh(t *testing.T) {
	f, remote, runtime := newHandoffFixture(t)
	f.service.Refresh(context.Background(), false)
	done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
	remoteCalls := remote.calls.Load()
	runtime.activity = protocol.ActivityBusy
	f.service.Refresh(context.Background(), true)
	assertHandoff(t, f, "in_progress")
	if remote.calls.Load() != remoteCalls {
		t.Fatal("activity reopen waited for remote collection")
	}
	called := false
	if err := f.service.GuardCleanup(context.Background(), done, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("stale cleanup ran after activity reopen")
	}
	runtime.activity = protocol.ActivityIdle
	f.service.Refresh(context.Background(), true)
	assertHandoff(t, f, "attention")
}

func TestFreshActivityGuardsManualCompletionAndStaleCleanup(t *testing.T) {
	for _, kind := range []string{"busy", "waiting", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			f, _, runtime := newHandoffFixture(t)
			f.service.Refresh(context.Background(), false)
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			// No cache refresh between the preview/completion and the new run.
			if kind == "unavailable" {
				runtime.readError = errors.New("runtime unavailable")
			} else {
				runtime.activity = protocol.Activity(kind)
			}
			if _, err := f.service.MutateTask(context.Background(), "task-done", &protocol.TaskMutation{TaskID: done.ID}); err == nil {
				t.Fatal("completion trusted cached idle")
			}
			called := false
			if err := f.service.GuardCleanup(context.Background(), done, func() error { called = true; return nil }); err == nil || called {
				t.Fatal("cleanup trusted cached idle")
			}
		})
	}
}

func TestActivityReadFailureDoesNotAuthorizeAutomaticCompletion(t *testing.T) {
	f, remote, runtime := newHandoffFixture(t)
	f.service.Refresh(context.Background(), false)
	remote.ref.Signal = "done"
	runtime.readError = errors.New("activity unavailable")
	result := f.service.Refresh(context.Background(), false)
	if handoffTask(t, f).Attention == "done" {
		t.Fatal("failed activity read treated as idle")
	}
	reported := false
	for _, source := range result.Sources {
		reported = reported || (source.Name == "obsidian" && source.Status == "error")
	}
	if !reported {
		t.Fatal("completion failure was hidden")
	}
	runtime.readError = nil
	f.service.Refresh(context.Background(), false)
	if handoffTask(t, f).Attention != "done" {
		t.Fatal("idle task no longer auto completes")
	}
}

func TestOrdinaryBusyRunDoesNotCreateReviewHold(t *testing.T) {
	f, _, runtime := newHandoffFixture(t)
	runtime.activity = protocol.ActivityBusy
	f.service.Refresh(context.Background(), false)
	runtime.activity = protocol.ActivityIdle
	f.service.Refresh(context.Background(), true)
	task := handoffTask(t, f)
	ref, _ := authoredRef(task, "obsidian")
	if ref.AgentReview || task.Attention == "attention" {
		t.Fatalf("ordinary turn introduced a handoff: %+v", task)
	}
}

func TestHandoffPreservesMuteUrgencyAndRemoteFacts(t *testing.T) {
	f, remote, runtime := newHandoffFixture(t)
	runtime.activity, remote.ref.Signal = protocol.ActivityBusy, "done"
	f.service.Refresh(context.Background(), false)
	runtime.activity = protocol.ActivityIdle
	f.service.Refresh(context.Background(), true)
	ready := assertHandoff(t, f, "attention")
	muted := f.mutate(t, "task-mute", &protocol.TaskMutation{TaskID: ready.ID})
	if muted.DisplayGroup() != "muted" {
		t.Fatal("handoff overrides mute")
	}
	urgent := f.mutate(t, "task-priority", &protocol.TaskMutation{TaskID: ready.ID, Priority: "urgent"})
	if urgent.Attention != "immediate" || urgent.DisplayGroup() != "muted" {
		t.Fatalf("urgency/mute = %+v", urgent)
	}
	for _, ref := range urgent.SourceRefs {
		if ref.ID == remote.ref.ID && ref.Signal == "done" {
			return
		}
	}
	t.Fatal("remote completion fact was overwritten")
}

func TestHandoffPersistenceFailureRemainsSafeAfterIdleAndRestart(t *testing.T) {
	for _, alreadyDone := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked completion", true: "reopen failure"}[alreadyDone], func(t *testing.T) {
			f, remote, runtime := newHandoffFixture(t)
			f.service.Refresh(context.Background(), false)
			if alreadyDone {
				f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			}
			remote.ref.Signal, runtime.activity = "done", protocol.ActivityBusy
			f.author.reconcileError = errors.New("note write failed")
			f.service.Refresh(context.Background(), false)
			task := handoffTask(t, f)
			if !task.AgentReviewPending || task.Attention == "done" {
				t.Fatalf("failed write lost handoff: %+v", task)
			}
			runtime.activity = protocol.ActivityIdle
			f.service.Refresh(context.Background(), true)
			store, err := state.NewStore(f.service.logger)
			if err != nil {
				t.Fatal(err)
			}
			f.store, f.service = store, New(store, f.service.logger, f.service.integrations)
			task = handoffTask(t, f)
			if !task.AgentReviewPending || task.Attention != "attention" {
				t.Fatalf("restart forgot failed handoff: %+v", task)
			}
			called := false
			if err := f.service.GuardCleanup(context.Background(), task, func() error { called = true; return nil }); err == nil || called {
				t.Fatal("failed handoff permitted cleanup")
			}
			if err := f.service.Reset(); err != nil {
				t.Fatal(err)
			}
			f.service.Refresh(context.Background(), false)
			if !handoffTask(t, f).AgentReviewPending {
				t.Fatal("cache reset silently discarded pending write")
			}
			f.author.reconcileError = nil
			f.service.Refresh(context.Background(), true)
			saved := assertHandoff(t, f, "attention")
			if saved.AgentReviewPending {
				t.Fatal("successful persistence left retry pending")
			}
		})
	}
}

func TestAutomaticCompletionRechecksActivityAfterCollection(t *testing.T) {
	f, remote, runtime := newHandoffFixture(t)
	idle := protocol.ActivityIdle
	runtime.observed = &idle // Collection saw idle; a run began before reconciliation.
	remote.ref.Signal, runtime.activity = "done", protocol.ActivityBusy
	f.service.Refresh(context.Background(), false)
	task := handoffTask(t, f)
	ref, _ := authoredRef(task, "obsidian")
	if !ref.AgentReview || task.Attention == "done" {
		t.Fatal("automatic completion trusted stale idle snapshot")
	}
}

// Freeze only the first collected runtime snapshot, allowing a mutation's
// verification/recollection to proceed while an older refresh is in flight.
type delayedRuntime struct {
	handoffRuntime
	freeze  atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (s *delayedRuntime) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	result := s.handoffRuntime.Collect(ctx, req)
	if s.freeze.CompareAndSwap(true, false) {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	}
	return result
}

func TestStaleBusyRefreshCannotUndoExplicitDone(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "local"}[local], func(t *testing.T) {
			remote := &testSource{name: "remote"}
			runtime := &delayedRuntime{handoffRuntime: handoffRuntime{testSource: testSource{name: "runtime", local: true}}, started: make(chan struct{}), release: make(chan struct{})}
			f := newFixture(t, remote, runtime)
			remote.ref = f.linkedRef("remote", "done")
			runtime.ref = f.linkedRef("runtime", "in_progress")
			runtime.ref.Lifecycle, runtime.ref.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
			runtime.activity = protocol.ActivityBusy
			f.service.Refresh(context.Background(), false)
			assertHandoff(t, f, "in_progress")
			runtime.freeze.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			finished := make(chan struct{})
			go func() { defer close(finished); f.service.Refresh(ctx, local) }()
			wait(t, runtime.started)
			runtime.activity = protocol.ActivityIdle
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			if done.Attention != "done" {
				t.Fatalf("explicit Done failed: %+v", done)
			}
			close(runtime.release)
			wait(t, finished)
			final := handoffTask(t, f)
			if final.Attention != "done" || final.AgentReviewPending || final.DoneAt != done.DoneAt {
				t.Fatalf("stale busy refresh undid Done: %+v", final)
			}
		})
	}
}

type handoffCleanup struct {
	path          string
	beforeExecute func()
	executed      bool
}

func (p *handoffCleanup) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "cleanup-test"}
}
func (p *handoffCleanup) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	p.beforeExecute()
	return []protocol.CleanupTarget{{Source: "cleanup-test", Path: p.path, WorkspaceID: "workspace-test", ProvidesWorkspace: true}}, nil
}
func (p *handoffCleanup) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	p.executed = true
	return req.Target, nil
}

func TestExpiredGCRechecksActivityAfterPreview(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "new busy run", true: "unavailable activity"}[unavailable], func(t *testing.T) {
			f, _, runtime := newHandoffFixture(t)
			f.service.Refresh(context.Background(), false)
			root := t.TempDir()
			path := filepath.Join(root, "completed")
			ref := protocol.SourceRef{ID: "workspace-test", Source: "workspace", Kind: "workspace", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace, Authority: protocol.SourceRefAuthorityNone, Path: path, ProvidesWorkspace: true, WorkspaceID: "workspace-test", LinkingKeys: []string{f.task.SourceRefs[0].ID}}
			f.store.SetTasksForSources([]protocol.Task{{Attention: "in_progress", SourceRefs: []protocol.SourceRef{ref}}}, []string{"workspace"})
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			doneAt, err := time.Parse(time.RFC3339, done.DoneAt)
			if err != nil {
				t.Fatal(err)
			}
			provider := &handoffCleanup{path: path, beforeExecute: func() {
				if unavailable {
					runtime.readError = errors.New("unavailable")
				} else {
					runtime.activity = protocol.ActivityBusy
				}
			}}
			result, err := workspacegc.Run(context.Background(), f.store, cleanup.New([]integration.CleanupProvider{provider}), f.service.logger, doneAt.Add(9*24*time.Hour), workspacegc.Options{WorkspaceRoot: root, GuardExecution: f.service.GuardCleanup})
			if err != nil || provider.executed || len(result.Deleted) != 0 || len(result.Skipped) != 1 {
				t.Fatalf("expired GC ignored activity: %+v %v executed=%v", result, err, provider.executed)
			}
			if !strings.Contains(result.Skipped[0].Reason, "activity") && !strings.Contains(result.Skipped[0].Reason, "agent busy") {
				t.Fatalf("GC skipped for an unrelated reason: %+v", result.Skipped)
			}
		})
	}
}
