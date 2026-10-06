package collector

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
)

type boundCompletionSource struct {
	completionSource
	resolved []protocol.SourceRef
	requests *[]protocol.SourceBinding
	failed   bool
}

func (s boundCompletionSource) ResolveBindings(_ context.Context, req integration.BindingRequest) integration.CollectResult {
	if s.requests != nil {
		*s.requests = append(*s.requests, req.Bindings...)
	}
	result := integration.CollectResult{Complete: !s.failed}
	if s.failed {
		result.SourceStatus = &protocol.SourceStatus{Name: s.name, Status: "error", Detail: "bound lookup unavailable"}
		return result
	}
	for _, binding := range req.Bindings {
		for _, ref := range s.resolved {
			if binding.ID == ref.ID {
				result.Observations = append(result.Observations, completionObservations([]protocol.SourceRef{ref})...)
			}
		}
	}
	return result
}

func adoptForCollection(t *testing.T, f *completionFixture, refs ...protocol.SourceRef) protocol.SourceRef {
	t.Helper()
	// Keep the fixture's canonical note, but use no incidental remote linking
	// keys: exact persisted bindings must be sufficient after a cold cache start.
	task := protocol.Task{Title: "Ship task", Attention: "attention", SourceRefs: append([]protocol.SourceRef{f.note}, refs...)}
	if _, err := f.notes.SetMuted(context.Background(), task, true); err != nil {
		t.Fatal(err)
	}
	result := f.notes.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("adoption result = %+v", result)
	}
	f.note = result.Observations[0].Ref
	return f.note
}

func TestBoundColdCollectionCompletesWithoutActiveSearchOrCache(t *testing.T) {
	f := newCompletionFixture(t)
	ref := f.ref("remote", "one", "in_progress")
	ref.LinkingKeys = nil
	adoptForCollection(t, f, ref)
	requests := []protocol.SourceBinding{}
	ref.Signal = "done"
	// No previous refs and no active search results: only the authored identity
	// requests a real provider-owned terminal observation.
	f.refresh(boundCompletionSource{completionSource: completionSource{name: "remote"}, resolved: []protocol.SourceRef{ref}, requests: &requests})
	f.assertState(t, "done")
	tasks := f.store.Tasks()
	if len(requests) != 1 || requests[0].ID != ref.ID || !tasks[0].Muted || tasks[0].DisplayGroup() != "done" {
		t.Fatalf("requests=%+v tasks=%+v", requests, tasks)
	}
	if len(tasks[0].SourceRefs) != 2 {
		t.Fatalf("cold lookup lost association: %+v", tasks)
	}
}

func TestMissingBoundWorkCannotCompleteAfterColdReset(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "failed"}[unavailable], func(t *testing.T) {
			f := newCompletionFixture(t)
			done := f.ref("first", "done", "done")
			done.LinkingKeys = nil
			absent := f.ref("second", "missing", "in_progress")
			absent.LinkingKeys = nil
			adoptForCollection(t, f, done, absent)
			result := f.refresh(boundCompletionSource{completionSource: completionSource{name: "first"}, resolved: []protocol.SourceRef{done}}, boundCompletionSource{completionSource: completionSource{name: "second"}, failed: unavailable})
			f.assertState(t, "open")
			if !f.store.Tasks()[0].Muted {
				t.Fatal("missing binding removed muted preference")
			}
			if unavailable && result.Complete["second"] {
				t.Fatal("failed lookup treated as complete")
			}
		})
	}
}

func TestMuteLifecycleRoundTripSurvivesRestartResetAndRemoteActivity(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "one", "done")
	pr.LinkingKeys = nil
	issue := f.ref("jira", "one", "in_progress")
	issue.LinkingKeys = nil
	adoptForCollection(t, f, pr, issue)
	sources := func() []integration.Source {
		return []integration.Source{boundCompletionSource{completionSource: completionSource{name: "github"}, resolved: []protocol.SourceRef{pr}}, boundCompletionSource{completionSource: completionSource{name: "jira"}, resolved: []protocol.SourceRef{issue}}}
	}
	f.refresh(sources()...)
	f.assertState(t, "open")
	if f.store.Tasks()[0].DisplayGroup() != "muted" {
		t.Fatal("active remote escaped muted")
	}
	reloaded, err := state.NewStore(f.logger)
	if err != nil {
		t.Fatal(err)
	}
	f.store = reloaded
	if !f.store.Tasks()[0].Muted {
		t.Fatal("restart lost preference")
	}
	if err := f.store.Reset(); err != nil {
		t.Fatal(err)
	}
	issue.Signal = "immediate"
	f.refresh(sources()...)
	f.assertState(t, "open")
	if task := f.store.Tasks()[0]; !task.Muted || task.Attention != "immediate" {
		t.Fatalf("remote update = %+v", task)
	}
	issue.Signal = "done"
	f.refresh(sources()...)
	f.assertState(t, "done")
	issue.Signal = "attention"
	f.refresh(sources()...)
	f.assertState(t, "open")
	task := f.store.Tasks()[0]
	if !task.Muted {
		t.Fatal("reopen implicitly unmuted")
	}
	if _, err := f.notes.SetMuted(context.Background(), task, false); err != nil {
		t.Fatal(err)
	}
	f.refresh(sources()...)
	if task := f.store.Tasks()[0]; task.Muted || task.Attention != "attention" {
		t.Fatalf("unmute = %+v", task)
	}
}

