package taskservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func sourceOnlyService(t *testing.T, sources ...integration.Integration) *Service {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	return New(store, logger, integration.NewRegistry(sources...))
}

func sourceOnlyTask(t *testing.T, s *Service) protocol.Task {
	t.Helper()
	tasks := s.store.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected one source-only task, got %+v", tasks)
	}
	return tasks[0]
}

func mutateSourceOnlyTask(t *testing.T, s *Service, method string, id int) protocol.Task {
	t.Helper()
	task, err := s.MutateTask(context.Background(), method, &protocol.TaskMutation{TaskID: id})
	if err != nil || task.ID != id {
		t.Fatalf("%s: task=%+v error=%v", method, task, err)
	}
	return task
}

func sourceOnlyWorkItem() *testSource {
	return &testSource{name: "remote", ref: protocol.SourceRef{
		ID: "remote:issue:ABC-42", Source: "remote", Kind: "issue", Title: "External issue",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem,
		Authority: protocol.SourceRefAuthorityContributing, Signal: "in_progress", WorkflowState: "in review", RetainInactive: true,
	}}
}

func TestSourceOnlyDoneAndReopenSurviveRestartAndRefresh(t *testing.T) {
	ctx := context.Background()
	remote := sourceOnlyWorkItem()
	s := sourceOnlyService(t, remote)
	s.Refresh(ctx, false)
	id := sourceOnlyTask(t, s).ID
	done := mutateSourceOnlyTask(t, s, "task-done", id)
	if !done.ManualDone || done.Attention != "done" || done.DoneAt == "" || remote.calls.Load() != 1 || done.SourceRefs[0].Signal != "in_progress" {
		t.Fatalf("Done must persist locally without touching the source: %+v", done)
	}
	restart := func() {
		store, err := state.NewStore(s.logger)
		if err != nil {
			t.Fatal(err)
		}
		s = New(store, s.logger, s.integrations)
	}
	assertDone := func() {
		t.Helper()
		if got := sourceOnlyTask(t, s); got.ID != id || !got.ManualDone || got.DoneAt != done.DoneAt {
			t.Fatalf("manual decision not retained: %+v", got)
		}
	}
	restart()
	assertDone()
	if got := s.store.CollectionTasks()[0]; got.Attention == "done" {
		t.Fatal("local completion must not suppress provider resolution")
	}
	remote.ref.Title, remote.ref.Signal = "Updated title", "attention"
	s.Refresh(ctx, false)
	assertDone()
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	s.Refresh(ctx, false)
	assertDone()
	remote.ref.Signal, remote.ref.WorkflowState = "done", "closed"
	s.Refresh(ctx, false)
	assertDone()

	opened := mutateSourceOnlyTask(t, s, "task-reopen", id)
	if opened.ManualDone || opened.Attention == "done" || opened.DoneAt != "" || opened.SourceRefs[0].Signal != "done" {
		t.Fatalf("Reopen must not reopen the source item: %+v", opened)
	}
	restart()
	s.Refresh(ctx, false)
	if got := sourceOnlyTask(t, s); got.Attention == "done" {
		t.Fatal("unchanged terminal source data undid Reopen")
	}
	remote.ref.Signal, remote.ref.WorkflowState = "in_progress", "in progress"
	s.Refresh(ctx, false)
	remote.ref.Signal, remote.ref.WorkflowState = "done", "closed"
	s.Refresh(ctx, false)
	if got := sourceOnlyTask(t, s); got.Attention != "done" || got.ManualDone {
		t.Fatalf("new source completion did not rearm automatic Done: %+v", got)
	}
}

