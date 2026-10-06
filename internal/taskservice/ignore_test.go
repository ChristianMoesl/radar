package taskservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

// fakeIgnoreAuthor persists its authored observation only in a temporary file.
// This exercises actual author-only collection and state merging without a live
// vault, workspace, remote provider, or mock of the service's publication path.
type fakeIgnoreAuthor struct {
	path         string
	mu           sync.Mutex
	lastTask     protocol.Task
	writeError   error
	collectError error
	calls        atomic.Int32
	writes       atomic.Int32
	creates      atomic.Int32
	collected    chan struct{}
}

var _ integration.TaskAuthoringProvider = (*fakeIgnoreAuthor)(nil)
var _ integration.TaskIgnoreProvider = (*fakeIgnoreAuthor)(nil)

func (*fakeIgnoreAuthor) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "author", Label: "Temporary author"}
}

func (*fakeIgnoreAuthor) Local() bool { return true }

func (a *fakeIgnoreAuthor) read() (protocol.SourceRef, error) {
	data, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return protocol.SourceRef{}, nil
	}
	if err != nil {
		return protocol.SourceRef{}, err
	}
	var ref protocol.SourceRef
	err = json.Unmarshal(data, &ref)
	return ref, err
}

func (a *fakeIgnoreAuthor) Collect(_ context.Context, req integration.CollectRequest) integration.CollectResult {
	a.mu.Lock()
	ref, err := a.read()
	if a.collectError != nil {
		err = a.collectError
	}
	a.mu.Unlock()
	result := integration.CollectResult{Complete: true, SourceStatus: &protocol.SourceStatus{Name: "author", Status: "ok"}}
	if err != nil {
		result.Complete = false
		result.SourceStatus.Status, result.SourceStatus.Detail = "error", err.Error()
		for _, task := range req.Previous {
			for _, previous := range task.SourceRefs {
				if previous.Source == "author" {
					result.Observations = append(result.Observations, integration.Observation{Ref: previous, Signal: integration.WorkSignal(previous.Signal)})
				}
			}
		}
	} else if ref.ID != "" {
		result.Observations = []integration.Observation{{Ref: ref, Signal: integration.WorkSignal(ref.Signal)}}
	}
	a.calls.Add(1)
	if a.collected != nil {
		a.collected <- struct{}{}
	}
	return result
}

func (a *fakeIgnoreAuthor) SetIgnored(_ context.Context, task protocol.Task, ignored bool) (integration.AuthoredTaskIdentity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastTask = task
	ref, err := a.read()
	if err != nil {
		return integration.AuthoredTaskIdentity{}, err
	}
	if ref.ID == "" && !ignored {
		return integration.AuthoredTaskIdentity{}, nil
	}
	previous := ref
	if ref.ID == "" {
		ref = protocol.SourceRef{
			ID: "author:task:one", EntityID: "author:task:one", Source: "author", Kind: "task",
			Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem,
			Authority: protocol.SourceRefAuthorityPrimary, Authored: true,
			Title: task.Title, Signal: "low_priority", Status: "open", CanonicalKey: "author:task:one",
			LinkingKeys: []string{"author:task:one"}, Presentation: protocol.SourceRefPresentation{PreferTitle: true},
		}
		if task.Attention == "done" {
			ref.Signal, ref.Status = "done", "done"
		}
		a.creates.Add(1)
	}
	// Only concrete authoritative refs are adopted. Preserve bindings on
	// unignore and keep terminal/missing contributors supplied by the service.
	bindings := append([]protocol.SourceBinding(nil), ref.Bindings...)
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Role != protocol.SourceRefRoleAuthoritative || sourceRef.Authored {
			continue
		}
		binding := sourceRef.Binding()
		found := false
		for _, existing := range bindings {
			found = found || existing == binding
		}
		if !found {
			bindings = append(bindings, binding)
		}
	}
	ref.Bindings, ref.Ignored = bindings, ignored
	ref.LinkingKeys = []string{ref.ID}
	for _, binding := range bindings {
		ref.LinkingKeys = append(ref.LinkingKeys, binding.LinkingKey())
	}
	identity := integration.AuthoredTaskIdentity{SourceRefID: ref.ID}
	if reflect.DeepEqual(previous, ref) {
		return identity, nil
	}
	data, err := json.Marshal(ref)
	if err != nil {
		return identity, err
	}
	if err := os.WriteFile(a.path, data, 0o600); err != nil {
		return identity, err
	}
	a.writes.Add(1)
	return identity, a.writeError
}