func TestNewBoundContributorPersistsAcrossReset(t *testing.T) {
	f := newCompletionFixture(t)
	first := f.ref("first", "one", "in_progress")
	first.LinkingKeys = nil
	adoptForCollection(t, f, first)
	// Ordinary linking finds another contributor; reconciliation must durably
	// record it before a future cache reset loses that incidental relationship.
	second := f.ref("second", "two", "attention")
	f.refresh(completionSource{name: "first", refs: []protocol.SourceRef{first}}, completionSource{name: "second", refs: []protocol.SourceRef{second}})
	current := f.notes.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	if len(current.Bindings) != 2 {
		t.Fatalf("new contributor not persisted: %+v", current.Bindings)
	}
	if err := f.store.Reset(); err != nil {
		t.Fatal(err)
	}
	second.LinkingKeys = nil
	f.refresh(boundCompletionSource{completionSource: completionSource{name: "first"}, resolved: []protocol.SourceRef{first}}, boundCompletionSource{completionSource: completionSource{name: "second"}, resolved: []protocol.SourceRef{second}})
	if tasks := f.store.Tasks(); len(tasks) != 1 || !tasks[0].Muted || len(tasks[0].SourceRefs) != 3 {
		t.Fatalf("new contributor detached: %+v", tasks)
	}
}

func TestSourceOnlyMuteCreatesOnlyOneNoteAndNoWorkspace(t *testing.T) {
	f := newCompletionFixture(t)
	// Remove only this test's empty initial note; all paths come from t.TempDir.
	if err := os.Remove(f.note.Metadata["note_path"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(f.note.Metadata["note_path"])); err != nil {
		t.Fatal(err)
	}
	ref := f.ref("remote", "one", "attention")
	ref.Title = "Review change"
	ref.LinkingKeys = nil
	identity, err := f.notes.SetMuted(context.Background(), protocol.Task{Title: "Review change", Attention: "attention", SourceRefs: []protocol.SourceRef{ref}}, true)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.notes.SetMuted(context.Background(), protocol.Task{Title: "Review change", Attention: "attention", SourceRefs: []protocol.SourceRef{ref}}, true)
	if err != nil || retry != identity {
		t.Fatalf("retry identity=%+v err=%v", retry, err)
	}
	f.refresh(boundCompletionSource{completionSource: completionSource{name: "remote"}, resolved: []protocol.SourceRef{ref}})
	if tasks := f.store.Tasks(); len(tasks) != 1 || !tasks[0].Muted || len(tasks[0].SourceRefs) != 2 {
		t.Fatalf("adopted projection = %+v", tasks)
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Workspaces) != 0 {
		t.Fatalf("mute created workspace: %+v", registry)
	}
}

func TestCloneTasksIsolatesBindings(t *testing.T) {
	original := []protocol.Task{{SourceRefs: []protocol.SourceRef{{Bindings: []protocol.SourceBinding{{Source: "remote", ID: "one"}}}}}}
	cloned := cloneTasks(original)
	cloned[0].SourceRefs[0].Bindings[0].ID = "changed"
	if original[0].SourceRefs[0].Bindings[0].ID != "one" {
		t.Fatal("bindings share input memory")
	}
}

type unavailableAuthor struct{ obsidian.Source }

func (unavailableAuthor) Status(context.Context, *slog.Logger) integration.StatusResult {
	return integration.StatusResult{Status: protocol.SourceStatus{Name: "obsidian", Status: "error", Detail: "vault unavailable"}}
}

func TestUnavailableAuthorRetainsMutedPreference(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "local"}[local], func(t *testing.T) {
			f := newCompletionFixture(t)
			remote := f.ref("remote", "one", "in_progress")
			remote.LinkingKeys = nil
			adoptForCollection(t, f, remote)
			f.refresh(completionSource{name: "remote", refs: []protocol.SourceRef{remote}})
			remote.Signal = "done"
			sources := []integration.Source{unavailableAuthor{f.notes}, completionSource{name: "remote", refs: []protocol.SourceRef{remote}}}
			var result Result
			if local {
				result = CollectLocal(context.Background(), f.store.CollectionTasks(), f.logger, sources)
				f.store.SetTasksForSources(result.Tasks, result.SourceNames)
			} else {
				result = Collect(context.Background(), f.store.CollectionTasks(), f.logger, sources)
				f.store.SetTasks(result.Tasks)
				if ReconcileAuthoredTasks(context.Background(), f.store.CollectionTasks(), &result, sources, f.logger) {
					t.Fatal("unavailable note authorized lifecycle")
				}
			}
			if result.Complete["obsidian"] {
				t.Fatal("cached note implies complete authority")
			}
			tasks := f.store.Tasks()
			if len(tasks) != 1 || !tasks[0].Muted || tasks[0].Attention == "done" {
				t.Fatalf("unavailable author lost preference/lifecycle: %+v", tasks)
			}
			if len(result.Sources) == 0 || result.Sources[0].Status != "error" {
				t.Fatalf("source error hidden: %+v", result.Sources)
			}
		})
	}
}

