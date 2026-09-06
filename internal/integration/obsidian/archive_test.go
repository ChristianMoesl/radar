package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func createArchiveTask(t *testing.T) (Source, protocol.SourceRef, string) {
	t.Helper()
	source := NewSourceAt(testVault(t))
	if _, err := source.Create(context.Background(), "Ship task"); err != nil {
		t.Fatal(err)
	}
	root, err := workspacegroup.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	return source, collectedArchiveRef(t, source), root
}

func collectedArchiveRef(t *testing.T, source Source) protocol.SourceRef {
	t.Helper()
	result := source.Collect(context.Background(), integration.CollectRequest{})
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("collection = %+v", result)
	}
	return result.Observations[0].Ref
}

func TestArchiveAndRestorePreserveIdentityAndContent(t *testing.T) {
	source, ref, _ := createArchiveTask(t)
	original := ref.Metadata["note_path"]
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	body := "\nUser notes with [[Other task]] and [remote](https://example.org).\n"
	if err := os.WriteFile(original, append(data, body...), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(original, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	archived := collectedArchiveRef(t, source)
	want := filepath.Join(taskRoot(source.vaultPath), "Archived", "Ship task.md")
	if archived.Metadata["note_path"] != want || archived.ID != ref.ID || archived.URL == ref.URL || archived.Status != "done" {
		t.Fatalf("archived = %+v", archived)
	}
	if _, err := os.Lstat(filepath.Dir(original)); !os.IsNotExist(err) {
		t.Fatalf("old directory not removed: %v", err)
	}
	if _, err := source.PrepareWorkspaceSeed(context.Background(), archived); err == nil || !strings.Contains(err.Error(), "reopen") {
		t.Fatalf("archived seed error = %v", err)
	}
	if err := source.ValidateWorkspaceNote(context.Background(), integration.DesiredWorkspaceNote{Path: want, LinkingKey: ref.ID}); err == nil {
		t.Fatal("archived note accepted for attachment")
	}
	if _, err := source.Create(context.Background(), "Ship task"); err == nil {
		t.Fatal("duplicate archived title accepted")
	}
	if _, err := source.SetLifecycle(context.Background(), archived, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetLifecycle(context.Background(), collectedArchiveRef(t, source), "open"); err != nil {
		t.Fatal(err)
	}
	reopened := collectedArchiveRef(t, source)
	if reopened.ID != ref.ID || reopened.Metadata["note_path"] != original || reopened.Status != "open" {
		t.Fatalf("reopened = %+v", reopened)
	}
	current, err := readNote(original)
	if err != nil || !strings.HasSuffix(current.content, body) || current.CompletedAt != "" || current.CompletionBaseline != "pending" {
		t.Fatalf("reopened note = %+v, err=%v", current, err)
	}
	info, err := os.Stat(original)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("reopened mode = %v, err=%v", info, err)
	}
	if _, err := os.Lstat(want); !os.IsNotExist(err) {
		t.Fatalf("archived copy still present: %v", err)
	}
}

func TestCompletionKeepsReferencedNotesAndLinksInPlace(t *testing.T) {
	for _, association := range []string{"primary", "secondary", "path"} {
		t.Run(association, func(t *testing.T) {
			source, ref, root := createArchiveTask(t)
			path := ref.Metadata["note_path"]
			anchor := filepath.Join(root, "workspace")
			if err := os.MkdirAll(anchor, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(anchor, "notes.md")
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			workspace := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "workspace", Path: anchor, NotePath: path}
			switch association {
			case "primary":
				workspace.TaskLinkingKey = ref.ID
			case "secondary":
				workspace.TaskLinkingKey = "jira:issue:ABC-123"
				workspace.NoteLinkingKey = ref.ID
			case "path":
				workspace.TaskLinkingKey = "jira:issue:ABC-123"
			}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Workspaces: []workspacegroup.Workspace{workspace}}); err != nil {
				t.Fatal(err)
			}
			if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
				t.Fatal(err)
			}
			if got := collectedArchiveRef(t, source); got.Metadata["note_path"] != path || got.Status != "done" {
				t.Fatalf("referenced note moved: %+v", got)
			}
			if data, err := os.ReadFile(link); err != nil || !strings.Contains(string(data), "radar-state: done") {
				t.Fatalf("broken link: %s, %v", data, err)
			}
			if _, err := source.SetLifecycle(context.Background(), ref, "open"); err != nil {
				t.Fatal(err)
			}
			if got := collectedArchiveRef(t, source); got.Metadata["note_path"] != path || got.Status != "open" {
				t.Fatalf("referenced reopened note moved: %+v", got)
			}
		})
	}
}

