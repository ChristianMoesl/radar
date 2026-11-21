package taskservice

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
)

// Hold the whole remote batch at a slow provider, after healthy providers have
// returned. Resource changes must not require any of those providers to retry.
func TestRemoteRefreshConvergesAcrossResourceMutations(t *testing.T) {
	for _, freshLocal := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh-local=%t", freshLocal), func(t *testing.T) {
			jira := &testSource{name: "jira", started: make(chan struct{}, 8), release: make(chan struct{})}
			github := &testSource{name: "github"}
			datadog := &testSource{name: "datadog"}
			slow := &testSource{name: "terraform", started: make(chan struct{}, 8), release: make(chan struct{})}
			local := &snapshotLocalSource{}
			workspace := &testSource{name: "resource", local: true}
			f := newFixture(t, jira, github, datadog, slow, local, workspace)
			for _, source := range []*testSource{jira, github, datadog, slow} {
				source.ref = f.linkedRef(source.name, "in_progress")
				source.ref.Title = "fresh " + source.name
			}
			ref := f.linkedRef("runtime", "in_progress")
			ref.Lifecycle, ref.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
			local.ref.Store(&ref)
			workspace.ref = f.linkedRef("resource", "in_progress")
			workspace.ref.Lifecycle, workspace.ref.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
			close(jira.release)
			f.store.SetSources([]protocol.SourceStatus{{Name: "jira", Status: "error", Detail: "old Jira error"}})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done := make(chan collector.Result, 1)
			joined := make(chan struct{})
			go func() { defer close(joined); done <- f.service.Refresh(ctx, false) }()
			t.Cleanup(func() { cancel(); <-joined })
			waitRemoteResource(t, jira.started)
			waitRemoteResource(t, slow.started)
			// Publish repeated newer local facts while Terraform keeps the batch
			// open. The last invalidation may deliberately remain uncollected.
			for generation := 1; generation <= 8; generation++ {
				updated := *local.ref.Load()
				updated.Title = fmt.Sprintf("local generation %d", generation)
				updated.Activity = protocol.ActivityBusy
				local.ref.Store(&updated)
				workspace.ref.Title = updated.Title
				f.service.MarkResourceRefreshPending()
				f.service.Refresh(ctx, true)
			}
			if !freshLocal {
				f.service.MarkResourceRefreshPending()
			}
			close(slow.release)
			var result collector.Result
			select {
			case result = <-done:
			case <-ctx.Done():
				t.Fatal("remote batch did not finish")
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("fixture expired before publication: %v", err)
			}
			for _, source := range []*testSource{jira, github, datadog, slow} {
				if source.calls.Load() != 1 || result.Attempts[source.name].Outcome != "complete" {
					t.Fatalf("%s retried or lost its completed attempt: calls=%d attempt=%+v", source.name, source.calls.Load(), result.Attempts[source.name])
				}
				found := false
				for _, status := range f.store.Sources() {
					if status.Name == source.name {
						found = status.Status == "ok" && status.Detail == "fresh"
					}
				}
				if !found {
					t.Fatalf("healthy %s result did not publish/clear old error: %+v", source.name, f.store.Sources())
				}
			}
			task, found := taskByID(f.store.Tasks(), f.task.ID)
			if !found || task.Activity != protocol.ActivityBusy || task.Attention == "done" {
				t.Fatalf("latest local activity lost: %+v", task)
			}
			for _, source := range []string{"jira", "github", "datadog", "terraform", "runtime", "resource"} {
				found := false
				for _, ref := range task.SourceRefs {
					if ref.Source == source {
						want := "fresh " + source
						if source == "runtime" || source == "resource" {
							want = "local generation 8"
						}
						found = ref.Title == want
					}
				}
				if !found {
					t.Fatalf("missing latest %s fact: %+v", source, task.SourceRefs)
				}
			}
			if pending := f.service.ResourceSnapshot().ResourceRefreshPending; pending == freshLocal {
				t.Fatalf("resource freshness=%t pending, want pending=%t", pending, !freshLocal)
			}
			if !freshLocal && result.Complete["resource"] {
				t.Fatal("remote publication certified a superseded local read")
			}
		})
	}
}

