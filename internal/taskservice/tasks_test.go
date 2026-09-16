package taskservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/protocol"
	"radar/internal/state"
)

type testAuthor struct {
	obsidian.Source
	collected  chan struct{}
	calls      atomic.Int32
	writeError error
}

func (s *testAuthor) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	result := s.Source.Collect(ctx, req)
	s.calls.Add(1)
	if s.collected != nil {
		s.collected <- struct{}{}
	}
	return result
}

func (s *testAuthor) SetLifecycle(ctx context.Context, ref protocol.SourceRef, lifecycle string) (integration.AuthoredTaskIdentity, error) {
	identity, err := s.Source.SetLifecycle(ctx, ref, lifecycle)
	if err == nil && s.writeError != nil {
		return identity, s.writeError
	}
	return identity, err
}

type testSource struct {
	name    string
	local   bool
	ref     protocol.SourceRef
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *testSource) Descriptor() integration.Descriptor { return integration.Descriptor{Name: s.name} }
func (s *testSource) Local() bool                        { return s.local }
func (s *testSource) Collect(ctx context.Context, _ integration.CollectRequest) integration.CollectResult {
	s.calls.Add(1)
	if s.started != nil {
		s.started <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return integration.CollectResult{}
		}
	}
	return integration.CollectResult{
		Observations: []integration.Observation{{Ref: s.ref, Signal: integration.WorkSignal(s.ref.Signal)}},
		Complete:     true,
		SourceStatus: &protocol.SourceStatus{Name: s.name, Status: "ok", Detail: "fresh"},
	}
}

type fixture struct {
	service *Service
	store   *state.Store
	author  *testAuthor
	task    protocol.Task
}

func newFixture(t testing.TB, sources ...integration.Integration) *fixture {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`{"workspace":{"root_dir":%q},"linking_mark_prefixes":["ABC"]}`, filepath.Join(t.TempDir(), "workspaces"))
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	author := &testAuthor{Source: obsidian.NewSourceAt(vault), collected: make(chan struct{}, 64)}
	registry := integration.NewRegistry(append([]integration.Integration{author}, sources...)...)
	f := &fixture{service: New(store, logger, registry), store: store, author: author}
	f.task = f.mutate(t, "task-create", &protocol.TaskMutation{Title: "Ship release"})
	return f
}

