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
	"radar/internal/integration/github/filters"
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
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.yaml"), []byte(fmt.Sprintf(`{workspace: {root_dir: %q},linking_mark_prefixes: ["ABC"]}`, filepath.Join(t.TempDir(), "workspaces"))), 0o600); err != nil {
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
		RetainInactive: true, CanonicalKey: source + ":" + id, LinkingKeys: []string{f.note.ID}, Signal: signal, WorkflowState: signal}
}

func (f *completionFixture) collect(sources ...integration.Source) (Result, []integration.Source) {
	sources = append([]integration.Source{f.notes}, sources...)
	return Collect(context.Background(), f.store.CollectionTasks(), f.logger, sources), sources
}
func (f *completionFixture) apply(result Result, sources []integration.Source) Result {
	f.store.SetTasks(result.Tasks)
	if ReconcileAuthoredTasks(context.Background(), f.store.CollectionTasks(), &result, sources, f.logger) {
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
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "open", nil); err != nil {
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
	// Reopened remote work also reopens the note.
	pr.Signal = "in_progress"
	f.refresh(remote())
	f.assertState(t, "open")
}

func TestAuthoredCompletionDoesNotRunDuringLocalRefresh(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "done")
	result, sources := f.collect(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
	f.store.SetTasks(result.Tasks)
	local := CollectLocal(context.Background(), f.store.CollectionTasks(), f.logger, sources)
	if ReconcileAuthoredTasks(context.Background(), f.store.CollectionTasks(), &local, sources, f.logger) {
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
				if err := os.Chmod(filepath.Dir(os.Getenv("RADAR_STATE")), 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(os.Getenv("RADAR_STATE")), 0o755) })
			}
			result = f.apply(result, sources)
			if !stale {
				if err := os.Chmod(filepath.Dir(os.Getenv("RADAR_STATE")), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			f.assertState(t, "open")
			if result.Sources[0].Status != "error" || !strings.Contains(result.Sources[0].Detail, "lifecycle reconciliation failed") {
				t.Fatalf("source status = %+v", result.Sources[0])
			}
			if !stale {
				if err := os.Chmod(filepath.Dir(os.Getenv("RADAR_STATE")), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
			f.assertState(t, "done")
		})
	}
}

func TestAuthoredManualCompletionWaitsForWorkflowChange(t *testing.T) {
	f := newCompletionFixture(t)
	jira := f.ref("jira", "issue:ABC-7", "in_progress")
	jira.WorkflowState = "in review"
	remote := func() completionSource { return completionSource{name: "jira", refs: []protocol.SourceRef{jira}} }
	f.refresh(remote())
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "done", []protocol.SourceRef{jira}); err != nil {
		t.Fatal(err)
	}
	f.refresh(remote())
	f.assertState(t, "done")
	// Both states classify as in_progress; the actual workflow change matters.
	jira.WorkflowState = "in progress"
	f.refresh(remote())
	f.assertState(t, "open")
	jira.Signal, jira.WorkflowState = "done", "resolved"
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
			if err := os.WriteFile(observation.Ref.Metadata["note_path"], []byte("---\nradar-state: open\n---\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{f.ref("github", "pr:acme/app:7", "done")}})
	data, err := os.ReadFile(f.note.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("note was completed during failed collection: %s", data)
	}
}

func TestAuthoredLifecycleWithoutRemoteAuthorityUsesNoteState(t *testing.T) {
	f := newCompletionFixture(t)
	info := f.ref("jira", "mention:ABC-7", "in_progress")
	info.Role = protocol.SourceRefRoleInformational
	resource := f.ref("local", "session:7", "in_progress")
	resource.Lifecycle, resource.Authority = protocol.SourceRefLifecycleResource, protocol.SourceRefAuthorityNone
	remote := completionSource{name: "jira", refs: []protocol.SourceRef{info}}
	local := completionSource{name: "local", refs: []protocol.SourceRef{resource}}
	f.refresh(remote, local)
	f.assertState(t, "open")
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "done", nil); err != nil {
		t.Fatal(err)
	}
	f.refresh(remote, local)
	f.assertState(t, "done")
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "open", nil); err != nil {
		t.Fatal(err)
	}
	f.refresh(remote, local)
	f.assertState(t, "open")
}