func TestRemoteResourceDriftDoesNotCrossCancellationOrReset(t *testing.T) {
	for _, action := range []string{"cancel", "reset"} {
		t.Run(action, func(t *testing.T) {
			f, remote, _ := concurrentRefreshFixture(t)
			f.store.SetSources([]protocol.SourceStatus{{Name: "remote", Status: "error", Detail: "old error"}})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); f.service.Refresh(ctx, false) }()
			t.Cleanup(func() { cancel(); <-done })
			waitRemoteResource(t, remote.started)
			f.service.MarkResourceRefreshPending()
			if action == "cancel" {
				cancel()
			} else if err := f.service.Reset(); err != nil {
				t.Fatal(err)
			}
			revision := f.store.Revision()
			close(remote.release)
			waitRemoteResource(t, done)
			if f.store.Revision() != revision {
				t.Fatal("canceled or pre-reset batch published")
			}
			if !f.service.ResourceSnapshot().ResourceRefreshPending {
				t.Fatal("discarded batch certified resource freshness")
			}
			if action == "reset" && len(f.store.CollectionTasks()) != 0 {
				t.Fatal("pre-reset batch revived cached tasks")
			}
		})
	}
}

func TestRemoteResourceDriftDefersLifecycleUntilStableBatch(t *testing.T) {
	for _, freshLocal := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh-local=%t", freshLocal), func(t *testing.T) {
			f, remote, _ := concurrentRefreshFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); f.service.Refresh(ctx, false) }()
			t.Cleanup(func() { cancel(); <-done })
			waitRemoteResource(t, remote.started)
			f.service.MarkResourceRefreshPending()
			if freshLocal {
				f.service.Refresh(ctx, true)
			}
			close(remote.release)
			waitRemoteResource(t, done)
			task, _ := taskByID(f.store.Tasks(), f.task.ID)
			if err := ctx.Err(); err != nil {
				t.Fatalf("fixture expired before publication: %v", err)
			}
			ref, found := authoredRef(task, "obsidian")
			if !found || ref.Signal == "done" || task.Attention == "done" {
				t.Fatalf("resource-fenced remote batch changed lifecycle: %+v", task)
			}
			// A later stable batch must still perform normal completion, rather
			// than disabling lifecycle inference to solve publication starvation.
			f.service.Refresh(ctx, false)
			task, _ = taskByID(f.store.Tasks(), f.task.ID)
			if task.Attention != "done" {
				t.Fatalf("stable remote batch did not complete idle work: %+v", task)
			}
		})
	}
}

func TestRemoteResourceDriftPreservesNewerAuthoredIntent(t *testing.T) {
	for _, action := range []string{"task-title", "task-done", "task-reopen", "delete"} {
		t.Run(action, func(t *testing.T) {
			f, remote, _ := concurrentRefreshFixture(t)
			if action == "task-done" || action == "delete" {
				remote.ref.Signal, remote.ref.WorkflowState = "in_progress", "in_progress"
			}
			// Only explicit adoption authorizes ongoing binding persistence.
			f.mutate(t, "task-mute", &protocol.TaskMutation{TaskID: f.task.ID})
			f.mutate(t, "task-unmute", &protocol.TaskMutation{TaskID: f.task.ID})
			if action == "task-reopen" {
				f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			}
			// Real durable authoring/archive I/O is not a latency budget. A
			// fixture timeout must never masquerade as a stale publication.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); f.service.Refresh(ctx, false) }()
			t.Cleanup(func() { cancel(); <-done })
			waitRemoteResource(t, remote.started)
			f.service.MarkResourceRefreshPending()
			if action == "delete" {
				preview, err := f.service.PreviewDeleteTask(ctx, f.task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.service.DeleteTask(ctx, &preview); err != nil {
					t.Fatal(err)
				}
			} else {
				f.mutate(t, action, &protocol.TaskMutation{TaskID: f.task.ID, Title: "Newest authored title"})
			}
			close(remote.release)
			waitRemoteResource(t, done)
			if err := ctx.Err(); err != nil {
				t.Fatalf("fixture expired before publication: %v", err)
			}
			foundRemote, foundAuthor := false, false
			for _, task := range f.store.CollectionTasks() {
				for _, ref := range task.SourceRefs {
					foundRemote = foundRemote || ref.ID == remote.ref.ID
					if !ref.Authored {
						continue
					}
					foundAuthor = true
					wantState := "open"
					if action == "task-done" {
						wantState = "done"
					}
					if ref.Metadata["state"] != wantState || (action == "task-title" && ref.Title != "Newest authored title") {
						t.Fatalf("remote batch overwrote authored intent: %+v", ref)
					}
					bound := false
					for _, binding := range ref.Bindings {
						bound = bound || binding == remote.ref.Binding()
					}
					if !bound {
						t.Fatalf("new remote contributor not durably bound: %+v", ref.Bindings)
					}
				}
			}
			if !foundRemote || foundAuthor == (action == "delete") {
				t.Fatalf("remote publication/deleted-author preservation failed: %+v", f.store.CollectionTasks())
			}
			persisted := f.author.Source.Collect(ctx, integration.CollectRequest{})
			wantNotes := 1
			if action == "delete" {
				wantNotes = 0
			}
			if !persisted.Complete || len(persisted.Observations) != wantNotes {
				t.Fatalf("canonical authoring state differs from publication: %+v", persisted)
			}
			for _, observation := range persisted.Observations {
				bound := false
				for _, binding := range observation.Ref.Bindings {
					bound = bound || binding == remote.ref.Binding()
				}
				if !bound {
					t.Fatal("published contributor was not retained in canonical authoring state")
				}
			}
		})
	}
}

type resourceRemoteFailure struct{ testSource }

func (s *resourceRemoteFailure) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{SourceStatus: &protocol.SourceStatus{Name: s.name, Status: "error", Detail: "new provider failure"}}
}

