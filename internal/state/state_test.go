package state

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

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
