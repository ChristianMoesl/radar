package server

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/state"
)

type blockedSnapshotFilter struct {
	started chan struct{}
	release chan struct{}
}

func (blockedSnapshotFilter) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "test-filter"}
}

func (f blockedSnapshotFilter) FilterTasks(tasks []protocol.Task, _ *slog.Logger) []protocol.Task {
	close(f.started)
	<-f.release
	return tasks
}

func TestResponseNeverLabelsPreDeletionTasksWithPostDeletionRevision(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Title: "Authored task", Attention: "low_priority", SourceRefs: []protocol.SourceRef{{ID: "author:task:one", Source: "author", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary}}}})
	before := store.Revision()
	filter := blockedSnapshotFilter{started: make(chan struct{}), release: make(chan struct{})}
	server := New(store, logger, nil, nil, nil, integration.NewRegistry(filter), cleanup.New(nil))
	responses := make(chan protocol.Response, 1)
	go func() { responses <- server.tasksResponse() }()
	<-filter.started
	// Deletion publishes while a response's filtering/serialization is still
	// processing its old task list. That response must keep the old revision.
	store.SetTasksForSources([]protocol.Task{}, []string{"author"})
	close(filter.release)
	response := <-responses
	if len(response.Tasks) != 1 || response.Revision != before || response.Revision >= store.Revision() {
		t.Fatalf("old tasks mislabeled with current revision: %+v", response)
	}
	if len(store.Tasks()) != 0 {
		t.Fatal("deletion was not published")
	}
}