func TestArchiveFailuresNeverOverwriteOrRemoveNotes(t *testing.T) {
	for _, failure := range []string{"file collision", "symlink collision", "archive symlink", "archive file", "attachment", "relative link", "bad registry"} {
		t.Run(failure, func(t *testing.T) {
			source, ref, root := createArchiveTask(t)
			path := ref.Metadata["note_path"]
			archive := filepath.Join(taskRoot(source.vaultPath), "Archived")
			var protected string
			switch failure {
			case "file collision", "symlink collision":
				if err := os.Mkdir(archive, 0o755); err != nil {
					t.Fatal(err)
				}
				protected = filepath.Join(archive, filepath.Base(path))
				if failure == "symlink collision" {
					if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), protected); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(protected, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "archive symlink":
				if err := os.Symlink(t.TempDir(), archive); err != nil {
					t.Fatal(err)
				}
			case "archive file":
				protected = archive
			case "attachment":
				protected = filepath.Join(filepath.Dir(path), "image.png")
			case "relative link":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, []byte("\n[context](../Context.md)\n")...), 0o644); err != nil {
					t.Fatal(err)
				}
			case "bad registry":
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(workspacegroup.Path(root), []byte("invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if protected != "" && failure != "symlink collision" {
				if err := os.WriteFile(protected, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := source.SetLifecycle(context.Background(), ref, "done"); err == nil {
				t.Fatal("unsafe archive succeeded")
			}
			current, err := readNote(path)
			if err != nil || current.State != "done" {
				t.Fatalf("completed note lost: %+v, %v", current, err)
			}
			if protected != "" {
				if failure == "symlink collision" {
					if _, err := os.Readlink(protected); err != nil {
						t.Fatal(err)
					}
				} else if data, err := os.ReadFile(protected); err != nil || string(data) != "keep" {
					t.Fatalf("protected data changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestRestoreRefusesOccupiedPrivateDirectory(t *testing.T) {
	source, ref, _ := createArchiveTask(t)
	path := ref.Metadata["note_path"]
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	archived := collectedArchiveRef(t, source)
	if err := os.Mkdir(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetLifecycle(context.Background(), archived, "open"); err == nil {
		t.Fatal("occupied restore succeeded")
	}
	current, err := readNote(archived.Metadata["note_path"])
	if err != nil || current.State != "done" {
		t.Fatalf("archive changed: %+v, %v", current, err)
	}
}

func TestArchiveFailureCanBeRetried(t *testing.T) {
	source, ref, _ := createArchiveTask(t)
	attachment := filepath.Join(filepath.Dir(ref.Metadata["note_path"]), "unused.txt")
	if err := os.WriteFile(attachment, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err == nil {
		t.Fatal("expected accompanying file refusal")
	}
	before, err := readNote(ref.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(attachment); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	after, err := readNote(collectedArchiveRef(t, source).Metadata["note_path"])
	if err != nil || after.CompletedAt != before.CompletedAt {
		t.Fatalf("completion timestamp changed: %+v, %v", after, err)
	}
}

func TestRelativeLinkPreflight(t *testing.T) {
	for _, content := range []string{"![image](image.png)", "[note](<../Other%20note.md>)", "[ref]: ../Other.md", `src="image.png"`, "[[../Other]]"} {
		if !hasRelativeLinks(content) {
			t.Errorf("accepted relative link %q", content)
		}
	}
	for _, content := range []string{"[[Other task]]", "[web](https://example.org)", "[heading](#heading)", "ordinary body"} {
		if hasRelativeLinks(content) {
			t.Errorf("rejected portable content %q", content)
		}
	}
}

func TestArchivedDiscoveryValidatesNotesAndDetectsCrossLayoutDuplicates(t *testing.T) {
	source, ref, _ := createArchiveTask(t)
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	archived := collectedArchiveRef(t, source)
	data, err := os.ReadFile(archived.Metadata["note_path"])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(ref.Metadata["note_path"]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref.Metadata["note_path"], data, 0o644); err != nil {
		t.Fatal(err)
	}
	previous := []protocol.Task{{SourceRefs: []protocol.SourceRef{archived}}}
	result := source.Collect(context.Background(), integration.CollectRequest{Previous: previous})
	if result.Complete || result.SourceStatus.Status != "partial" || len(result.Observations) != 1 {
		t.Fatalf("duplicate collection=%+v", result)
	}
	if err := os.Remove(ref.Metadata["note_path"]); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(ref.Metadata["note_path"])); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archived.Metadata["note_path"], []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = source.Collect(context.Background(), integration.CollectRequest{Previous: previous})
	if result.Complete || result.SourceStatus.Status != "partial" || len(result.Observations) != 1 {
		t.Fatalf("malformed archive collection=%+v", result)
	}
}

func TestCollectionDoesNotMigrateExistingCompletedNotes(t *testing.T) {
	source, ref, _ := createArchiveTask(t)
	path := ref.Metadata["note_path"]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(string(data), "radar-state: open", "radar-state: done", 1)
	content = strings.Replace(content, "radar-completed-at:", "radar-completed-at: 2026-08-25T12:00:00Z", 1)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		current := collectedArchiveRef(t, source)
		if current.Metadata["note_path"] != path || current.Status != "done" {
			t.Fatalf("collection moved note: %+v", current)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != content {
		t.Fatalf("collection changed note: %s, %v", after, err)
	}
}
