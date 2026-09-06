package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/protocol"
	"radar/internal/state"
)

type completionSource struct {
	name       string
	refs       []protocol.SourceRef
	done       []protocol.SourceRef
	incomplete bool
}

func (s completionSource) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: s.name}
}
func (s completionSource) Collect(context.Context, integration.CollectRequest) integration.CollectResult {
	return integration.CollectResult{Observations: completionObservations(s.refs), Complete: !s.incomplete}
}
func (s completionSource) Reconcile(context.Context, integration.ReconcileRequest) []integration.Observation {
	return completionObservations(s.done)
}
func completionObservations(refs []protocol.SourceRef) []integration.Observation {
	observations := make([]integration.Observation, 0, len(refs))
	for _, ref := range refs {
		observations = append(observations, integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal)})
	}
	return observations
}

type completionFixture struct {
	store  *state.Store
	notes  obsidian.Source
	note   protocol.SourceRef
	logger *slog.Logger
}

func newCompletionFixture(t *testing.T) *completionFixture {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), []byte(fmt.Sprintf(`{"workspace":{"root_dir":%q},"linking_mark_prefixes":["ABC"]}`, filepath.Join(t.TempDir(), "workspaces"))), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &completionFixture{notes: obsidian.NewSourceAt(vault), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := f.notes.Create(context.Background(), "Ship task"); err != nil {
		t.Fatal(err)
	}
	f.note = f.notes.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	var err error
	f.store, err = state.NewStore(f.logger)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *completionFixture) ref(source, id, signal string) protocol.SourceRef {
	return protocol.SourceRef{ID: source + ":" + id, Source: source, Kind: "work_item", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing,
		RetainInactive: true, CanonicalKey: source + ":" + id, LinkingKeys: []string{f.note.ID}, Signal: signal}
}

func (f *completionFixture) collect(sources ...integration.Source) (Result, []integration.Source) {
	sources = append([]integration.Source{f.notes}, sources...)
	return Collect(context.Background(), f.store.CollectionTasks(), f.logger, sources), sources
}
func (f *completionFixture) apply(result Result, sources []integration.Source) Result {
	f.store.SetTasks(result.Tasks)
	if CompleteAuthoredTasks(context.Background(), f.store.CollectionTasks(), &result, sources, f.logger) {
		f.store.SetTasks(result.Tasks)
	}
	return result
}
func (f *completionFixture) refresh(sources ...integration.Source) Result {
	result, sources := f.collect(sources...)
	return f.apply(result, sources)
}
func (f *completionFixture) assertState(t *testing.T, want string) {
	t.Helper()
	collected := f.notes.Collect(context.Background(), integration.CollectRequest{})
	if !collected.Complete || len(collected.Observations) != 1 || collected.Observations[0].Ref.Status != want {
		t.Fatalf("note collection = %+v, want %s", collected, want)
	}
	f.note = collected.Observations[0].Ref
	tasks := f.store.Tasks()
	if len(tasks) != 1 || (tasks[0].Attention == "done") != (want == "done") {
		t.Fatalf("projected tasks = %+v, want %s", tasks, want)
	}
	if want == "done" && (tasks[0].DoneAt == "" || tasks[0].DoneAt != collected.Observations[0].Ref.Metadata["completed_at"]) {
		t.Fatalf("completion timestamp = %q, note = %+v", tasks[0].DoneAt, collected.Observations[0])
	}
}

func TestAuthoredCompletionRequiresAllAuthoritativeWork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pr, jira string
		want     string
	}{
		{"all done", "done", "done", "done"},
		{"PR still open", "in_progress", "done", "open"},
		{"Jira still open", "done", "in_progress", "open"},
		{"unknown signal", "done", "", "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompletionFixture(t)
			f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{f.ref("github", "pr:acme/app:7", tc.pr)}},
				completionSource{name: "jira", refs: []protocol.SourceRef{f.ref("jira", "issue:ABC-7", tc.jira)}})
			f.assertState(t, tc.want)
		})
	}
}

