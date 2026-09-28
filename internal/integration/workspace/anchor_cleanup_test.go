package workspace

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

func configureDisposableEntries(t *testing.T, names ...string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workspace.Cleanup.DisposableEntries = names
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func disposableAnchorFixture(t *testing.T) (string, workspacegroup.Workspace, protocol.Task) {
	t.Helper()
	root := configureWorkspaceRoot(t)
	anchor := filepath.Join(root, "feature")
	writeAnchorFile(t, filepath.Join(anchor, ".pnpm-store", "v10", "content"))
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "feature", Path: anchor}
	saveAnchorFixture(t, root, group)
	result := (Source{}).Collect(context.Background(), integration.CollectRequest{})
	if len(result.Observations) != 1 {
		t.Fatalf("collection = %+v", result)
	}
	return root, group, protocol.Task{SourceRefs: []protocol.SourceRef{result.Observations[0].Ref}}
}

func saveAnchorFixture(t *testing.T, root string, group workspacegroup.Workspace) {
	t.Helper()
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
}

func writeAnchorFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep unless disposable"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertAnchorFileKept(t *testing.T, path string) {
	t.Helper()
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep unless disposable" {
		t.Fatalf("file changed: %s: %q, %v", path, data, err)
	}
}

func previewDisposableAnchor(t *testing.T, task protocol.Task) protocol.CleanupTarget {
	t.Helper()
	targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task})
	if err != nil || len(targets) != 1 {
		t.Fatalf("preview = %+v, %v", targets, err)
	}
	return targets[0]
}

func TestDisposableAnchorEntriesClearIssuesAndCleanUp(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[force], func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			source := Source{}
			_, err := source.PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task})
			if err == nil || !strings.Contains(err.Error(), ".pnpm-store") || len(task.SourceRefs[0].CleanupIssues) != 1 || task.SourceRefs[0].CleanupIssues[0] != err.Error() {
				t.Fatalf("unconfigured entry: preview=%v, issues=%v", err, task.SourceRefs[0].CleanupIssues)
			}
			configureDisposableEntries(t, ".pnpm-store", "scratch.log")
			writeAnchorFile(t, filepath.Join(group.Path, "scratch.log"))
			group.NotePath = filepath.Join(t.TempDir(), "Canonical.md")
			writeAnchorFile(t, group.NotePath)
			if err := ensureNoteLink(group.Path, group.NotePath); err != nil {
				t.Fatal(err)
			}
			saveAnchorFixture(t, root, group)
			result := source.Collect(context.Background(), integration.CollectRequest{})
			if len(result.Observations) != 1 || len(result.Observations[0].Ref.CleanupIssues) != 0 {
				t.Fatalf("configured entries remain unresolved: %+v", result)
			}
			target := previewDisposableAnchor(t, task)
			if len(cleanup.BlockingMessages([]protocol.CleanupTarget{target})) != 0 || len(target.Safety) != 1 || !strings.Contains(target.Safety[0].Message, ".pnpm-store, scratch.log") {
				t.Fatalf("preview did not show nonblocking deletions: %+v", target)
			}
			if !strings.Contains(target.Description, target.Safety[0].Message) {
				t.Fatalf("CLI description omitted deletions: %s", target.Description)
			}
			if _, err := source.Cleanup(context.Background(), integration.CleanupRequest{Target: target, Force: force}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(group.Path); !os.IsNotExist(err) {
				t.Fatalf("anchor remains: %v", err)
			}
			registry, err := workspacegroup.Load(root)
			if err != nil || len(registry.Workspaces) != 0 {
				t.Fatalf("registry = %+v, %v", registry, err)
			}
			assertAnchorFileKept(t, group.NotePath)
		})
	}
}

