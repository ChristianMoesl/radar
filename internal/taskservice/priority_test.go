package taskservice

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/notification"
	"radar/internal/protocol"
)

type prioritySource struct {
	testSource
	failure error
}

func (s *prioritySource) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	if s.failure != nil {
		return integration.CollectResult{SourceStatus: &protocol.SourceStatus{Name: s.name, Status: "error", Detail: s.failure.Error()}}
	}
	return s.testSource.Collect(ctx, req)
}

type prioritySender struct{ sent int }

func (s *prioritySender) Send(context.Context, notification.Notification) error {
	s.sent++
	return nil
}

func TestLowPriorityMutationAndActionResolutionShareNotificationProjection(t *testing.T) {
	ctx := context.Background()
	remote := &prioritySource{testSource: testSource{name: "remote"}}
	f := newFixture(t, remote)
	remote.ref = f.linkedRef("remote", "attention")
	remote.ref.ActionableSignals = []string{"request:one"}
	f.service.Refresh(ctx, false)
	sender := &prioritySender{}
	notifier := notification.NewWithSender(f.service.logger, sender)
	f.service.SetOnRefresh(func(before, after []protocol.Task) { notifier.NotifyTransitions(ctx, before, after) })
	beforeCalls := remote.calls.Load()
	task := f.mutate(t, "task-priority", &protocol.TaskMutation{TaskID: f.task.ID, Priority: "low"})
	if task.Priority != "low" || task.Attention != "low_priority" || remote.calls.Load() != beforeCalls || sender.sent != 0 {
		t.Fatalf("Low did not quiet known work locally and without self-notifications: %+v", task)
	}
	check := func(want string, notifications int) {
		t.Helper()
		current, ok := taskByID(f.store.Tasks(), f.task.ID)
		if !ok || current.Priority != "low" || current.Attention != want || sender.sent != notifications {
			t.Fatalf("effective projection = %+v, notifications=%d; want %s/%d", current, sender.sent, want, notifications)
		}
	}
	f.service.Refresh(ctx, false)
	check("low_priority", 0)
	remote.ref.ActionableSignals = []string{"request:two"}
	f.service.Refresh(ctx, false)
	check("attention", 1)
	remote.failure = errors.New("source temporarily unavailable")
	f.service.Refresh(ctx, false)
	check("attention", 1)
	remote.failure = nil
	remote.ref.Signal, remote.ref.WorkflowState = "in_progress", "in_progress"
	f.service.Refresh(ctx, false)
	check("low_priority", 1)
	remote.ref.Signal, remote.ref.WorkflowState = "attention", "attention"
	remote.ref.ActionableSignals = []string{"request:three"}
	f.service.Refresh(ctx, false)
	check("attention", 2)
	task = f.mutate(t, "task-priority", &protocol.TaskMutation{TaskID: f.task.ID, Priority: "low"})
	if task.Attention != "low_priority" || sender.sent != 2 {
		t.Fatal("explicit re-deferral did not quiet the outstanding request without notification")
	}
	f.mutate(t, "task-mute", &protocol.TaskMutation{TaskID: f.task.ID})
	remote.ref.ActionableSignals = []string{"request:four"}
	f.service.Refresh(ctx, false)
	check("attention", 2)
	current, _ := taskByID(f.store.Tasks(), f.task.ID)
	if !current.Muted || current.DisplayGroup() != "muted" {
		t.Fatal("new activity implicitly unmuted Low work")
	}
}

func TestPriorityRejectsDoneAndInvalidValuesWithoutChangingIntent(t *testing.T) {
	f := newFixture(t)
	if _, err := f.service.MutateTask(context.Background(), "task-priority", &protocol.TaskMutation{TaskID: f.task.ID, Priority: "invalid"}); err == nil {
		t.Fatal("invalid priority was accepted")
	}
	f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
	for _, priority := range []string{"low", "normal", "urgent"} {
		if _, err := f.service.MutateTask(context.Background(), "task-priority", &protocol.TaskMutation{TaskID: f.task.ID, Priority: priority}); err == nil {
			t.Fatal("priority mutation reopened Done work")
		}
	}
	current, _ := taskByID(f.store.Tasks(), f.task.ID)
	if current.Attention != "done" || current.Priority != "normal" {
		t.Fatal("rejected mutation changed lifecycle or authored priority")
	}
}

