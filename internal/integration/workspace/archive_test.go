package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func archiveWorkspaceFixture(t *testing.T) (string, obsidian.Source, protocol.SourceRef, workspacegroup.Workspace) {
	t.Helper()
	root := configureWorkspaceRoot(t)
	vault := filepath.Join(t.TempDir(), "Vault")
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"workspace":{"root_dir":%q},"obsidian":{"vault_path":%q},"linking_mark_prefixes":["ABC"]}`, root, vault)
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "radar", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	source := obsidian.NewSourceAt(vault)
	if _, err := source.Create(context.Background(), "Plan"); err != nil {
		t.Fatal(err)
	}
	ref := source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	anchor := filepath.Join(root, "plan")
	if err := os.MkdirAll(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoteLink(anchor, ref.Metadata["note_path"]); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "Plan", Path: anchor,
		TaskLinkingKey: "jira:issue:ABC-123", NoteLinkingKey: ref.ID, NotePath: ref.Metadata["note_path"],
		Members: []workspacegroup.Member{},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	return root, source, ref, group
}

func cleanupArchiveWorkspace(t *testing.T, group workspacegroup.Workspace) error {
	t.Helper()
	_, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: protocol.CleanupTarget{Source: "workspace", SourceRefID: "workspace:" + group.ID}})
	return err
}

func TestWorkspaceCleanupArchivesOnlyCompletedUnreferencedNote(t *testing.T) {
	for _, done := range []bool{false, true} {
		t.Run(fmt.Sprint(done), func(t *testing.T) {
			root, source, ref, group := archiveWorkspaceFixture(t)
			if done {
				if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.ReadFile(filepath.Join(group.Path, "notes.md")); err != nil {
				t.Fatal(err)
			}
			if err := cleanupArchiveWorkspace(t, group); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(group.Path); !os.IsNotExist(err) {
				t.Fatalf("anchor remains: %v", err)
			}
			registry, err := workspacegroup.Load(root)
			if err != nil || len(registry.Workspaces) != 0 {
				t.Fatalf("registry=%+v err=%v", registry, err)
			}
			collected := source.Collect(context.Background(), integration.CollectRequest{})
			if !collected.Complete || len(collected.Observations) != 1 {
				t.Fatalf("collection=%+v", collected)
			}
			got := collected.Observations[0].Ref
			want := group.NotePath
			if done {
				want = filepath.Join(filepath.Dir(filepath.Dir(group.NotePath)), "Archived", "Plan.md")
			}
			if got.Metadata["note_path"] != want || got.ID != ref.ID {
				t.Fatalf("note=%+v want=%s", got, want)
			}
		})
	}
}

func TestCleaningAnotherWorkspaceDoesNotArchiveReferencedNote(t *testing.T) {
	root, source, ref, owner := archiveWorkspaceFixture(t)
	other := workspacegroup.Workspace{Path: filepath.Join(root, "other"), Name: "Other", TaskLinkingKey: "jira:issue:ABC-456"}
	other.ID = workspacegroup.ID(other.Path)
	if err := os.Mkdir(other.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Workspaces: []workspacegroup.Workspace{owner, other}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	if err := cleanupArchiveWorkspace(t, other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(owner.Path, "notes.md")); err != nil {
		t.Fatalf("owner link broken: %v", err)
	}
	if err := cleanupArchiveWorkspace(t, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owner.NotePath); !os.IsNotExist(err) {
		t.Fatalf("note not archived: %v", err)
	}
}

func TestFailedWorkspaceCleanupDoesNotArchive(t *testing.T) {
	for _, failure := range []string{"unknown file", "member"} {
		t.Run(failure, func(t *testing.T) {
			root, source, ref, group := archiveWorkspaceFixture(t)
			if failure == "unknown file" {
				if err := os.WriteFile(filepath.Join(group.Path, "keep.txt"), []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: filepath.Join(group.Path, "member"), Branch: "feature"}}
				if err := workspacegroup.Save(root, workspacegroup.Registry{Workspaces: []workspacegroup.Workspace{group}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
				t.Fatal(err)
			}
			if err := cleanupArchiveWorkspace(t, group); err == nil {
				t.Fatal("unsafe cleanup succeeded")
			}
			if _, err := os.ReadFile(filepath.Join(group.Path, "notes.md")); err != nil {
				t.Fatalf("link broken: %v", err)
			}
		})
	}
}

func TestCleanupArchiveCollisionLeavesCompletedNote(t *testing.T) {
	root, source, ref, group := archiveWorkspaceFixture(t)
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(filepath.Dir(filepath.Dir(group.NotePath)), "Archived")
	if err := os.Mkdir(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(archive, "Plan.md")
	if err := os.WriteFile(destination, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cleanupArchiveWorkspace(t, group); err == nil || !strings.Contains(err.Error(), "workspace removed") {
		t.Fatalf("cleanup err=%v", err)
	}
	if _, err := os.ReadFile(group.NotePath); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "keep" {
		t.Fatalf("overwritten destination: %q, %v", data, err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("registry=%+v err=%v", registry, err)
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
}

func TestArchivedNoteCannotBeAttachedOrMounted(t *testing.T) {
	root, source, ref, group := archiveWorkspaceFixture(t)
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	if err := cleanupArchiveWorkspace(t, group); err != nil {
		t.Fatal(err)
	}
	archived := source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	notePath := archived.Metadata["note_path"]
	anchor := filepath.Join(root, "new")
	if err := os.Mkdir(anchor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureNoteLink(anchor, notePath); err == nil {
		t.Fatal("archived link accepted")
	}
	if err := validateWorkspaceNoteAddition(anchor, DesiredWorkspaceNote{Path: notePath, LinkingKey: ref.ID}); err == nil {
		t.Fatal("archived attachment accepted")
	}
	group.NotePath = notePath
	if _, err := desiredReconciledSandboxMounts(context.Background(), &fakeRunner{}, group, nil, nil, nil); err == nil {
		t.Fatal("shared archive mount accepted")
	}
	if _, _, err := startWorkspaceRuntime(context.Background(), &fakeRunner{}, group, ""); err == nil {
		t.Fatal("archived runtime accepted")
	}
	if _, err := source.SetLifecycle(context.Background(), archived, "open"); err != nil {
		t.Fatal(err)
	}
	reopened := source.Collect(context.Background(), integration.CollectRequest{}).Observations[0].Ref
	if err := ensureNoteLink(anchor, reopened.Metadata["note_path"]); err != nil {
		t.Fatal(err)
	}
	group.NotePath = reopened.Metadata["note_path"]
	mounts, err := desiredReconciledSandboxMounts(context.Background(), &fakeRunner{}, group, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(mounts, filepath.Dir(group.NotePath)) || contains(mounts, filepath.Dir(notePath)) || contains(mounts, filepath.Dir(filepath.Dir(notePath))) {
		t.Fatalf("unsafe mounts: %v", mounts)
	}
}

type failedSandboxCleanup struct{}

func (failedSandboxCleanup) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "sbx"}
}
func (failedSandboxCleanup) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	return nil, nil
}
func (failedSandboxCleanup) Cleanup(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return protocol.CleanupTarget{}, fmt.Errorf("sandbox still running")
}

func TestSandboxCleanupFailurePreventsNoteArchive(t *testing.T) {
	_, source, ref, group := archiveWorkspaceFixture(t)
	if _, err := source.SetLifecycle(context.Background(), ref, "done"); err != nil {
		t.Fatal(err)
	}
	service := cleanup.New([]integration.CleanupProvider{failedSandboxCleanup{}, Source{}})
	_, err := service.Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{{Source: "sbx"}, {Source: "workspace", SourceRefID: "workspace:" + group.ID}}}, cleanup.ExecuteOptions{})
	if err == nil {
		t.Fatal("expected failed sandbox cleanup")
	}
	if _, err := os.ReadFile(filepath.Join(group.Path, "notes.md")); err != nil {
		t.Fatalf("live link broken: %v", err)
	}
}