func TestAnchorCleanupRevalidatesBeforeAnyDeletion(t *testing.T) {
	for _, change := range []string{"unknown sibling", "same-prefix sibling", "permission revoked", "new disposable entry", "config expanded", "managed member", "note replaced", "invalid preview"} {
		t.Run(change, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			configureDisposableEntries(t, ".pnpm-store", "scratch.log")
			target := previewDisposableAnchor(t, task)
			switch change {
			case "unknown sibling":
				writeAnchorFile(t, filepath.Join(group.Path, "valuable.txt"))
			case "same-prefix sibling":
				writeAnchorFile(t, filepath.Join(group.Path, ".pnpm-store-backup", "content"))
			case "permission revoked":
				configureDisposableEntries(t)
			case "new disposable entry":
				writeAnchorFile(t, filepath.Join(group.Path, "scratch.log"))
			case "config expanded":
				configureDisposableEntries(t, ".pnpm-store", "new-cache")
				writeAnchorFile(t, filepath.Join(group.Path, "new-cache", "content"))
			case "managed member":
				group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: filepath.Join(group.Path, ".pnpm-store"), Branch: "feature"}}
				saveAnchorFixture(t, root, group)
			case "note replaced":
				writeAnchorFile(t, filepath.Join(group.Path, "notes.md"))
			case "invalid preview":
				target.Operation["disposable_entries"] = "not JSON"
			}
			if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Force: true}); err == nil {
				t.Fatal("cleanup bypassed changed safety conditions")
			}
			assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
			registry, err := workspacegroup.Load(root)
			if err != nil || len(registry.Workspaces) != 1 {
				t.Fatalf("registration changed: %+v, %v", registry, err)
			}
		})
	}
}

func TestDisposableSymlinksNeverDeleteTheirTargets(t *testing.T) {
	_, group, task := disposableAnchorFixture(t)
	outside := filepath.Join(t.TempDir(), "valuable.txt")
	writeAnchorFile(t, outside)
	for _, path := range []string{
		filepath.Join(group.Path, "cache-link"),
		filepath.Join(group.Path, ".pnpm-store", "outside-link"),
	} {
		if err := os.Symlink(filepath.Dir(outside), path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(group.Path, "broken-link")); err != nil {
		t.Fatal(err)
	}
	configureDisposableEntries(t, ".pnpm-store", "cache-link", "broken-link")
	target := previewDisposableAnchor(t, task)
	if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target}); err != nil {
		t.Fatal(err)
	}
	assertAnchorFileKept(t, outside)
}

func TestAnchorCleanupRejectsUnsafeLocationsAndSymlinkedAnchors(t *testing.T) {
	for _, change := range []string{"anchor symlink", "parent symlink", "parent symlink inside root", "outside root", "root itself"} {
		t.Run(change, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			configureDisposableEntries(t, ".pnpm-store")
			target := previewDisposableAnchor(t, task)
			outside := t.TempDir()
			writeAnchorFile(t, filepath.Join(outside, ".pnpm-store", "valuable"))
			switch change {
			case "anchor symlink":
				if err := os.Rename(group.Path, group.Path+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, group.Path); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				writeAnchorFile(t, filepath.Join(outside, "child", ".pnpm-store", "valuable"))
				if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				group.Path = filepath.Join(root, "link", "child")
			case "parent symlink inside root":
				other := filepath.Join(root, "other")
				writeAnchorFile(t, filepath.Join(other, "child", ".pnpm-store", "valuable"))
				if err := os.Symlink(other, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				group.Path = filepath.Join(root, "link", "child")
			case "outside root":
				group.Path = outside
			case "root itself":
				group.Path = root
			}
			group.ID = workspacegroup.ID(group.Path)
			saveAnchorFixture(t, root, group)
			target.SourceRefID = "workspace:" + group.ID
			if _, err := removeWorkspaceAnchor(root, target); err == nil {
				t.Fatal("unsafe anchor accepted")
			}
			if _, err := anchorCleanupEntries(root, group, []string{".pnpm-store"}); err == nil {
				t.Fatal("unsafe anchor not reported before execution")
			}
			assertAnchorFileKept(t, filepath.Join(outside, ".pnpm-store", "valuable"))
		})
	}
}