func TestAuthoredCompletionUsesReconciledAndPreviouslyConfirmedDoneRefs(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "in_progress")
	jira := f.ref("jira", "issue:ABC-7", "in_progress")
	f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}}, completionSource{name: "jira", refs: []protocol.SourceRef{jira}})
	pr.Signal = "done"
	f.refresh(completionSource{name: "github", done: []protocol.SourceRef{pr}}, completionSource{name: "jira", refs: []protocol.SourceRef{jira}})
	f.assertState(t, "open")
	// GitHub no longer emits yesterday's completed PR. It must not be lost
	// while the note waits for the last Jira issue to finish.
	f.refresh(completionSource{name: "github"}, completionSource{name: "jira", refs: []protocol.SourceRef{jira}})
	f.assertState(t, "open")
	jira.Signal = "done"
	f.refresh(completionSource{name: "github"}, completionSource{name: "jira", done: []protocol.SourceRef{jira}})
	f.assertState(t, "done")
	if len(f.store.CollectionTasks()[0].SourceRefs) != 3 {
		t.Fatal("completion lost a known remote ref")
	}
	store, err := state.NewStore(f.logger)
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	f.assertState(t, "done")
	// Note lifecycle remains done even after rebuilding the cache without remotes.
	if err := f.store.Reset(); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	f.assertState(t, "done")
}

func TestAuthoredCompletionKeepsMissingAndFailedWorkAsBlockers(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "failed"}[incomplete], func(t *testing.T) {
			f := newCompletionFixture(t)
			pr := f.ref("github", "pr:acme/app:7", "done")
			jira := f.ref("jira", "issue:ABC-7", "in_progress")
			f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}}, completionSource{name: "jira", refs: []protocol.SourceRef{jira}})
			for range 3 {
				f.refresh(completionSource{name: "github"}, completionSource{name: "jira", incomplete: incomplete})
				f.assertState(t, "open")
				if len(f.store.CollectionTasks()[0].SourceRefs) != 3 {
					t.Fatal("lost missing work item")
				}
			}
			jira.Signal = "done"
			// Even explicit done refs from an incomplete source cannot close the note.
			f.refresh(completionSource{name: "github"}, completionSource{name: "jira", refs: []protocol.SourceRef{jira}, incomplete: true})
			f.assertState(t, "open")
			f.refresh(completionSource{name: "github"}, completionSource{name: "jira", refs: []protocol.SourceRef{jira}})
			f.assertState(t, "done")
		})
	}
}

func TestAuthoredCompletionIgnoresInformationalRefsAndResources(t *testing.T) {
	f := newCompletionFixture(t)
	info := f.ref("jira", "mention:ABC-7", "in_progress")
	info.Role = protocol.SourceRefRoleInformational
	resource := f.ref("local", "session:7", "in_progress")
	resource.Lifecycle, resource.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
	f.refresh(completionSource{name: "local", refs: []protocol.SourceRef{resource}})
	f.assertState(t, "open")
	pr := f.ref("github", "pr:acme/app:7", "done")
	f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}}, completionSource{name: "jira", refs: []protocol.SourceRef{info}}, completionSource{name: "local", refs: []protocol.SourceRef{resource}})
	f.assertState(t, "done")
}

func TestAuthoredCompletionChecksEveryLinkedPRAndIssue(t *testing.T) {
	f := newCompletionFixture(t)
	prs := []protocol.SourceRef{f.ref("github", "pr:acme/app:7", "done"), f.ref("github", "pr:acme/app:8", "in_progress")}
	issues := []protocol.SourceRef{f.ref("jira", "issue:ABC-7", "done"), f.ref("jira", "issue:ABC-8", "in_progress")}
	f.refresh(completionSource{name: "github", refs: prs}, completionSource{name: "jira", refs: issues})
	f.assertState(t, "open")
	prs[1].Signal = "done"
	f.refresh(completionSource{name: "github", refs: prs}, completionSource{name: "jira", refs: issues})
	f.assertState(t, "open")
	issues[1].Signal = "done"
	f.refresh(completionSource{name: "github", refs: prs}, completionSource{name: "jira", refs: issues})
	f.assertState(t, "done")
}

