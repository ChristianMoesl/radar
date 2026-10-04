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

func TestDeleteMovesPrivateTaskAndAttachmentsIntactToTrash(t *testing.T) {
	source, ref, _ := createArchiveTask(t)
	path := ref.Metadata["note_path"]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("\n![image](assets/image.png)\n[[../Context]]\n")...)
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(filepath.Dir(path), "assets")
	if err := os.Mkdir(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "image.png"), []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assets, "link")); err != nil {
		t.Fatal(err)
	}
	preview, err := source.PreviewDelete(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Path != filepath.Dir(path) || !strings.Contains(preview.Description, "accompanying files") {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := os.Lstat(preview.TrashDirectory); !os.IsNotExist(err) {
		t.Fatalf("preview created trash: %v", err)
	}
	preview.TaskID = 7
	result, err := source.Delete(context.Background(), ref, preview)
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID != 7 || result.SourceRefID != ref.ID || result.OriginalPath != filepath.Dir(path) || !strings.HasPrefix(result.TrashPath, filepath.Join(source.vaultPath, ".trash")+string(os.PathSeparator)) {
		t.Fatalf("result = %+v", result)
	}
	moved := filepath.Join(result.TrashPath, filepath.Base(path))
	if got, err := os.ReadFile(moved); err != nil || string(got) != string(data) {
		t.Fatalf("note changed: %q, %v", got, err)
	}
	if info, err := os.Stat(moved); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("note permissions changed: %v, %v", info, err)
	}
	for name, want := range map[string]string{"image.png": "image bytes", "link": "outside"} {
		if got, err := os.ReadFile(filepath.Join(result.TrashPath, "assets", name)); err != nil || string(got) != want {
			t.Fatalf("attachment %s changed: %q, %v", name, got, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(result.TrashPath, "assets", "link")); err != nil || target != outside {
		t.Fatalf("attachment symlink changed: %q, %v", target, err)
	}
	if _, err := os.Lstat(preview.Path); !os.IsNotExist(err) {
		t.Fatalf("original task still exists: %v", err)
	}
	collected := source.Collect(context.Background(), integration.CollectRequest{Previous: []protocol.Task{{SourceRefs: []protocol.SourceRef{ref}}}})
	if !collected.Complete || len(collected.Observations) != 0 {
		t.Fatalf("trash was collected: %+v", collected)
	}
	// Recovery is a normal move back to the original path, without rewriting
	// the note or creating a new task identity. Re-deletion cannot overwrite.
	if err := os.Rename(result.TrashPath, result.OriginalPath); err != nil {
		t.Fatal(err)
	}
	if restored := collectedArchiveRef(t, source); restored.ID != ref.ID {
		t.Fatalf("restored identity changed: %+v", restored)
	}
	preview, err = source.PreviewDelete(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	again, err := source.Delete(context.Background(), ref, preview)
	if err != nil || again.TrashPath == result.TrashPath {
		t.Fatalf("re-deletion reused destination: %+v, %v", again, err)
	}
}

func TestDeleteArchivedTaskMovesOnlySelectedNote(t *testing.T) {
	source, ref, _ := createArchiveTask(t)
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	ref = collectedArchiveRef(t, source)
	path := ref.Metadata["note_path"]
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	neighbor := filepath.Join(filepath.Dir(path), "keep.txt")
	if err := os.WriteFile(neighbor, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := source.PreviewDelete(context.Background(), ref)
	if err != nil || preview.Path != path {
		t.Fatalf("archive preview = %+v, %v", preview, err)
	}
	result, err := source.Delete(context.Background(), ref, preview)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(result.TrashPath); err != nil || string(got) != string(original) {
		t.Fatalf("archive content changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(neighbor); err != nil || string(got) != "keep" {
		t.Fatalf("shared archive was changed: %q, %v", got, err)
	}
}

func TestDeleteRevalidatesWorkspaceReferencesAfterConfirmation(t *testing.T) {
	for _, association := range []string{"primary", "secondary", "path"} {
		t.Run(association, func(t *testing.T) {
			source, ref, root := createArchiveTask(t)
			preview, err := source.PreviewDelete(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			anchor := filepath.Join(root, "workspace")
			workspace := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "workspace", Path: anchor}
			switch association {
			case "primary":
				workspace.TaskLinkingKey = ref.ID
			case "secondary":
				workspace.TaskLinkingKey = "jira:issue:ABC-123"
				workspace.NoteLinkingKey = ref.ID
				workspace.NotePath = filepath.Join(t.TempDir(), "Other.md")
			case "path":
				workspace.TaskLinkingKey = "jira:issue:ABC-123"
				workspace.NotePath = ref.Metadata["note_path"]
			}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Workspaces: []workspacegroup.Workspace{workspace}}); err != nil {
				t.Fatal(err)
			}
			if _, err := source.PreviewDelete(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "clean up") {
				t.Fatalf("referenced preview error = %v", err)
			}
			if _, err := source.Delete(context.Background(), ref, preview); err == nil || !strings.Contains(err.Error(), "clean up") {
				t.Fatalf("referenced deletion error = %v", err)
			}
			if _, err := os.Stat(ref.Metadata["note_path"]); err != nil {
				t.Fatalf("referenced note lost: %v", err)
			}
		})
	}
}

func TestDeleteFailsClosedOnUnsafeOrStalePlan(t *testing.T) {
	for _, failure := range []string{"body edit", "added attachment", "malformed note", "identity changed", "note symlink", "directory symlink", "task root symlink", "trash symlink", "trash file", "bad registry", "extra note", "wrong path", "wrong identity", "empty revision", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			source, ref, root := createArchiveTask(t)
			path := ref.Metadata["note_path"]
			preview, err := source.PreviewDelete(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch failure {
			case "body edit", "malformed note", "identity changed":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				content := string(data) + "\nNew user content\n"
				if failure == "malformed note" {
					content = "not a task note"
				} else if failure == "identity changed" {
					content = strings.Replace(content, ref.Metadata["radar_id"], "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			case "added attachment", "extra note":
				name := "new.txt"
				if failure == "extra note" {
					name = "Other.md"
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "note symlink", "directory symlink", "task root symlink":
				target := path
				if failure == "directory symlink" {
					target = filepath.Dir(path)
				} else if failure == "task root symlink" {
					target = taskRoot(source.vaultPath)
				}
				moved := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(target, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, target); err != nil {
					t.Fatal(err)
				}
			case "trash symlink":
				if err := os.Symlink(t.TempDir(), preview.TrashDirectory); err != nil {
					t.Fatal(err)
				}
			case "trash file":
				if err := os.WriteFile(preview.TrashDirectory, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "bad registry":
				if err := os.WriteFile(workspacegroup.Path(root), []byte("invalid"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong path":
				preview.Path = source.vaultPath
			case "wrong identity":
				preview.SourceRefID = "obsidian:task:other"
			case "empty revision":
				preview.Revision = ""
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Delete(ctx, ref, preview); err == nil {
				t.Fatal("unsafe deletion succeeded")
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
				t.Fatalf("note changed on failure: %q, %v", after, err)
			}
		})
	}
}