type trackingConsumer struct{ discovered, tracked *[]protocol.Task }

func (trackingConsumer) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "remote"}
}
func (s trackingConsumer) Collect(_ context.Context, req integration.CollectRequest) integration.CollectResult {
	*s.discovered = req.Previous
	return integration.CollectResult{Complete: true}
}
func (s trackingConsumer) ResolveBindings(_ context.Context, req integration.BindingRequest) integration.CollectResult {
	*s.tracked = req.Previous
	return integration.CollectResult{Complete: true}
}

func TestHiddenHistoryFeedsResolversNotTitleDiscovery(t *testing.T) {
	f := newCompletionFixture(t)
	previous := []protocol.Task{{ID: 1, Title: "ABC-1 current", Attention: "in_progress"}}
	for i := 2; i <= 62; i++ {
		previous = append(previous, protocol.Task{ID: i, Title: fmt.Sprintf("ABC-%d archived", i), Attention: "done", TrackingOnly: true, SourceRefs: []protocol.SourceRef{{ID: fmt.Sprintf("notes:%d", i), Source: "notes", Authored: true, Role: protocol.SourceRefRoleAuthoritative, Bindings: []protocol.SourceBinding{{Source: "remote", Kind: "issue", ID: fmt.Sprintf("remote:%d", i), WorkItem: true}}}}})
	}
	var discovered, tracked []protocol.Task
	result := CollectSources(context.Background(), previous, f.logger, []integration.Source{trackingConsumer{&discovered, &tracked}})
	if len(discovered) != 1 || discovered[0].ID != 1 {
		t.Fatalf("hidden history consumed title discovery budget: %+v", discovered)
	}
	if len(tracked) != len(previous) || !result.Results["remote"].Complete {
		t.Fatalf("resolver lost terminal cache: tracked=%d result=%+v", len(tracked), result.Results)
	}
}

func TestUnchangedBoundHistoryDoesNotRequireOwnershipRescan(t *testing.T) {
	ref := protocol.SourceRef{ID: "remote:one", Source: "remote", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing}
	owner := protocol.SourceRef{ID: "notes:one", Source: "notes", Authored: true, Role: protocol.SourceRefRoleAuthoritative, Bindings: []protocol.SourceBinding{ref.Binding()}}
	task := protocol.Task{SourceRefs: []protocol.SourceRef{owner, ref}}
	if hasNewBindings(owner, task) {
		t.Fatal("unchanged bindings trigger repeated ownership scan")
	}
	other := ref
	other.ID = "remote:two"
	task.SourceRefs = append(task.SourceRefs, other)
	if !hasNewBindings(owner, task) {
		t.Fatal("new contributor not persisted")
	}
	task.SourceRefs = []protocol.SourceRef{owner, ref}
	owner.Bindings[0].WorkItem = false
	if !hasNewBindings(owner, task) {
		t.Fatal("new completion authority not persisted")
	}
	owner.Bindings[0].WorkItem = true
	other = owner
	other.ID = "notes:other"
	task.SourceRefs = append(task.SourceRefs, other)
	if !hasNewBindings(owner, task) {
		t.Fatal("ambiguous authored ownership bypasses provider validation")
	}
}
