package obsidian

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
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

func TestWorkspaceNoteSanitizesBeforePlanningAndCollects(t *testing.T) {
	for _, title := range []string{`Fix CI/CD\build: "why?"`, "../outside", "line\nbreak\x00", "...", "CON", "  Setup: screenshots  ", "true", "007", "a: b # c [d]", strings.Repeat("界", 150)} {
		t.Run(title, func(t *testing.T) {
			vault := testVault(t)
			source := NewSourceAt(vault)
			ctx := context.Background()
			desired, err := source.PrepareWorkspaceNote(ctx, title)
			if err != nil {
				t.Fatal(err)
			}
			want := taskFilename(title)
			id := strings.TrimPrefix(desired.LinkingKey, "obsidian:task:")
			if desired.Title != strings.TrimSpace(title) || desired.Path != filepath.Join(taskRoot(vault), taskDirectoryName(want, id), want+".md") {
				t.Fatalf("planned path = %q", desired.Path)
			}
			// The title must survive the preview/apply JSON boundary.
			encoded, err := json.Marshal(desired)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &desired); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Dir(desired.Path)); !os.IsNotExist(err) {
				t.Fatal("preparation created a directory")
			}
			if err := source.EnsureWorkspaceNote(ctx, desired); err != nil {
				t.Fatal(err)
			}
			if err := source.EnsureWorkspaceNote(ctx, desired); err != nil {
				t.Fatalf("retry: %v", err)
			}
			result := source.Collect(ctx, integration.CollectRequest{})
			if !result.Complete || len(result.Observations) != 1 {
				t.Fatalf("collection = %+v", result)
			}
			ref := result.Observations[0].Ref
			if ref.Title != strings.TrimSpace(title) || ref.Presentation.WorkspaceName != strings.TrimSpace(title) || ref.ID != desired.LinkingKey || ref.WorkspaceAnchorPath != desired.Path {
				t.Fatalf("collected ref = %+v", ref)
			}
			if _, err := source.SetPriority(ctx, ref, "urgent"); err != nil {
				t.Fatalf("mutate sanitized note: %v", err)
			}
		})
	}
}

func TestCreateRejectsSanitizedTitleCollision(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	if _, err := source.Create(ctx, "CI/CD"); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{`CI\CD`, "CI-CD", "CI/CD"} {
		if _, err := source.Create(ctx, title); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create collision %q: %v", title, err)
		}
	}
	result := source.Collect(ctx, integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("collection after collisions = %+v", result)
	}
}

func TestWorkspaceNoteRejectsUnsanitizedPlannedPath(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	desired, err := source.PrepareWorkspaceNote(ctx, "Plan")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(desired.LinkingKey, "obsidian:task:")
	root := filepath.Dir(filepath.Dir(desired.Path))
	for _, title := range []string{"Plan: why?", "CON", "...", strings.Repeat("a", 201)} {
		desired.Path = filepath.Join(root, taskDirectoryName(title, id), title+".md")
		if err := source.EnsureWorkspaceNote(ctx, desired); err == nil {
			t.Fatalf("unsanitized planned path accepted: %q", desired.Path)
		}
		if _, err := os.Lstat(filepath.Dir(desired.Path)); !os.IsNotExist(err) {
			t.Fatal("invalid plan created a directory")
		}
	}
}

func TestExistingNoteWithUnsanitizedNameRemainsUsable(t *testing.T) {
	source := NewSourceAt(testVault(t))
	ctx := context.Background()
	desired, err := source.PrepareWorkspaceNote(ctx, "Plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnsureWorkspaceNote(ctx, desired); err != nil {
		t.Fatal(err)
	}
	oldDirectory := filepath.Dir(desired.Path)
	newDirectory := filepath.Join(filepath.Dir(oldDirectory), "Old: "+filepath.Base(oldDirectory))
	data, err := os.ReadFile(desired.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(newDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(newDirectory, "Plan: why?.md")
	if err := os.WriteFile(newPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(oldDirectory); err != nil {
		t.Fatal(err)
	}
	desired.Path = newPath
	for _, create := range []bool{true, false} {
		desired.Create = create
		if err := source.EnsureWorkspaceNote(ctx, desired); err != nil {
			t.Fatalf("existing note rejected, create=%v: %v", create, err)
		}
	}
	result := source.Collect(ctx, integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 1 || result.Observations[0].Ref.Title != "Plan" {
		t.Fatalf("existing note collection = %+v", result)
	}
	if _, err := source.SetPriority(ctx, result.Observations[0].Ref, "urgent"); err != nil {
		t.Fatal(err)
	}
}
