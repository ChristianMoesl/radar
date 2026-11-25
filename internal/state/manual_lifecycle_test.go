package state

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/protocol"
)

func manualStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	store, err := NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSourceOnlyManualLifecycleSaveFailureDoesNotPublish(t *testing.T) {
	store := manualStore(t)
	ref := testGitWorktreeRef("git:worktree:/workspace", "/workspace", "acme/app", "feature")
	store.SetTasks([]protocol.Task{makeTask("in_progress", "workspace", ref)})
	id := store.Tasks()[0].ID
	for _, done := range []bool{true, false} {
		path, revision := store.path, store.Revision()
		store.path = t.TempDir() // SQLite cannot write a database at a directory path.
		_, err := store.SetManualLifecycle(id, done)
		store.path = path
		if err == nil || !strings.Contains(err.Error(), "save local lifecycle") {
			t.Fatalf("expected write error, got %v", err)
		}
		if got := store.Tasks()[0]; got.ManualDone == done || store.Revision() != revision || (store.Records()[0].State == "done") == done {
			t.Fatalf("failed write changed published lifecycle: %+v", got)
		}
		if _, err := store.SetManualLifecycle(id, done); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceOnlyManualDoneDoesNotFollowReplacementWorktree(t *testing.T) {
	store := manualStore(t)
	ref := testGitWorktreeRef("git:worktree:/workspace", "/workspace", "acme/app", "feature")
	ref.BindingKey = "original"
	store.SetTasks([]protocol.Task{makeTask("in_progress", "workspace", ref)})
	if _, err := store.SetManualLifecycle(store.Tasks()[0].ID, true); err != nil {
		t.Fatal(err)
	}
	ref.BindingKey = "replacement"
	store.SetTasks([]protocol.Task{makeTask("in_progress", "workspace", ref)})
	if got := store.Tasks()[0]; got.ManualDone || got.Attention == "done" || store.Records()[0].State == "done" {
		t.Fatalf("replacement inherited completion or cleanup eligibility: %+v", got)
	}
}

func TestSourceOnlyManualDoneNeedsConfirmedNewWork(t *testing.T) {
	store := manualStore(t)
	left := testGitHubPRRef("github:pr:acme/app:1", "acme/app", "ABC-7-old")
	left.WorkflowState = "OPEN"
	right := testGitHubPRRef("github:pr:acme/app:2", "acme/app", "ABC-7-new")
	right.WorkflowState = "OPEN"
	first := makeTask("in_progress", "original PR", left)
	store.SetTasks([]protocol.Task{first})
	id := store.Tasks()[0].ID
	if _, err := store.SetManualLifecycle(id, true); err != nil {
		t.Fatal(err)
	}
	items := []protocol.Task{first, makeTask("in_progress", "new PR", right)}
	store.SetCollection(items, nil, nil, map[string]bool{"github": false})
	if got := store.Tasks()[0]; !got.ManualDone {
		t.Fatal("incomplete collection reopened manual Done")
	}
	store.SetCollection(items, nil, nil, map[string]bool{"github": true})
	if got := store.Tasks()[0]; got.ID != id || got.Attention == "done" || got.ManualDone {
		t.Fatalf("confirmed new active work did not reopen the same task: %+v", got)
	}
}

func TestSourceOnlyManualDoneCanReopenRetainedTerminalWork(t *testing.T) {
	store := manualStore(t)
	ref := testGitHubPRRef("github:pr:acme/app:1", "acme/app", "ABC-7-work")
	ref.RetainInactive, ref.WorkflowState = true, "OPEN"
	store.SetTasks([]protocol.Task{makeTask("in_progress", "PR", ref)})
	id := store.Tasks()[0].ID
	if _, err := store.SetManualLifecycle(id, true); err != nil {
		t.Fatal(err)
	}
	ref.Signal, ref.WorkflowState = "done", "MERGED"
	store.SetCollection([]protocol.Task{makeTask("done", "PR", ref)}, nil, nil, map[string]bool{"github": true})
	store.SetCollection(nil, nil, nil, map[string]bool{"github": true})
	if got := store.Tasks()[0]; !got.ManualDone {
		t.Fatalf("manual completion disappeared when a terminal ref became inactive: %+v", got)
	}
	if got, err := store.SetManualLifecycle(id, false); err != nil || got.Attention == "done" || got.ManualDone {
		t.Fatalf("could not reopen retained work: %+v %v", got, err)
	}
}