func TestAuthoredCompletionPreservesManualReopenAcrossCacheReset(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "done")
	remote := func() completionSource { return completionSource{name: "github", refs: []protocol.SourceRef{pr}} }
	f.refresh(remote())
	f.assertState(t, "done")
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "open"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Reset(); err != nil {
		t.Fatal(err)
	}
	f.refresh(remote())
	f.assertState(t, "open")
	if err := f.store.Reset(); err != nil {
		t.Fatal(err)
	}
	f.refresh(remote())
	f.assertState(t, "open")
	pr.Signal = "in_progress"
	f.refresh(remote())
	f.assertState(t, "open")
	pr.Signal = "done"
	f.refresh(remote())
	f.assertState(t, "done")
	// Manual completion remains terminal even with active remote work.
	pr.Signal = "in_progress"
	f.refresh(remote())
	f.assertState(t, "done")
}

func TestAuthoredCompletionDoesNotRunDuringLocalRefresh(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "done")
	result, sources := f.collect(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
	f.store.SetTasks(result.Tasks)
	local := CollectLocal(context.Background(), f.store.CollectionTasks(), f.logger, sources)
	if CompleteAuthoredTasks(context.Background(), f.store.CollectionTasks(), &local, sources, f.logger) {
		t.Fatal("local refresh completed authored work")
	}
	f.assertState(t, "open")
}

func TestAuthoredCompletionDoesNotProjectFailedOrStaleNoteWrites(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "write failure", true: "concurrent edit"}[stale], func(t *testing.T) {
			f := newCompletionFixture(t)
			pr := f.ref("github", "pr:acme/app:7", "done")
			result, sources := f.collect(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
			path := f.note.Metadata["note_path"]
			if stale {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("\nNew user notes.\n")...), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o755) })
			}
			result = f.apply(result, sources)
			f.assertState(t, "open")
			if result.Sources[0].Status != "error" || !strings.Contains(result.Sources[0].Detail, "completion failed") {
				t.Fatalf("source status = %+v", result.Sources[0])
			}
			if !stale {
				if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
			f.assertState(t, "done")
		})
	}
}

func TestAuthoredCompletionManualDoneAndReopenWithActiveWork(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "in_progress")
	remote := func() completionSource { return completionSource{name: "github", refs: []protocol.SourceRef{pr}} }
	f.refresh(remote())
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "done"); err != nil {
		t.Fatal(err)
	}
	f.refresh(remote())
	f.assertState(t, "done")
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "open"); err != nil {
		t.Fatal(err)
	}
	f.refresh(remote())
	f.assertState(t, "open")
	pr.Signal = "done"
	f.refresh(remote())
	f.assertState(t, "done")
}

func TestAuthoredCompletionRequiresSuccessfulNoteCollection(t *testing.T) {
	f := newCompletionFixture(t)
	// A malformed second note makes Obsidian collection incomplete.
	if _, err := f.notes.Create(context.Background(), "Other note"); err != nil {
		t.Fatal(err)
	}
	collected := f.notes.Collect(context.Background(), integration.CollectRequest{})
	for _, observation := range collected.Observations {
		if observation.Ref.ID != f.note.ID {
			if err := os.WriteFile(observation.Ref.Metadata["note_path"], []byte("Invalid note"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{f.ref("github", "pr:acme/app:7", "done")}})
	data, err := os.ReadFile(f.note.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "radar-state: open") {
		t.Fatalf("note was completed during failed collection: %s", data)
	}
}