func TestRemoteResourceDriftPreservesPartialProviderFailure(t *testing.T) {
	remote := &testSource{name: "jira", started: make(chan struct{}, 1), release: make(chan struct{})}
	failed := &resourceRemoteFailure{testSource: testSource{name: "terraform"}}
	f := newFixture(t, remote, failed)
	remote.ref = f.linkedRef("jira", "done")
	f.store.SetSources([]protocol.SourceStatus{{Name: "jira", Status: "error", Detail: "old Jira error"}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan collector.Result, 1)
	joined := make(chan struct{})
	go func() { defer close(joined); done <- f.service.Refresh(ctx, false) }()
	t.Cleanup(func() { cancel(); <-joined })
	waitRemoteResource(t, remote.started)
	f.service.MarkResourceRefreshPending()
	close(remote.release)
	var result collector.Result
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("partial remote batch did not finish")
	}
	if !result.Complete["jira"] || result.Complete["terraform"] {
		t.Fatalf("source completeness changed: %+v", result.Complete)
	}
	for _, name := range []string{"jira", "terraform"} {
		found := false
		for _, status := range f.store.Sources() {
			if status.Name == name {
				found = (name == "jira" && status.Status == "ok") || (name == "terraform" && status.Status == "error" && status.Detail == "new provider failure")
			}
		}
		if !found {
			t.Fatalf("partial batch hid %s health: %+v", name, f.store.Sources())
		}
	}
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("remote partial batch certified invalidated local resources")
	}
}

func TestRemoteResourceDriftPersistsNewBindingsBeforeReset(t *testing.T) {
	source := &bindingRaceSource{started: make(chan struct{}, 8), release: make(chan struct{})}
	f := newFixture(t, source)
	first := f.linkedRef("remote", "in_progress")
	second := first
	second.ID, second.EntityID, second.CanonicalKey = "remote:second", "remote:second", "remote:second"
	f.store.SetTasksForSources([]protocol.Task{{Attention: first.Signal, SourceRefs: []protocol.SourceRef{first}}}, []string{"remote"})
	f.mutate(t, "task-mute", &protocol.TaskMutation{TaskID: f.task.ID})
	source.normal = []protocol.SourceRef{first, second}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.service.Refresh(ctx, false) }()
	t.Cleanup(func() { cancel(); <-done })
	waitRemoteResource(t, source.started)
	// No authoring mutation or post-mutation local refresh may accidentally
	// supply the fresh authoring read needed to durably bind the new result.
	f.service.MarkResourceRefreshPending()
	close(source.release)
	waitRemoteResource(t, done)
	persisted := f.author.Source.Collect(ctx, integration.CollectRequest{})
	if !persisted.Complete || len(persisted.Observations) != 1 || len(persisted.Observations[0].Ref.Bindings) != 2 {
		t.Fatalf("published remote result lacks durable binding before immediate reset: %+v", persisted)
	}
	if !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("binding reread falsely certified all local resources")
	}
	if err := f.service.Reset(); err != nil {
		t.Fatal(err)
	}
	source.normal = nil
	source.requests = nil // Count only the post-reset resolver recovery.
	first.LinkingKeys, second.LinkingKeys = nil, nil
	source.resolved = []protocol.SourceRef{first, second}
	f.service.Refresh(ctx, false)
	tasks := f.store.Tasks()
	if len(tasks) != 1 || !tasks[0].Muted || len(tasks[0].SourceRefs) != 3 || len(source.requests) != 2 {
		t.Fatalf("reset detached the resource-fenced remote contributor: tasks=%+v requests=%+v", tasks, source.requests)
	}
}

