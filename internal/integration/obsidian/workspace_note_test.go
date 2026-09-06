package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparedWorkspaceNoteCreatesOnlyOnApplyAndRetriesWithoutOverwriting(t *testing.T) {
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	author := NewSourceAt(vault)
	ctx := context.Background()
	note, err := author.PrepareWorkspaceNote(ctx, "Plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Dir(note.Path)); !os.IsNotExist(err) {
		t.Fatal("preparation created private directory")
	}
	if err := author.EnsureWorkspaceNote(ctx, note); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(note.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "---\n") {
		t.Fatal("new note has a generated body")
	}
	data = append(data, []byte("User notes\n")...)
	if err := os.WriteFile(note.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := author.EnsureWorkspaceNote(ctx, note); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(note.Path)
	if err != nil || string(after) != string(data) {
		t.Fatal("retry overwrote note")
	}
	note.LinkingKey = "obsidian:task:12345678-1234-4123-8123-123456789abc"
	if err := author.ValidateWorkspaceNote(ctx, note); err == nil {
		t.Fatal("forged note identity accepted")
	}
}

func TestWorkspaceNoteRejectsSymlinkedPrivateDirectory(t *testing.T) {
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	author := NewSourceAt(vault)
	ctx := context.Background()
	note, err := author.PrepareWorkspaceNote(ctx, "Plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Dir(note.Path)); err != nil {
		t.Fatal(err)
	}
	if err := author.EnsureWorkspaceNote(ctx, note); err == nil {
		t.Fatal("symlinked directory accepted")
	}
}