func TestAuthoredLifecycleReopenedTaskCanCompleteNewRemoteWork(t *testing.T) {
	f := newCompletionFixture(t)
	old := f.ref("github", "pr:acme/app:7", "done")
	remote := completionSource{name: "github", refs: []protocol.SourceRef{old}}
	f.refresh(remote)
	f.assertState(t, "done")
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "open", nil); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.refresh(remote)
		f.assertState(t, "open")
	}
	next := f.ref("github", "pr:acme/app:8", "in_progress")
	remote.refs = append(remote.refs, next)
	f.refresh(remote)
	f.assertState(t, "open")
	remote.refs[1].Signal = "done"
	f.refresh(remote)
	f.assertState(t, "done")
}

func TestAuthoredLifecycleConfirmedActiveWorkReopensDespiteOtherFailedSource(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "done")
	issue := f.ref("jira", "issue:ABC-7", "done")
	github := completionSource{name: "github", refs: []protocol.SourceRef{pr}}
	jira := completionSource{name: "jira", refs: []protocol.SourceRef{issue}}
	f.refresh(github, jira)
	f.assertState(t, "done")
	github.refs[0].Signal = "in_progress"
	github.incomplete = true
	jira.incomplete = true
	f.refresh(github, jira)
	f.assertState(t, "done") // Neither source confirmed its new observations.
	github.incomplete = false
	f.refresh(github, jira)
	f.assertState(t, "open")
	github.refs[0].Signal = "done"
	f.refresh(github, jira)
	f.assertState(t, "open") // A failed source cannot confirm all work is done.
	jira.incomplete = false
	f.refresh(github, jira)
	f.assertState(t, "done")
}

func TestAuthoredLifecycleFailedReopenDoesNotProjectSuccess(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "done")
	f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
	f.assertState(t, "done")
	pr.Signal = "in_progress"
	result, sources := f.collect(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
	path := f.note.Metadata["note_path"]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("\nConcurrent user edit.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	result = f.apply(result, sources)
	f.assertState(t, "done")
	if result.Sources[0].Status != "error" {
		t.Fatal("failed reopen not reported")
	}
	f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
	f.assertState(t, "open")
}

func TestMutedPRStillBlocksCompletesAndReopensAuthoredWork(t *testing.T) {
	f := newCompletionFixture(t)
	pr := f.ref("github", "pr:acme/app:7", "attention")
	pr.Kind, pr.Repo = "pull_request", "acme/app"
	jira := f.ref("jira", "issue:ABC-7", "done")
	rules := []filters.PullRequestRule{{Repos: []string{"acme/app"}, Action: "mute"}}
	refresh := func() {
		f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}}, completionSource{name: "jira", refs: []protocol.SourceRef{jira}})
	}
	refresh()
	f.assertState(t, "open")
	view := filters.Apply(f.store.Tasks(), rules)
	if len(view) != 1 || view[0].Attention != "low_priority" || len(view[0].SourceRefs) != 3 {
		t.Fatalf("muted PR hid other work or failed to link: %+v", view)
	}
	pr.Signal = "done"
	refresh()
	f.assertState(t, "done")
	if view := filters.Apply(f.store.Tasks(), rules); len(view) != 1 || view[0].Attention != "done" {
		t.Fatalf("muting changed authored completion: %+v", view)
	}
	pr.Signal = "in_progress"
	refresh()
	f.assertState(t, "open")
}

