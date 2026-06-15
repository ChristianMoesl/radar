package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"radar.nvim/internal/protocol"
)

func TestAssignTaskIDsReassignsDuplicateExplicitIDs(t *testing.T) {
	previous := []protocol.Task{
		{ID: 7, Title: "first", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:1"}}},
		{ID: 7, Title: "second", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:2"}}},
	}
	next := []protocol.Task{
		{ID: 7, Title: "first", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:1"}}},
		{ID: 7, Title: "second", SourceRefs: []protocol.SourceRef{{ID: "github:pr:acme/app:2"}}},
	}

	got := assignTaskIDs(previous, next)
	if got[0].ID != 7 {
		t.Fatalf("first ID = %d, want 7", got[0].ID)
	}
	if got[1].ID == 0 || got[1].ID == 7 {
		t.Fatalf("second ID = %d, want a new unique ID", got[1].ID)
	}
}

func TestLoadRejectsHugeStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxStateFileSize + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	store := &Store{path: path, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := store.Load(); err == nil {
		t.Fatal("Load() succeeded for huge state file, want error")
	}
}