func TestDisposableNameCannotOverrideManagedMemberOrCanonicalNote(t *testing.T) {
	for _, protected := range []string{"member", "canonical note"} {
		t.Run(protected, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			configureDisposableEntries(t, ".pnpm-store")
			if protected == "member" {
				group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: filepath.Join(group.Path, ".pnpm-store"), Branch: "feature"}}
			} else {
				group.NotePath = filepath.Join(group.Path, ".pnpm-store", "v10", "content")
			}
			saveAnchorFixture(t, root, group)
			targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task})
			if protected == "canonical note" {
				if err == nil || !strings.Contains(err.Error(), "canonical note") {
					t.Fatalf("preview = %+v, %v", targets, err)
				}
				return
			}
			if err != nil || len(targets) != 1 || targets[0].Operation["disposable_entries"] != "" {
				t.Fatalf("managed member declared disposable: %+v, %v", targets, err)
			}
			if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: targets[0], Force: true}); err == nil {
				t.Fatal("managed member deleted by anchor cleanup")
			}
			// A removed member that reappears is not part of the pinned deletion list.
			group.Members = nil
			saveAnchorFixture(t, root, group)
			if _, err := removeWorkspaceAnchor(root, targets[0]); err == nil {
				t.Fatal("reappearing member deleted as disposable")
			}
			assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
		})
	}
}

func TestGarbageCollectionUsesDisposableEntryPolicy(t *testing.T) {
	for _, mode := range []string{"unconfigured", "configured", "unknown sibling", "dirty member", "unpublished commits", "active", "within retention"} {
		t.Run(mode, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			if mode != "unconfigured" {
				configureDisposableEntries(t, ".pnpm-store")
			}
			if mode == "unknown sibling" {
				writeAnchorFile(t, filepath.Join(group.Path, "valuable.txt"))
			}
			t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "tasks.json"))
			store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			ref := task.SourceRefs[0]
			workItem := protocol.SourceRef{
				ID: "test:item", EntityID: "test:item", Source: "test", Kind: "task",
				Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem,
				Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", LinkingKeys: ref.LinkingKeys,
			}
			if mode == "active" {
				workItem.Signal = "in_progress"
			}
			store.SetTasks([]protocol.Task{{Title: "Test", SourceRefs: []protocol.SourceRef{ref, workItem}}})
			guard := &anchorGCGuard{workspaceID: group.ID}
			if mode == "dirty member" || mode == "unpublished commits" {
				guard.reason = mode
			}
			service := cleanup.New([]integration.CleanupProvider{guard, Source{}})
			now := time.Now().Add(25 * time.Hour)
			if mode == "within retention" {
				now = time.Now()
			}
			result, err := workspacegc.Run(context.Background(), store, service, nil, now, workspacegc.Options{WorkspaceRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "configured" {
				if len(result.Deleted) != 1 || len(result.Skipped) != 0 || !guard.cleaned {
					t.Fatalf("eligible workspace not collected: %+v", result)
				}
				if _, err := os.Stat(group.Path); !os.IsNotExist(err) {
					t.Fatalf("anchor remains: %v", err)
				}
			} else {
				if len(result.Deleted) != 0 || guard.cleaned {
					t.Fatalf("unsafe/ineligible bundle partially cleaned: %+v", result)
				}
				assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
			}
		})
	}
}

// Stand-in for an earlier provider: unknown anchor contents must prevent any
// bundle side effects, while its own Git safety messages must remain effective.
type anchorGCGuard struct {
	workspaceID, reason string
	cleaned             bool
}

func (*anchorGCGuard) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "guard"}
}

func (p *anchorGCGuard) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	target := protocol.CleanupTarget{Source: "guard", WorkspaceID: p.workspaceID}
	if p.reason != "" {
		target.Safety = []protocol.CleanupSafety{{Message: p.reason, BlocksAutomatic: true}}
	}
	return []protocol.CleanupTarget{target}, nil
}

func (p *anchorGCGuard) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	p.cleaned = true
	return req.Target, nil
}

func TestCleanupToleratesAlreadyRemovedDisposableEntriesAndAnchors(t *testing.T) {
	for _, missing := range []string{"entry", "anchor"} {
		t.Run(missing, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			configureDisposableEntries(t, ".pnpm-store")
			target := previewDisposableAnchor(t, task)
			path := filepath.Join(group.Path, ".pnpm-store")
			if missing == "anchor" {
				path = group.Path
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if _, err := removeWorkspaceAnchor(root, target); err != nil {
				t.Fatal(err)
			}
			if registry, err := workspacegroup.Load(root); err != nil || len(registry.Workspaces) != 0 {
				t.Fatalf("registration remains: %+v, %v", registry, err)
			}
		})
	}
}