func TestManualDoneReconcilesEachContributorIndependently(t *testing.T) {
	f := newCompletionFixture(t)
	jira := f.ref("jira", "issue:ABC-7", "in_progress")
	jira.WorkflowState = "in review"
	pr := f.ref("github", "pr:acme/app:7", "in_progress")
	pr.WorkflowState = "OPEN"
	refresh := func() {
		f.refresh(completionSource{name: "jira", refs: []protocol.SourceRef{jira}}, completionSource{name: "github", refs: []protocol.SourceRef{pr}})
	}
	refresh()
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "done", []protocol.SourceRef{jira, pr}); err != nil {
		t.Fatal(err)
	}
	refresh()
	f.assertState(t, "done")
	doneAt := f.store.Tasks()[0].DoneAt
	// Completing the PR must not let the unchanged active Jira issue reopen us.
	pr.Signal, pr.WorkflowState = "done", "MERGED"
	refresh()
	f.assertState(t, "done")
	if f.store.Tasks()[0].DoneAt != doneAt {
		t.Fatal("terminal remote transition reset the manual completion timestamp")
	}
	// Also reconcile the snapshot when every contributor is terminal.
	jira.Signal, jira.WorkflowState = "done", "resolved"
	refresh()
	f.assertState(t, "done")
	// Record the new terminal state, not only the original state at manual Done.
	pr.Signal, pr.WorkflowState = "in_progress", "OPEN"
	refresh()
	f.assertState(t, "open")
}

func TestManualDoneSurvivesRestartResetAndNonWorkflowUpdates(t *testing.T) {
	f := newCompletionFixture(t)
	jira := f.ref("jira", "issue:ABC-7", "in_progress")
	jira.WorkflowState = "in review"
	pr := f.ref("github", "pr:acme/app:7", "done")
	pr.WorkflowState = "MERGED"
	refresh := func() {
		f.refresh(completionSource{name: "jira", refs: []protocol.SourceRef{jira}}, completionSource{name: "github", refs: []protocol.SourceRef{pr}})
	}
	refresh()
	if _, err := f.notes.SetLifecycle(context.Background(), f.note, "done", []protocol.SourceRef{jira, pr}); err != nil {
		t.Fatal(err)
	}
	refresh()
	f.assertState(t, "done")
	doneAt := f.store.Tasks()[0].DoneAt
	store, err := state.NewStore(f.logger)
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	if err := f.store.Reset(); err != nil {
		t.Fatal(err)
	}
	jira.Title, jira.Status, jira.Signal = "Edited title", "In Review", "attention"
	jira.Metadata = map[string]string{"priority": "High", "updated": "2026-10-22T12:00:00Z"}
	pr.Status, pr.Metadata = "merged yesterday", map[string]string{"unresolved_review_threads": "3", "head_oid": "new-head"}
	for range 3 {
		refresh()
		f.assertState(t, "done")
	}
	if f.store.Tasks()[0].DoneAt != doneAt {
		t.Fatal("refresh/reset changed manual completion timestamp")
	}
	jira.WorkflowState = "in progress"
	refresh()
	f.assertState(t, "open")
}

func TestManualDoneIgnoresUnconfirmedStateAndDetectsNewActiveWork(t *testing.T) {
	for _, failure := range []string{"missing", "incomplete", "unknown state"} {
		t.Run(failure, func(t *testing.T) {
			f := newCompletionFixture(t)
			jira := f.ref("jira", "issue:ABC-7", "in_progress")
			jira.WorkflowState = "in review"
			remote := completionSource{name: "jira", refs: []protocol.SourceRef{jira}}
			f.refresh(remote)
			if _, err := f.notes.SetLifecycle(context.Background(), f.note, "done", []protocol.SourceRef{jira}); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing":
				remote.refs = nil
			case "incomplete":
				remote.incomplete = true
				remote.refs[0].WorkflowState = "in progress"
			case "unknown state":
				remote.refs[0].WorkflowState = ""
			}
			f.refresh(remote)
			f.assertState(t, "done")
			// Unconfirmed observations must not replace the last confirmed state.
			jira.WorkflowState = "in progress"
			f.refresh(completionSource{name: "jira", refs: []protocol.SourceRef{jira}})
			f.assertState(t, "open")
		})
	}
	for _, signal := range []string{"done", "in_progress"} {
		t.Run("new contributor "+signal, func(t *testing.T) {
			f := newCompletionFixture(t)
			f.refresh()
			if _, err := f.notes.SetLifecycle(context.Background(), f.note, "done", nil); err != nil {
				t.Fatal(err)
			}
			pr := f.ref("github", "pr:acme/app:7", signal)
			f.refresh(completionSource{name: "github", refs: []protocol.SourceRef{pr}})
			want := "open"
			if signal == "done" {
				want = "done"
			}
			f.assertState(t, want)
		})
	}
}
