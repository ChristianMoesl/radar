package taskservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/protocol"
)

func TestCleanupGuardRefreshesAuthorAfterExternalOrFailedReopen(t *testing.T) {
	for _, failedMutation := range []bool{false, true} {
		t.Run(map[bool]string{false: "external edit", true: "post-write error"}[failedMutation], func(t *testing.T) {
			f := newFixture(t)
			done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			ref, _ := authoredRef(done, "obsidian")
			if failedMutation {
				f.author.writeError = errors.New("failed after writing open state")
				if _, err := f.service.MutateTask(context.Background(), "task-reopen", &protocol.TaskMutation{TaskID: f.task.ID}); err == nil {
					t.Fatal("expected post-write error")
				}
			} else {
				if _, err := f.author.Source.SetLifecycle(context.Background(), ref, "open"); err != nil {
					t.Fatal(err)
				}
			}
			if f.store.Records()[0].State != "done" {
				t.Fatal("fixture must leave stale completion in the cache")
			}
			called := false
			err := f.service.GuardCleanup(context.Background(), done, func() error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("reopened note authorized cleanup: guard=%v called=%v", err, called)
			}
		})
	}
}

func TestCleanupGuardRefusesUnverifiableLifecycle(t *testing.T) {
	f := newFixture(t)
	done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
	f.author.collectError = errors.New("note cannot be read")
	called := false
	err := f.service.GuardCleanup(context.Background(), done, func() error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("cleanup ran with unverified lifecycle: err=%v called=%v", err, called)
	}
}

func TestCleanupGuardDoesNotRequireUnrelatedAuthor(t *testing.T) {
	f := newFixture(t)
	f.author.collectError = errors.New("optional source unavailable")
	calls := f.author.calls.Load()
	called := false
	err := f.service.GuardCleanup(context.Background(), protocol.Task{Attention: "done"}, func() error { called = true; return nil })
	if err != nil || !called || f.author.calls.Load() != calls {
		t.Fatalf("unrelated source blocked cleanup: err=%v called=%v", err, called)
	}
}

func TestCleanupGuardIgnoresUnrelatedInvalidNote(t *testing.T) {
	f := newFixture(t)
	done := f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
	ref, _ := authoredRef(done, "obsidian")
	// Completed notes are in Tasks/Archived; an invalid sibling task directory
	// makes collection partial without making the selected note uncertain.
	root := filepath.Dir(filepath.Dir(ref.Metadata["note_path"]))
	if err := os.Mkdir(filepath.Join(root, "invalid-task"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	err := f.service.GuardCleanup(context.Background(), done, func() error { called = true; return nil })
	if err != nil || !called {
		t.Fatalf("unrelated invalid note blocked cleanup: err=%v called=%v", err, called)
	}
}