func TestSourceOnlyLowAdoptionAndResetUseTheSameDurableBinding(t *testing.T) {
	remote := &testSource{name: "github", ref: protocol.SourceRef{
		ID: "github:pr:acme/app:7", EntityID: "github:pr:acme/app:7", Source: "github", Kind: "pull_request",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing,
		CanonicalKey: "github:pr:acme/app:7", Title: "Source-only review", URL: "https://github.com/acme/app/pull/7", Signal: "attention", WorkflowState: "OPEN", ActionableSignals: []string{"review-request:first"},
	}}
	f := newFixture(t, remote)
	f.service.Refresh(context.Background(), false)
	var original protocol.Task
	for _, task := range f.store.Tasks() {
		if task.Title == remote.ref.Title {
			original = task
		}
	}
	if original.ID == 0 || len(original.SourceRefs) != 1 {
		t.Fatal("fixture is not source-only")
	}
	calls := remote.calls.Load()
	low, err := f.service.SetPriority(context.Background(), original.ID, "low")
	if err != nil || low.ID != original.ID || low.Priority != "low" || low.Attention != "low_priority" || remote.calls.Load() != calls {
		t.Fatalf("source-only adoption: %+v %v", low, err)
	}
	author, ok := authoredRef(low, "obsidian")
	if !ok || len(author.Bindings) != 1 || author.Bindings[0] != remote.ref.Binding() || author.ProvidesWorkspace {
		t.Fatalf("adoption lost its tracking identity or created a workspace: %+v", author)
	}
	low, err = f.service.SetPriority(context.Background(), low.ID, "low")
	if err != nil {
		t.Fatal(err)
	}
	author, _ = authoredRef(low, "obsidian")
	if err := f.service.Reset(); err != nil {
		t.Fatal(err)
	}
	f.service.Refresh(context.Background(), false)
	matched := 0
	for _, task := range f.store.Tasks() {
		for _, ref := range task.SourceRefs {
			if ref.ID != author.ID {
				continue
			}
			matched++
			if task.Priority != "low" || task.Attention != "low_priority" || !reflect.DeepEqual(ref.LowPriorityBaseline, author.LowPriorityBaseline) || len(ref.Bindings) != 1 {
				t.Fatalf("cache reset lost canonical Low intent: %+v", task)
			}
		}
	}
	if matched != 1 {
		t.Fatal("reset/retry duplicated or lost the adopted task")
	}
}

func TestLowMutationDoesNotWaitForRefreshOrPredeferAnInflightNewRequest(t *testing.T) {
	remote := &testSource{name: "remote"}
	f := newFixture(t, remote)
	remote.ref = f.linkedRef("remote", "attention")
	remote.ref.ActionableSignals = []string{"request:first"}
	f.service.Refresh(context.Background(), false)
	remote.ref.ActionableSignals = []string{"request:second"}
	remote.started, remote.release = make(chan struct{}, 1), make(chan struct{})
	for len(f.author.collected) > 0 {
		<-f.author.collected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		f.service.Refresh(ctx, false)
	}()
	released := false
	defer func() {
		if !released {
			close(remote.release)
		}
		<-finished
	}()
	wait(t, remote.started)
	wait(t, f.author.collected)
	type result struct {
		task protocol.Task
		err  error
	}
	mutated := make(chan result, 1)
	go func() {
		task, err := f.service.SetPriority(ctx, f.task.ID, "low")
		mutated <- result{task, err}
	}()
	select {
	case got := <-mutated:
		if got.err != nil || got.task.Priority != "low" || got.task.Attention != "low_priority" {
			t.Fatalf("Low did not quiet the current known request: %+v %v", got.task, got.err)
		}
	case <-ctx.Done():
		t.Fatal("priority mutation waited for unrelated collection")
	}
	close(remote.release)
	released = true
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("in-flight refresh did not finish")
	}
	current, _ := taskByID(f.store.Tasks(), f.task.ID)
	if current.Priority != "low" || current.Attention != "attention" || remote.calls.Load() != 2 {
		t.Fatalf("stale authoring reverted Low or silently deferred an unseen new request: %+v", current)
	}
}
