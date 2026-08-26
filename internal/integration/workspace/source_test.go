package workspace

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/integration/workspace/group"
	"radar/internal/linking"
	"radar/internal/protocol"
)

func configureWorkspaceRoot(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	configHome := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), []byte(fmt.Sprintf(`{"workspace":{"root_dir":%q},"linking_mark_prefixes":["ABC"]}`, root)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	return root
}

func TestCollectEmitsNoteOnlyWorkspaceAndRepairsRenamedNote(t *testing.T) {
	root := configureWorkspaceRoot(t)
	anchor := filepath.Join(root, "plan-authentication")
	directory := filepath.Join(t.TempDir(), "Plan authentication--2c965c99")
	oldNote := filepath.Join(directory, "Plan authentication.md")
	newNote := filepath.Join(directory, "Authentication plan.md")
	if err := os.MkdirAll(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nradar-id: 2c965c99-6a50-446e-834a-72656fbc056a\n---\n"
	if err := os.WriteFile(oldNote, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldNote, filepath.Join(anchor, "note.md")); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "Plan authentication", Path: anchor,
		TaskLinkingKey: "obsidian:task:2c965c99-6a50-446e-834a-72656fbc056a", NotePath: oldNote,
		Members: []workspacegroup.Member{},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldNote, newNote); err != nil {
		t.Fatal(err)
	}

	result := (Source{}).Collect(context.Background(), integration.CollectRequest{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), LinkingMarks: linking.MarkMatcher{}})
	if !result.Complete || len(result.Observations) != 1 {
		t.Fatalf("collection = %+v", result)
	}
	ref := result.Observations[0].Ref
	contracttest.AssertValidSourceRefs(t, "workspace", []protocol.SourceRef{ref})
	if ref.ID != "workspace:"+group.ID || ref.Path != anchor || !ref.ProvidesWorkspace || ref.Lifecycle != protocol.SourceRefLifecycleWorkspace || ref.Authority != protocol.SourceRefAuthorityNone {
		t.Fatalf("workspace ref = %+v", ref)
	}
	if !contains(ref.LinkingKeys, group.TaskLinkingKey) || !contains(ref.LinkingKeys, linking.WorkspaceGroupKey(group.ID)) {
		t.Fatalf("linking keys = %+v", ref.LinkingKeys)
	}
	target, err := os.Readlink(filepath.Join(anchor, "note.md"))
	if err != nil || target != newNote {
		t.Fatalf("note link = %q, err=%v", target, err)
	}
	loaded, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Workspaces[0].NotePath != newNote {
		t.Fatalf("stored note path = %q", loaded.Workspaces[0].NotePath)
	}
}

func TestCleanupRemovesOnlyAnchorAndRefusesUnknownFiles(t *testing.T) {
	root := configureWorkspaceRoot(t)
	anchor := filepath.Join(root, "plan")
	if err := os.MkdirAll(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "Plan", Path: anchor, Members: []workspacegroup.Member{}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	ref := protocol.SourceRef{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", Path: anchor}
	if err := os.WriteFile(filepath.Join(anchor, "notes.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
	if err == nil || !strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("preview error = %v", err)
	}
	if err := os.Remove(filepath.Join(anchor, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}})
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %+v, err=%v", targets, err)
	}
	if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: targets[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(anchor); !os.IsNotExist(err) {
		t.Fatalf("anchor still exists: %v", err)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
