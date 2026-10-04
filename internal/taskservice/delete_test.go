package taskservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/state"
)

func TestDeletionPublishesImmediatelyAndSurvivesRestartReset(t *testing.T) {
	for _, done := range []bool{false, true} {
		t.Run(fmt.Sprintf("done=%v", done), func(t *testing.T) {
			other := &testSource{name: "remote"}
			f := newFixture(t, other)
			if done {
				f.mutate(t, "task-done", &protocol.TaskMutation{TaskID: f.task.ID})
			}
			preview, err := f.service.PreviewDeleteTask(context.Background(), f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := f.store.Revision()
			calls := f.author.calls.Load()
			result, err := f.service.DeleteTask(context.Background(), &preview)
			if err != nil {
				t.Fatal(err)
			}
			if result.TaskID != f.task.ID || f.store.Revision() <= before || f.author.calls.Load() != calls+1 || other.calls.Load() != 0 {
				t.Fatalf("deletion did not use source-scoped publication: %+v", result)
			}
			if len(f.store.Tasks()) != 0 {
				t.Fatalf("deleted task remains visible: %+v", f.store.Tasks())
			}
			if _, err := os.Stat(result.TrashPath); err != nil {
				t.Fatal(err)
			}
			restarted, err := state.NewStore(f.service.logger)
			if err != nil || len(restarted.Tasks()) != 0 {
				t.Fatalf("deleted task returned after restart: %v", err)
			}
			if err := f.service.Reset(); err != nil {
				t.Fatal(err)
			}
			// Refresh with only the author; the dummy remote has no observation.
			New(f.store, f.service.logger, f.service.integrations).Refresh(context.Background(), true)
			if len(f.store.Tasks()) != 0 {
				t.Fatal("deleted task returned after reset and collection")
			}
		})
	}
}

func TestDeletionKeepsLinkedSourcesAndTheirCachedStatus(t *testing.T) {
	remote := &testSource{name: "remote"}
	f := newFixture(t, remote)
	ref := f.linkedRef("remote", "in_progress")
	ref.Title, ref.Presentation.PreferTitle = "Remote work", true
	f.store.SetTasksForSources([]protocol.Task{{Attention: ref.Signal, SourceRefs: []protocol.SourceRef{ref}}}, []string{ref.Source})
	status := protocol.SourceStatus{Name: "remote", Status: "error", Detail: "cached error"}
	f.store.SetSources([]protocol.SourceStatus{status})
	preview, err := f.service.PreviewDeleteTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.DeleteTask(context.Background(), &preview); err != nil {
		t.Fatal(err)
	}
	tasks := f.store.Tasks()
	if len(tasks) != 1 || len(tasks[0].SourceRefs) != 1 || !reflect.DeepEqual(tasks[0].SourceRefs[0], ref) || tasks[0].Attention != "in_progress" || tasks[0].Title != "Remote work" {
		t.Fatalf("linked source changed: %+v", tasks)
	}
	if got := f.store.Sources()[0]; got != status || remote.calls.Load() != 0 {
		t.Fatalf("unrelated source was refreshed: %+v", got)
	}
}

func TestDeletionCannotBeRevivedByCollectionFailure(t *testing.T) {
	f := newFixture(t)
	preview, err := f.service.PreviewDeleteTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.author.collectError = errors.New("vault unavailable after moving note")
	if _, err := f.service.DeleteTask(context.Background(), &preview); err != nil {
		t.Fatal(err)
	}
	for _, local := range []bool{true, false} {
		f.service.Refresh(context.Background(), local)
		if len(f.store.Tasks()) != 0 {
			t.Fatalf("fallback resurrected deleted note: %+v", f.store.Tasks())
		}
	}
	if statuses := f.store.Sources(); len(statuses) != 1 || statuses[0].Status != "error" {
		t.Fatalf("collection error was hidden: %+v", statuses)
	}
}

func TestDeletionDoesNotWaitForCollectionOrLoseToStaleSnapshot(t *testing.T) {
	for _, localOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%v", localOnly), func(t *testing.T) {
			other := &testSource{name: "other", local: localOnly, started: make(chan struct{}, 8), release: make(chan struct{})}
			f := newFixture(t, other)
			other.ref = f.linkedRef("other", "in_progress")
			preview, err := f.service.PreviewDeleteTask(context.Background(), f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			for len(f.author.collected) > 0 {
				<-f.author.collected
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				f.service.Refresh(ctx, localOnly)
			}()
			released := false
			defer func() {
				if !released {
					close(other.release)
				}
				<-finished
			}()
			wait(t, other.started)
			wait(t, f.author.collected)
			deleted := make(chan error, 1)
			go func() {
				_, err := f.service.DeleteTask(ctx, &preview)
				deleted <- err
			}()
			select {
			case err := <-deleted:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("deletion waited for unrelated collection")
			}
			if len(f.store.Tasks()) != 0 {
				t.Fatal("deletion not published immediately")
			}
			close(other.release)
			released = true
			wait(t, finished)
			tasks := f.store.Tasks()
			if len(tasks) != 1 || len(tasks[0].SourceRefs) != 1 || tasks[0].SourceRefs[0].ID != other.ref.ID || other.calls.Load() != 1 {
				t.Fatalf("stale note returned or fresh remote result lost: %+v", tasks)
			}
		})
	}
}