func (f *fixture) mutate(t testing.TB, method string, mutation *protocol.TaskMutation) protocol.Task {
	t.Helper()
	task, err := f.service.MutateTask(context.Background(), method, mutation)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (f *fixture) linkedRef(source, signal string) protocol.SourceRef {
	return protocol.SourceRef{
		ID: source + ":work", Source: source, Kind: "work_item", CanonicalKey: source + ":work",
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem,
		Authority: protocol.SourceRefAuthorityContributing, RetainInactive: true,
		LinkingKeys: []string{f.task.SourceRefs[0].ID}, Signal: signal,
	}
}

func TestMutationsRefreshOnlyAuthorAndPreserveCachedReferences(t *testing.T) {
	remote := &testSource{name: "remote"}
	local := &testSource{name: "local", local: true}
	f := newFixture(t, remote, local)
	refs := []protocol.SourceRef{f.linkedRef("remote", "in_progress"), f.linkedRef("local", "in_progress")}
	for _, ref := range refs {
		f.store.SetTasksForSources([]protocol.Task{{Attention: ref.Signal, SourceRefs: []protocol.SourceRef{ref}}}, []string{ref.Source})
	}
	statuses := []protocol.SourceStatus{{Name: "remote", Status: "error", Detail: "cached error"}, {Name: "local", Status: "ok", Detail: "cached local"}}
	f.store.SetSources(statuses)
	for _, tc := range []struct{ method, priority, state string }{
		{"task-done", "", "done"}, {"task-reopen", "", "open"},
		{"task-priority", "urgent", "open"}, {"task-priority", "normal", "open"},
	} {
		before := f.store.Revision()
		calls := f.author.calls.Load()
		task := f.mutate(t, tc.method, &protocol.TaskMutation{TaskID: f.task.ID, Priority: tc.priority})
		if task.ID != f.task.ID {
			t.Fatalf("task identity changed: %+v", task)
		}
		ref, ok := authoredRef(task, "obsidian")
		if !ok || ref.Metadata["state"] != tc.state || (task.Attention == "done") != (tc.state == "done") {
			t.Fatalf("%s: task = %+v", tc.method, task)
		}
		if tc.priority != "" && ref.Metadata["priority"] != tc.priority {
			t.Fatalf("priority = %q", ref.Metadata["priority"])
		}
		if got := f.author.calls.Load(); got != calls+1 {
			t.Fatalf("author collections = %d, want %d", got, calls+1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		gotRevision := f.store.WaitForRevision(ctx, before)
		cancel()
		if gotRevision <= before {
			t.Fatal("watchers were not notified")
		}
		for _, want := range refs {
			found := false
			for _, tracked := range f.store.CollectionTasks() {
				for _, got := range tracked.SourceRefs {
					if got.ID == want.ID {
						found = reflect.DeepEqual(got, want)
					}
				}
			}
			if !found {
				t.Fatalf("cached ref changed or lost: %+v", want)
			}
		}
		for i, want := range statuses {
			if got := f.store.Sources()[i]; !reflect.DeepEqual(got, want) {
				t.Fatalf("cached status = %+v, want %+v", got, want)
			}
		}
	}
	if remote.calls.Load() != 0 || local.calls.Load() != 0 {
		t.Fatal("mutation collected unrelated sources")
	}
}

func TestMutationDoesNotWaitForCollectionOrLoseToStaleSnapshot(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		for _, method := range []string{"task-create", "task-done", "task-reopen", "task-priority", "multiple", "write-error"} {
			t.Run(fmt.Sprintf("local=%v/%s", localOnly, method), func(t *testing.T) {
				other := &testSource{name: "other", local: localOnly, started: make(chan struct{}, 8), release: make(chan struct{})}
				f := newFixture(t, other)
				other.ref = f.linkedRef("other", "in_progress")
				if method == "task-reopen" {
					f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
					// Reopening must also survive automatic completion when the
					// old remote snapshot says all linked work is done.
					other.ref.Signal = "done"
				}
				if method == "write-error" {
					f.author.writeError = errors.New("archive failed after write")
				}
				for len(f.author.collected) > 0 {
					<-f.author.collected
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				finished := make(chan collector.Result, 1)
				joined := make(chan struct{})
				go func() {
					defer close(joined)
					finished <- f.service.Refresh(ctx, localOnly)
				}()
				released := false
				defer func() {
					if !released {
						close(other.release)
					}
					<-joined
				}()
				wait(t, other.started)
				wait(t, f.author.collected) // the refresh has captured the old note

				type response struct {
					task protocol.Task
					err  error
				}
				mutated := make(chan response, 1)
				go func() {
					actualMethod := method
					if method == "multiple" || method == "write-error" {
						actualMethod = "task-done"
					}
					task, err := f.service.MutateTask(ctx, actualMethod, &protocol.TaskMutation{TaskID: f.task.ID, Title: "Another task", Priority: "urgent"})
					if method == "multiple" && err == nil {
						task, err = f.service.MutateTask(ctx, "task-reopen", &protocol.TaskMutation{TaskID: f.task.ID})
						if err == nil {
							task, err = f.service.MutateTask(ctx, "task-priority", &protocol.TaskMutation{TaskID: f.task.ID, Priority: "urgent"})
						}
					}
					mutated <- response{task, err}
				}()
				var got response
				select {
				case got = <-mutated:
				case <-ctx.Done():
					t.Fatal("mutation waited for blocked source collection")
				}
				if method == "write-error" {
					if !errors.Is(got.err, f.author.writeError) {
						t.Fatalf("mutation error = %v", got.err)
					}
				} else if got.err != nil {
					t.Fatal(got.err)
				}
				if other.calls.Load() != 1 {
					t.Fatal("mutation re-collected unrelated source")
				}
				// The collection is still blocked, but a successful mutation is
				// already visible to socket watchers and subsequent actions.
				if got.err == nil {
					cached, ok := taskByID(f.store.Tasks(), got.task.ID)
					if !ok || !reflect.DeepEqual(cached, got.task) {
						t.Fatal("mutation not published before refresh finished")
					}
				}
				close(other.release)
				released = true
				var result collector.Result
				select {
				case result = <-finished:
				case <-ctx.Done():
					t.Fatal("refresh did not finish")
				}
				id := got.task.ID
				if method == "write-error" {
					id = f.task.ID
				}
				task, ok := taskByID(f.store.Tasks(), id)
				if !ok {
					t.Fatal("task lost after stale refresh")
				}
				wantState := "open"
				if method == "task-done" || method == "write-error" {
					wantState = "done"
				}
				ref, ok := authoredRef(task, "obsidian")
				if !ok || ref.Metadata["state"] != wantState || (task.Attention == "done") != (wantState == "done") {
					t.Fatalf("stale refresh overwrote mutation: %+v", task)
				}
				if (method == "task-priority" || method == "multiple") && ref.Metadata["priority"] != "urgent" {
					t.Fatal("priority was overwritten")
				}
				if other.calls.Load() != 1 {
					t.Fatal("stale result retried unrelated collection")
				}
				if !localOnly && !result.Complete["other"] {
					t.Fatal("lost remote completeness evidence")
				}
				found := false
				for _, tracked := range f.store.CollectionTasks() {
					for _, ref := range tracked.SourceRefs {
						if ref.ID == other.ref.ID {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("discarded unrelated collector's fresh result")
				}
				for _, status := range f.store.Sources() {
					if status.Name == "other" && status.Detail != "fresh" {
						t.Fatal("discarded fresh source status")
					}
				}
			})
		}
	}
}

func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not start")
	}
}

func TestFullRefreshStillAutomaticallyCompletesLinkedWork(t *testing.T) {
	other := &testSource{name: "other"}
	f := newFixture(t, other)
	other.ref = f.linkedRef("other", "done")
	f.service.Refresh(context.Background(), false)
	task, ok := taskByID(f.store.Tasks(), f.task.ID)
	if !ok || task.Attention != "done" {
		t.Fatalf("automatic completion lost: %+v", task)
	}
	result := f.author.Source.Collect(context.Background(), integration.CollectRequest{})
	if len(result.Observations) != 1 || result.Observations[0].Ref.Status != "done" {
		t.Fatal("automatic completion was not persisted")
	}
}

func TestMutationValidationDoesNotWriteOrRefresh(t *testing.T) {
	f := newFixture(t)
	before := f.store.Revision()
	for _, tc := range []struct {
		method   string
		mutation *protocol.TaskMutation
	}{
		{"task-done", nil}, {"task-done", &protocol.TaskMutation{TaskID: 999}},
		{"unsupported", &protocol.TaskMutation{}}, {"task-priority", &protocol.TaskMutation{TaskID: f.task.ID, Priority: "invalid"}},
	} {
		if _, err := f.service.MutateTask(context.Background(), tc.method, tc.mutation); err == nil {
			t.Fatalf("accepted invalid request: %+v", tc)
		}
	}
	if f.store.Revision() != before || f.author.calls.Load() != 1 {
		t.Fatal("invalid request changed cache")
	}
}

func BenchmarkAuthoredTaskLifecycle(b *testing.B) {
	f := newFixture(b)
	f.author.collected = nil
	ctx := context.Background()
	for i := 1; i < 100; i++ {
		if _, err := f.author.Create(ctx, fmt.Sprintf("Task %d", i)); err != nil {
			b.Fatal(err)
		}
	}
	f.service.Refresh(ctx, true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		method := "task-done"
		if i%2 != 0 {
			method = "task-reopen"
		}
		if _, err := f.service.MutateTask(ctx, method, &protocol.TaskMutation{TaskID: f.task.ID}); err != nil {
			b.Fatal(err)
		}
	}
}