type resourceEventSource struct{ testSource }

func (*resourceEventSource) CollectionItems([]integration.Observation) []integration.CollectionItem {
	return []integration.CollectionItem{{Key: "fresh-item"}}
}

type resourceEventObserver struct{ snapshotLocalSource }

func (*resourceEventObserver) ObserveCollection(context.Context, string, []integration.CollectionItem) error {
	return errors.New("remote event persistence failed")
}

func TestRemoteResourceDriftRetainsObserverFailureAfterActivityReread(t *testing.T) {
	remote := &resourceEventSource{testSource: testSource{name: "github", started: make(chan struct{}, 1), release: make(chan struct{})}}
	observer := &resourceEventObserver{}
	f := newFixture(t, remote, observer)
	remote.ref = f.linkedRef("github", "in_progress")
	local := f.linkedRef("runtime", "in_progress")
	local.Lifecycle, local.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
	observer.ref.Store(&local)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.service.Refresh(ctx, false) }()
	t.Cleanup(func() { cancel(); <-done })
	waitRemoteResource(t, remote.started)
	f.service.MarkResourceRefreshPending()
	close(remote.release)
	waitRemoteResource(t, done)
	if observer.calls.Load() != 2 {
		t.Fatal("fixture did not exercise post-mutation activity reread")
	}
	foundRemote, foundError := false, false
	for _, status := range f.store.Sources() {
		foundRemote = foundRemote || (status.Name == "github" && status.Status == "ok")
		foundError = foundError || (status.Name == "runtime" && status.Status == "error" && status.Detail == "remote event persistence failed")
	}
	if !foundRemote || !foundError || !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatalf("remote publication hid delivery failure or certified pending resources: %+v", f.store.Sources())
	}
}

func waitRemoteResource(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(30 * time.Second):
		t.Fatal("remote resource fixture did not reach the expected phase")
	}
}

type resourceAuthorReadGate struct {
	*testAuthor
	block   atomic.Bool
	started chan struct{}
}

func (s *resourceAuthorReadGate) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	result := s.testAuthor.Collect(ctx, req)
	if s.block.Swap(false) {
		close(s.started)
		<-ctx.Done()
	}
	// A native reader can return a complete observation after cancellation.
	// The publication boundary must still reject the whole canceled batch.
	return result
}

func TestRemoteResourceDriftCancellationDuringAuthorRereadDoesNotPublish(t *testing.T) {
	f, remote, local := concurrentRefreshFixture(t)
	author := &resourceAuthorReadGate{testAuthor: f.author, started: make(chan struct{})}
	f.service.integrations = integration.NewRegistry(author, remote, local)
	f.store.SetSources([]protocol.SourceStatus{{Name: "remote", Status: "error", Detail: "old error"}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.service.Refresh(ctx, false) }()
	t.Cleanup(func() { cancel(); <-done })
	waitRemoteResource(t, remote.started)
	f.service.MarkResourceRefreshPending()
	revision := f.store.Revision()
	author.block.Store(true)
	close(remote.release)
	waitRemoteResource(t, author.started)
	cancel()
	waitRemoteResource(t, done)
	if f.store.Revision() != revision || !f.service.ResourceSnapshot().ResourceRefreshPending {
		t.Fatal("canceled authoring reread published or certified local resources")
	}
	for _, status := range f.store.Sources() {
		if status.Name == "remote" && status.Status != "error" {
			t.Fatal("canceled batch cleared cached provider error")
		}
	}
}