func TestDeletionRejectsMissingAmbiguousOrChangedTargets(t *testing.T) {
	f := newFixture(t)
	if _, err := f.service.DeleteTask(context.Background(), nil); err == nil {
		t.Fatal("accepted missing confirmation")
	}
	if _, err := f.service.PreviewDeleteTask(context.Background(), 999); err == nil {
		t.Fatal("accepted nonexistent task")
	}
	preview, err := f.service.PreviewDeleteTask(context.Background(), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrong := preview
	wrong.SourceRefID = "obsidian:task:other"
	if _, err := f.service.DeleteTask(context.Background(), &wrong); err == nil || !strings.Contains(err.Error(), "target changed") {
		t.Fatalf("identity error = %v", err)
	}
	// Simulate a cache ID now pointing to different work. The confirmed source
	// identity, not the cache-local numeric ID, must protect the target.
	other := f.mutate(t, "task-create", &protocol.TaskMutation{Title: "Other task"})
	wrong = preview
	wrong.TaskID = other.ID
	if _, err := f.service.DeleteTask(context.Background(), &wrong); err == nil {
		t.Fatal("accepted reassigned task ID")
	}
	linked := other.SourceRefs[0]
	linked.LinkingKeys = append(linked.LinkingKeys, f.task.SourceRefs[0].ID)
	f.store.SetTasksForSources([]protocol.Task{f.task, {SourceRefs: []protocol.SourceRef{linked}, Attention: "low_priority"}}, []string{"obsidian"})
	if _, err := f.service.PreviewDeleteTask(context.Background(), f.task.ID); err == nil || !strings.Contains(err.Error(), "multiple authored notes") {
		t.Fatalf("ambiguous target error = %v", err)
	}
	if _, err := f.service.DeleteTask(context.Background(), &preview); err == nil {
		t.Fatal("accepted newly ambiguous target")
	}
	if _, err := os.Stat(preview.Path); err != nil {
		t.Fatalf("validation removed note: %v", err)
	}
	// A remote-only item has no authored task to delete.
	remote := f.linkedRef("remote", "in_progress")
	f.store.SetTasks([]protocol.Task{{Attention: "in_progress", SourceRefs: []protocol.SourceRef{remote}}})
	if _, err := f.service.PreviewDeleteTask(context.Background(), f.store.Tasks()[0].ID); err == nil || !strings.Contains(err.Error(), "no authored note") {
		t.Fatalf("remote-only deletion error = %v", err)
	}
}