func TestSourceOnlyDoneFencesInFlightRemoteWorkflow(t *testing.T) {
	remote := sourceOnlyWorkItem()
	s := sourceOnlyService(t, remote)
	s.Refresh(context.Background(), false)
	id := sourceOnlyTask(t, s).ID
	remote.ref.WorkflowState = "in progress"
	remote.started, remote.release = make(chan struct{}, 1), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s.Refresh(ctx, false)
	}()
	select {
	case <-remote.started:
	case <-ctx.Done():
		t.Fatal("remote collection did not start")
	}
	mutateSourceOnlyTask(t, s, "task-done", id)
	close(remote.release)
	<-finished
	if got := sourceOnlyTask(t, s); !got.ManualDone {
		t.Fatal("in-flight snapshot overruled the local mutation")
	}
	s.Refresh(ctx, false)
	if got := sourceOnlyTask(t, s); got.ID != id || got.Attention == "done" {
		t.Fatalf("confirmed changed workflow did not reopen: %+v", got)
	}
}

func TestSourceOnlyRuntimeCompletionKeepsActivityAndCleanupSafeguards(t *testing.T) {
	ctx := context.Background()
	runtime := &handoffRuntime{testSource: testSource{name: "runtime", local: true, ref: protocol.SourceRef{
		ID: "runtime:session:1", Source: "runtime", Kind: "session", Title: "Local session", Signal: "low_priority",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource, Authority: protocol.SourceRefAuthorityNone,
	}}}
	s := sourceOnlyService(t, runtime)
	s.Refresh(ctx, true)
	mutation := &protocol.TaskMutation{TaskID: sourceOnlyTask(t, s).ID}
	for _, tc := range []struct {
		activity protocol.Activity
		readErr  error
		want     string
	}{
		{activity: protocol.ActivityWaiting, want: "agent waiting"},
		{readErr: errors.New("activity unavailable"), want: "activity unavailable"},
	} {
		runtime.activity, runtime.readError = tc.activity, tc.readErr
		if _, err := s.MutateTask(ctx, "task-done", mutation); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("unsafe completion must give a reason: %v", err)
		}
	}
	runtime.activity, runtime.readError = protocol.ActivityIdle, nil
	done := mutateSourceOnlyTask(t, s, "task-done", mutation.TaskID)
	if !done.ManualDone || s.store.Records()[0].State != "done" {
		t.Fatalf("local resource task not done: %+v", done)
	}
	runtime.activity = protocol.ActivityBusy
	if err := s.GuardCleanup(ctx, done, func() error { t.Fatal("cleanup executed while busy"); return nil }); err == nil {
		t.Fatal("cleanup did not recheck live activity")
	}
	s.Refresh(ctx, true)
	if got := sourceOnlyTask(t, s); got.Attention != "in_progress" || !got.LocalAgentReview {
		t.Fatalf("resumed work did not reopen: %+v", got)
	}
	runtime.activity = protocol.ActivityIdle
	s.Refresh(ctx, true)
	if got := sourceOnlyTask(t, s); got.Attention != "attention" || !got.LocalAgentReview || s.store.Records()[0].State == "done" {
		t.Fatalf("settled work bypassed agent result review: %+v", got)
	}
	if got := mutateSourceOnlyTask(t, s, "task-done", mutation.TaskID); !got.ManualDone || got.LocalAgentReview {
		t.Fatalf("explicit Done did not clear review: %+v", got)
	}
}

func TestSourceOnlyLifecycleRejectsDurableAgentRegistration(t *testing.T) {
	s := sourceOnlyService(t)
	s.store.SetTasks([]protocol.Task{{Attention: "low_priority", SourceRefs: []protocol.SourceRef{{
		ID: "agent:instance:fixture", Source: "agent", Kind: "agent", AgentID: "fixture",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleResource,
	}}}})
	id := sourceOnlyTask(t, s).ID
	for _, method := range []string{"task-done", "task-reopen"} {
		_, err := s.MutateTask(context.Background(), method, &protocol.TaskMutation{TaskID: id})
		if err == nil || !strings.Contains(err.Error(), "durable-agent registration") {
			t.Fatalf("%s: want durable-agent error, got %v", method, err)
		}
	}
}
