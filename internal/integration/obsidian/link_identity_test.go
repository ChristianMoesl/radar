package obsidian

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestDistinctLinkedAuthoredTasksCanPersistIndependentReviewHolds(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	for _, title := range []string{"Independent one", "Independent two"} {
		if _, err := source.Create(ctx, title); err != nil {
			t.Fatal(err)
		}
	}
	collected := source.Collect(ctx, integration.CollectRequest{})
	for _, observation := range collected.Observations {
		if _, err := source.SetLifecycle(ctx, observation.Ref, "done", nil); err != nil {
			t.Fatal(err)
		}
	}
	collected = source.Collect(ctx, integration.CollectRequest{})
	if len(collected.Observations) != 2 {
		t.Fatal("two-note fixture missing")
	}
	var refs []protocol.SourceRef
	var tasks []protocol.Task
	for _, observation := range collected.Observations {
		ref := observation.Ref
		ref.Signal = "done"
		ref.LinkingKeys = append(ref.LinkingKeys, "branch:acme/app:shared")
		refs = append(refs, ref)
		tasks = append(tasks, protocol.Task{Title: ref.Title, Attention: "done", SourceRefs: []protocol.SourceRef{ref}})
	}
	remote := protocol.SourceRef{ID: "jira:issue:ABC-7", Source: "jira", Kind: "issue", Role: protocol.SourceRefRoleAuthoritative,
		Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityContributing,
		LinkingKeys: []string{refs[0].ID, refs[1].ID}, Signal: "in_progress"}
	tasks = append(tasks, protocol.Task{Title: "Shared contributor", Attention: "in_progress", SourceRefs: []protocol.SourceRef{remote}})
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks(tasks)
	if len(store.Tasks()) != 3 {
		t.Fatal("shared association arbitrarily chose an authored owner")
	}
	// The mutation safety net still rejects an ambiguous caller.
	if _, err := source.ReconcileLifecycle(ctx, refs[0], integration.TaskLifecycleRequest{RequireAgentReview: true, Task: protocol.Task{SourceRefs: refs}}); err == nil || !strings.Contains(err.Error(), "multiple authored") {
		t.Fatalf("ambiguous review ownership no longer fails closed: %v", err)
	}
	count := 0
	for _, task := range store.Tasks() {
		for _, ref := range task.SourceRefs {
			if !ref.Authored {
				continue
			}
			observation, err := source.ReconcileLifecycle(ctx, ref, integration.TaskLifecycleRequest{RequireAgentReview: true, Task: task})
			if err != nil || observation == nil || !observation.Ref.AgentReview {
				t.Fatalf("independent review did not persist: %+v %v", observation, err)
			}
			current, err := readNote(observation.Ref.Metadata["note_path"])
			if err != nil || !current.AgentReview {
				t.Fatal("persisted hold missing", err)
			}
			content, err := os.ReadFile(current.Path)
			if err != nil || len(content) != 0 {
				t.Fatal("review changed note content", err)
			}
			count++
		}
	}
	if count != 2 {
		t.Fatal("did not qualify both authored identities")
	}
}