func (*fakeIgnoreAuthor) Create(context.Context, string) (integration.AuthoredTaskIdentity, error) {
	return integration.AuthoredTaskIdentity{}, errors.New("not used by ignore tests")
}

func (*fakeIgnoreAuthor) SetLifecycle(context.Context, protocol.SourceRef, string) (integration.AuthoredTaskIdentity, error) {
	return integration.AuthoredTaskIdentity{}, errors.New("not used by ignore tests")
}

func (*fakeIgnoreAuthor) SetPriority(context.Context, protocol.SourceRef, string) (integration.AuthoredTaskIdentity, error) {
	return integration.AuthoredTaskIdentity{}, errors.New("not used by ignore tests")
}

func (*fakeIgnoreAuthor) PreviewDelete(context.Context, protocol.SourceRef) (protocol.TaskDeletionPreview, error) {
	return protocol.TaskDeletionPreview{}, errors.New("not used by ignore tests")
}

func (*fakeIgnoreAuthor) Delete(context.Context, protocol.SourceRef, protocol.TaskDeletionPreview) (protocol.TaskDeletionResult, error) {
	return protocol.TaskDeletionResult{}, errors.New("not used by ignore tests")
}

func newIgnoreFixture(t *testing.T, source *testSource) (*Service, *state.Store, *fakeIgnoreAuthor, protocol.Task) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.Mkdir(filepath.Join(configHome, "radar"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), []byte(`{"linking_mark_prefixes":["ABC"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	author := &fakeIgnoreAuthor{path: filepath.Join(t.TempDir(), "note.json"), collected: make(chan struct{}, 64)}
	registry := integration.NewRegistry(author, source)
	service := New(store, logger, registry)
	store.SetTasks([]protocol.Task{{Title: source.ref.Title, Attention: source.ref.Signal, SourceRefs: []protocol.SourceRef{source.ref}}})
	store.SetSources([]protocol.SourceStatus{{Name: source.name, Status: "error", Detail: "cached error"}})
	return service, store, author, store.Tasks()[0]
}

func ignoreSource(local bool, signal string) *testSource {
	return &testSource{name: "other", local: local, ref: protocol.SourceRef{
		ID: "other:work:one", Source: "other", Kind: "work_item", EntityID: "other:work:one",
		Title: "Ship release", CanonicalKey: "other:work:one", LinkingKeys: []string{"release-one"},
		Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem,
		Authority: protocol.SourceRefAuthorityContributing, RetainInactive: true, Signal: signal,
	}}
}

func TestIgnoreAdoptsSourceOnlyTaskAndPreservesCachedFacts(t *testing.T) {
	source := ignoreSource(false, "attention")
	service, store, author, original := newIgnoreFixture(t, source)
	ctx := context.Background()
	for _, ignored := range []bool{true, true, false, false, true} {
		before := store.Revision()
		task, err := service.SetIgnored(ctx, original.ID, ignored)
		if err != nil {
			t.Fatal(err)
		}
		if task.ID != original.ID || task.Ignored != ignored || task.Attention != original.Attention {
			t.Fatalf("task identity, ignore preference, or underlying signal changed: %+v", task)
		}
		wantGroup := original.Attention
		if ignored {
			wantGroup = "ignored"
		}
		if task.DisplayGroup() != wantGroup {
			t.Fatalf("group = %q, want %q", task.DisplayGroup(), wantGroup)
		}
		ref, ok := authoredRef(task, "author")
		if !ok || len(ref.Bindings) != 1 || ref.Bindings[0] != source.ref.Binding() || ref.Ignored != ignored || ref.Status != "open" {
			t.Fatalf("authored preference/binding = %+v", ref)
		}
		if store.Revision() <= before {
			t.Fatal("ignore mutation was not published to watchers")
		}
		for _, status := range store.Sources() {
			if status.Name == source.name && (status.Status != "error" || status.Detail != "cached error") {
				t.Fatalf("changed unrelated source status: %+v", status)
			}
		}
		if source.calls.Load() != 0 {
			t.Fatal("ignore mutation collected unrelated source")
		}
	}
	if author.creates.Load() != 1 || author.writes.Load() != 3 {
		t.Fatalf("creates=%d writes=%d, want one note and three actual preference changes", author.creates.Load(), author.writes.Load())
	}
}

func TestConcurrentIgnoreRequestsAreIdempotent(t *testing.T) {
	service, store, author, original := newIgnoreFixture(t, ignoreSource(false, "attention"))
	const requests = 16
	errors := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			task, err := service.Ignore(context.Background(), original.ID)
			if err == nil && (task.ID != original.ID || !task.Ignored) {
				err = fmt.Errorf("unexpected ignored task: %+v", task)
			}
			errors <- err
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if author.creates.Load() != 1 || author.writes.Load() != 1 || len(store.Tasks()) != 1 {
		t.Fatalf("concurrent ignore duplicated/re-wrote note: creates=%d writes=%d tasks=%d", author.creates.Load(), author.writes.Load(), len(store.Tasks()))
	}
}

func TestUnignoreNeverIgnoredSourceOnlyTaskDoesNotAuthorANote(t *testing.T) {
	service, store, author, original := newIgnoreFixture(t, ignoreSource(false, "immediate"))
	for range 2 {
		task, err := service.Unignore(context.Background(), original.ID)
		if err != nil || !reflect.DeepEqual(task, original) {
			t.Fatalf("unignore no-op = %+v, %v, want original %+v", task, err, original)
		}
	}
	if author.creates.Load() != 0 || author.writes.Load() != 0 || len(store.Tasks()) != 1 {
		t.Fatal("unignore created a note or detached the task")
	}
}

func TestIgnoreDoneTaskDoesNotReopenIt(t *testing.T) {
	service, _, author, original := newIgnoreFixture(t, ignoreSource(false, "done"))
	for _, ignored := range []bool{true, false, true} {
		task, err := service.SetIgnored(context.Background(), original.ID, ignored)
		if err != nil || task.Ignored != ignored || task.Attention != "done" || task.DisplayGroup() != "done" {
			t.Fatalf("done task ignore=%v: %+v, %v", ignored, task, err)
		}
		ref, _ := author.read()
		if ref.Status != "done" {
			t.Fatalf("ignore changed authored lifecycle: %+v", ref)
		}
	}
}

func TestIgnorePassesRetainedContributorsToAuthor(t *testing.T) {
	source := ignoreSource(false, "attention")
	service, store, author, original := newIgnoreFixture(t, source)
	missing := source.ref
	missing.ID, missing.EntityID, missing.CanonicalKey, missing.Signal = "other:work:retained", "other:work:retained", "other:work:retained", "done"
	store.SetTasks([]protocol.Task{{Title: original.Title, Attention: original.Attention, SourceRefs: []protocol.SourceRef{source.ref, missing}}})
	store.SetTasksForSources([]protocol.Task{{Title: original.Title, Attention: original.Attention, SourceRefs: []protocol.SourceRef{source.ref}}}, []string{source.name})
	if len(store.Tasks()[0].SourceRefs) != 1 || len(store.CollectionTasks()[0].SourceRefs) != 2 {
		t.Fatal("fixture must include a retained contributor absent from the served projection")
	}
	task, err := service.Ignore(context.Background(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(author.lastTask.SourceRefs) != 2 {
		t.Fatalf("author received projected rather than retained refs: %+v", author.lastTask)
	}
	ref, _ := authoredRef(task, "author")
	if len(ref.Bindings) != 2 {
		t.Fatalf("retained contributor was not bound: %+v", ref.Bindings)
	}
	tracked, _ := taskByID(store.CollectionTasks(), task.ID)
	found := false
	for _, ref := range tracked.SourceRefs {
		found = found || ref.ID == missing.ID
	}
	if !found {
		t.Fatal("ignore publication discarded retained source reference")
	}
}

func TestIgnoreMutationDoesNotWaitForCollectionOrLoseBindingsToStaleRefresh(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		for _, method := range []string{"task-ignore", "task-unignore", "multiple", "write-error"} {
			t.Run(fmt.Sprintf("local=%v/%s", localOnly, method), func(t *testing.T) {
				source := ignoreSource(localOnly, "attention")
				source.started, source.release = make(chan struct{}, 4), make(chan struct{})
				service, store, author, original := newIgnoreFixture(t, source)
				if method == "task-unignore" {
					if _, err := service.Ignore(context.Background(), original.ID); err != nil {
						t.Fatal(err)
					}
				}
				if method == "write-error" {
					author.writeError = errors.New("write succeeded but post-write operation failed")
				}
				for len(author.collected) > 0 {
					<-author.collected
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				finished := make(chan collector.Result, 1)
				joined := make(chan struct{})
				go func() {
					defer close(joined)
					finished <- service.Refresh(ctx, localOnly)
				}()
				released := false
				defer func() {
					if !released {
						close(source.release)
					}
					<-joined
				}()
				wait(t, source.started)
				wait(t, author.collected)
				type mutationResult struct {
					task protocol.Task
					err  error
				}
				mutated := make(chan mutationResult, 1)
				go func() {
					actualMethod := method
					if method == "multiple" || method == "write-error" {
						actualMethod = "task-ignore"
					}
					task, err := service.MutateTask(ctx, actualMethod, &protocol.TaskMutation{TaskID: original.ID})
					if method == "multiple" && err == nil {
						task, err = service.Unignore(ctx, task.ID)
						if err == nil {
							task, err = service.Ignore(ctx, task.ID)
						}
					}
					mutated <- mutationResult{task: task, err: err}
				}()
				var got mutationResult
				select {
				case got = <-mutated:
				case <-ctx.Done():
					t.Fatal("ignore mutation waited for unrelated collection")
				}
				if method == "write-error" {
					if !errors.Is(got.err, author.writeError) {
						t.Fatalf("mutation error = %v", got.err)
					}
				} else if got.err != nil {
					t.Fatal(got.err)
				} else {
					cached, ok := taskByID(store.Tasks(), got.task.ID)
					if !ok || !reflect.DeepEqual(cached, got.task) {
						t.Fatal("ignore preference not visible while refresh remains blocked")
					}
				}
				if source.calls.Load() != 1 {
					t.Fatal("ignore mutation re-collected an unrelated source")
				}
				close(source.release)
				released = true
				select {
				case <-finished:
				case <-ctx.Done():
					t.Fatal("refresh did not finish")
				}
				tasks := store.Tasks()
				if len(tasks) != 1 || tasks[0].ID != original.ID || tasks[0].Ignored != (method != "task-unignore") {
					t.Fatalf("stale refresh detached binding or reverted preference: %+v", tasks)
				}
				ref, ok := authoredRef(tasks[0], "author")
				if !ok || len(ref.Bindings) != 1 || ref.Bindings[0] != source.ref.Binding() {
					t.Fatalf("stale refresh lost authored binding: %+v", ref)
				}
				if tasks[0].Attention == "done" || source.calls.Load() != 1 || author.creates.Load() != 1 {
					t.Fatal("ignore changed lifecycle, retried remote collection, or duplicated the note")
				}
			})
		}
	}
}

func TestIgnoreCollectFailureAfterWriteIsReportedAndRetryReusesNote(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			service, store, author, original := newIgnoreFixture(t, ignoreSource(false, "attention"))
			if existing {
				if _, err := service.Ignore(context.Background(), original.ID); err != nil {
					t.Fatal(err)
				}
			}
			author.collectError = errors.New("temporary collection failure")
			// Even if the stale projection already has the requested preference,
			// a failed publication must not be reported as a successful mutation.
			if _, err := service.Ignore(context.Background(), original.ID); err == nil {
				t.Fatal("failed post-write author collection reported success")
			}
			ref, err := author.read()
			if err != nil || !ref.Ignored {
				t.Fatalf("preference was not durably written before collection failure: %+v, %v", ref, err)
			}
			author.collectError = nil
			task, err := service.Ignore(context.Background(), original.ID)
			if err != nil || !task.Ignored || task.ID != original.ID || len(store.Tasks()) != 1 {
				t.Fatalf("retry failed to publish preference: %+v, %v", task, err)
			}
			if author.creates.Load() != 1 || author.writes.Load() != 1 {
				t.Fatal("retry duplicated or unnecessarily rewrote the authored note")
			}
		})
	}
}

// Embedding the narrower contract intentionally hides SetIgnored, proving it is
// an optional capability rather than a new requirement for every author.
type authorWithoutIgnore struct {
	integration.TaskAuthoringProvider
}

func TestIgnoreRejectsMissingCapabilityAndInvalidTargets(t *testing.T) {
	service, store, author, _ := newIgnoreFixture(t, ignoreSource(false, "attention"))
	before, revision := author.calls.Load(), store.Revision()
	for _, method := range []string{"task-ignore", "task-unignore"} {
		for _, mutation := range []*protocol.TaskMutation{nil, {TaskID: 999}} {
			if _, err := service.MutateTask(context.Background(), method, mutation); err == nil {
				t.Fatalf("accepted invalid mutation: %s %+v", method, mutation)
			}
		}
	}
	service.integrations = integration.NewRegistry(authorWithoutIgnore{author})
	if _, err := service.Ignore(context.Background(), store.Tasks()[0].ID); err == nil {
		t.Fatal("author without optional ignore capability accepted mutation")
	}
	if author.calls.Load() != before || store.Revision() != revision || author.writes.Load() != 0 {
		t.Fatal("invalid mutation wrote authoring data or refreshed cache")
	}
}
