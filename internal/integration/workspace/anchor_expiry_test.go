package workspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"radar/internal/config"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func previewExpiredAnchor(t *testing.T, task protocol.Task) protocol.CleanupTarget {
	t.Helper()
	targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task, Mode: integration.CleanupExpired})
	if err != nil || len(targets) != 1 {
		t.Fatalf("expired preview = %+v, %v", targets, err)
	}
	return targets[0]
}

func assertAnchorRegistered(t *testing.T, root, id string) {
	t.Helper()
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := workspacegroup.FindByID(registry, id); !found {
		t.Fatal("workspace registration removed")
	}
}

func initAnchorRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--initial-branch=main", path).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
}

func TestExpiredAnchorRemovesUnknownEntriesWithoutFollowingSymlinks(t *testing.T) {
	root, group, task := disposableAnchorFixture(t)
	writeAnchorFile(t, filepath.Join(group.Path, "scratch.txt"))
	writeAnchorFile(t, filepath.Join(group.Path, "unknown-directory", "nested", "content"))
	outside := t.TempDir()
	initAnchorRepository(t, outside)
	writeAnchorFile(t, filepath.Join(outside, "valuable.txt"))
	for _, name := range []string{"external-link", filepath.Join("unknown-directory", "external-link")} {
		if err := os.Symlink(outside, filepath.Join(group.Path, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(group.Path, "broken-link")); err != nil {
		t.Fatal(err)
	}
	group.NotePath = filepath.Join(outside, "Canonical.md")
	writeAnchorFile(t, group.NotePath)
	if err := ensureNoteLink(group.Path, group.NotePath); err != nil {
		t.Fatal(err)
	}
	// The normal workspace mount and genuinely external target do not grant
	// anchor cleanup ownership over the external contents.
	group.Sandbox = &workspacegroup.Sandbox{
		Name: "test-sandbox", Agent: "shell", Mounts: []string{group.Path, outside + ":ro"},
		AdditionalMounts: []workspacegroup.SandboxMount{{Path: outside, ReadOnly: true}},
	}
	saveAnchorFixture(t, root, group)
	target := previewExpiredAnchor(t, task)
	var pinned []string
	if err := json.Unmarshal([]byte(target.Operation["expired_entries"]), &pinned); err != nil {
		t.Fatal(err)
	}
	want := []string{".pnpm-store", "broken-link", "external-link", "scratch.txt", "unknown-directory"}
	if !slices.Equal(pinned, want) {
		t.Fatalf("pinned entries = %v, want %v", pinned, want)
	}
	if len(target.Safety) != 1 || !target.Safety[0].Expires || !target.Safety[0].BlocksAutomatic || !strings.Contains(target.Description, target.Safety[0].Message) {
		t.Fatalf("expiry data loss missing from preview: %+v", target)
	}
	if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(group.Path); !os.IsNotExist(err) {
		t.Fatalf("expired anchor remains: %v", err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("registry = %+v, %v", registry, err)
	}
	assertAnchorFileKept(t, group.NotePath)
	assertAnchorFileKept(t, filepath.Join(outside, "valuable.txt"))
	if _, err := os.Stat(filepath.Join(outside, ".git")); err != nil {
		t.Fatalf("external primary repository changed: %v", err)
	}
}

func TestExpiredAnchorDoesNotRelaxSafeOrConfirmedCleanup(t *testing.T) {
	for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed} {
		t.Run(map[integration.CleanupMode]string{integration.CleanupSafe: "safe", integration.CleanupConfirmed: "confirmed"}[mode], func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			if _, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task, Mode: mode}); err == nil || !strings.Contains(err.Error(), "unknown files") {
				t.Fatalf("ordinary preview accepted unknown files: %v", err)
			}
			target := previewExpiredAnchor(t, task)
			// Even a later config grant cannot turn an expired-only preview
			// into an ordinary/manual confirmed deletion.
			configureDisposableEntries(t, ".pnpm-store")
			if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: mode}); err == nil {
				t.Fatal("ordinary cleanup accepted expired preview")
			}
			assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
			assertAnchorRegistered(t, root, group.ID)
		})
	}
}

func TestExpiredAnchorRevalidatesBeforeAnyDeletion(t *testing.T) {
	for _, change := range []string{
		"unknown root file", "configured root file", "config expanded", "primary checkout", "canonical note", "nested workspace", "external mount", "managed member", "invalid preview", "unpinned preview", "target path", "target workspace id", "invalid mode",
	} {
		t.Run(change, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			target := previewExpiredAnchor(t, task)
			mode := integration.CleanupExpired
			switch change {
			case "unknown root file":
				writeAnchorFile(t, filepath.Join(group.Path, "late.txt"))
			case "configured root file":
				configureDisposableEntries(t, "late.txt")
				writeAnchorFile(t, filepath.Join(group.Path, "late.txt"))
			case "config expanded":
				configureDisposableEntries(t, ".pnpm-store", "new-cache")
				writeAnchorFile(t, filepath.Join(group.Path, "new-cache", "content"))
			case "primary checkout":
				initAnchorRepository(t, filepath.Join(group.Path, ".pnpm-store", "v10", "primary"))
			case "canonical note":
				group.NotePath = filepath.Join(group.Path, ".pnpm-store", "v10", "content")
				saveAnchorFixture(t, root, group)
			case "nested workspace":
				path := filepath.Join(group.Path, ".pnpm-store", "nested")
				writeAnchorFile(t, filepath.Join(path, "valuable.txt"))
				child := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "nested", Path: path}
				if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group, child}}); err != nil {
					t.Fatal(err)
				}
			case "external mount":
				group.Sandbox = &workspacegroup.Sandbox{Name: "test-sandbox", Agent: "shell", AdditionalMounts: []workspacegroup.SandboxMount{{Path: filepath.Join(group.Path, ".pnpm-store", "v10")}}}
				saveAnchorFixture(t, root, group)
			case "managed member":
				group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: filepath.Join(group.Path, ".pnpm-store"), Branch: "feature"}}
				saveAnchorFixture(t, root, group)
			case "invalid preview":
				target.Operation["expired_entries"] = "invalid JSON"
			case "unpinned preview":
				target.Operation = nil
			case "target path":
				target.Path = filepath.Join(root, "different")
			case "target workspace id":
				target.WorkspaceID = "different"
			case "invalid mode":
				mode = integration.CleanupMode(255)
			}
			if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: mode}); err == nil {
				t.Fatal("cleanup accepted changed safety conditions")
			}
			assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
			assertAnchorRegistered(t, root, group.ID)
		})
	}
}

func TestExpiredAnchorPreflightsProtectedContentsIncludingMembers(t *testing.T) {
	for _, protected := range []string{
		"canonical root note", "canonical physical note", "canonical root symlink", "canonical unknown directory note", "canonical disposable note", "canonical member note", "another canonical note", "primary checkout", "primary anchor", "primary member", "nested primary in member", "registered primary repository", "nested workspace", "additional mount", "effective mount", "configured mount",
	} {
		t.Run(protected, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			registry := workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}
			wantError := ""
			switch protected {
			case "canonical root note":
				group.NotePath = filepath.Join(group.Path, "notes.md")
				writeAnchorFile(t, group.NotePath)
				wantError = "canonical note"
			case "canonical physical note":
				group.NotePath = filepath.Join(t.TempDir(), "Canonical.md")
				if err := os.Symlink(filepath.Join(group.Path, ".pnpm-store", "v10", "content"), group.NotePath); err != nil {
					t.Fatal(err)
				}
				wantError = "canonical note"
			case "canonical root symlink":
				group.NotePath = filepath.Join(group.Path, "canonical-link.md")
				outside := filepath.Join(t.TempDir(), "Canonical.md")
				writeAnchorFile(t, outside)
				if err := os.Symlink(outside, group.NotePath); err != nil {
					t.Fatal(err)
				}
				wantError = "canonical note"
			case "canonical unknown directory note", "canonical disposable note", "canonical member note":
				group.NotePath = filepath.Join(group.Path, ".pnpm-store", "v10", "content")
				if protected == "canonical disposable note" {
					configureDisposableEntries(t, ".pnpm-store")
				}
				if protected == "canonical member note" {
					group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: filepath.Join(group.Path, ".pnpm-store"), Branch: "feature"}}
				}
				wantError = "canonical note"
			case "another canonical note":
				otherPath := filepath.Join(root, "other")
				registry.Workspaces = append(registry.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(otherPath), Name: "other", Path: otherPath, NotePath: filepath.Join(group.Path, ".pnpm-store", "v10", "content")})
				wantError = "canonical note"
			case "primary checkout":
				initAnchorRepository(t, filepath.Join(group.Path, ".pnpm-store", "v10", "primary"))
				wantError = "Git checkout"
			case "primary anchor":
				initAnchorRepository(t, group.Path)
				wantError = "Git checkout"
			case "primary member", "nested primary in member":
				memberPath := filepath.Join(group.Path, ".pnpm-store")
				group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: memberPath, Branch: "feature"}}
				if protected == "primary member" {
					initAnchorRepository(t, memberPath)
				} else {
					writeAnchorFile(t, filepath.Join(memberPath, ".git"))
					initAnchorRepository(t, filepath.Join(memberPath, "v10", "primary"))
				}
				wantError = "Git checkout"
			case "registered primary repository":
				group.Members = []workspacegroup.Member{{Repository: filepath.Join(group.Path, ".pnpm-store", "v10"), Path: filepath.Join(group.Path, "member"), Branch: "feature"}}
				writeAnchorFile(t, filepath.Join(group.Members[0].Path, ".git"))
				wantError = "primary repository"
			case "nested workspace":
				path := filepath.Join(group.Path, ".pnpm-store", "nested")
				member := filepath.Join(path, "member")
				writeAnchorFile(t, filepath.Join(member, "valuable.txt"))
				registry.Workspaces = append(registry.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "nested", Path: path, Members: []workspacegroup.Member{{Repository: t.TempDir(), Path: member, Branch: "feature"}}})
				wantError = "registered workspace"
			case "additional mount", "effective mount", "configured mount":
				path := filepath.Join(group.Path, ".pnpm-store", "v10")
				group.Sandbox = &workspacegroup.Sandbox{Name: "test-sandbox", Agent: "shell", Mounts: []string{group.Path}}
				if protected == "additional mount" {
					group.Sandbox.AdditionalMounts = []workspacegroup.SandboxMount{{Path: path, ReadOnly: true}}
				} else if protected == "effective mount" {
					group.Sandbox.Mounts = append(group.Sandbox.Mounts, path+":ro")
				} else {
					cfg, err := config.Load()
					if err != nil {
						t.Fatal(err)
					}
					cfg.SBX.AdditionalMounts = []string{path + ":ro"}
					data, err := json.Marshal(cfg)
					if err != nil {
						t.Fatal(err)
					}
					cfgPath, err := config.Path()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				wantError = "sandbox mount"
			}
			registry.Workspaces[0] = group
			if err := workspacegroup.Save(root, registry); err != nil {
				t.Fatal(err)
			}
			if targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task, Mode: integration.CleanupExpired}); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("protected %s accepted: %+v, %v; want %q", protected, targets, err, wantError)
			}
			assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
			assertAnchorRegistered(t, root, group.ID)
		})
	}
}

func TestExpiredAnchorNeverOwnsManagedMemberEntries(t *testing.T) {
	root, group, task := disposableAnchorFixture(t)
	memberPath := filepath.Join(group.Path, ".pnpm-store")
	group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: memberPath, Branch: "feature"}}
	writeAnchorFile(t, filepath.Join(memberPath, ".git"))
	configureDisposableEntries(t, ".pnpm-store")
	saveAnchorFixture(t, root, group)
	task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{ID: "git:member", Source: "git", Kind: "worktree", Path: memberPath, WorkspaceID: group.ID})
	target := previewExpiredAnchor(t, task)
	if len(target.Operation) != 0 {
		t.Fatalf("member planned as anchor content: %+v", target)
	}
	if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err == nil {
		t.Fatal("anchor cleanup deleted a registered member")
	}
	group.Members = nil
	saveAnchorFixture(t, root, group)
	if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err == nil {
		t.Fatal("anchor cleanup deleted a reappearing unpreviewed member")
	}
	assertAnchorFileKept(t, filepath.Join(memberPath, "v10", "content"))
}

func TestExpiredAnchorRejectsUnsafePathsAtPreviewAndExecution(t *testing.T) {
	for _, unsafe := range []string{"root itself", "outside root", "anchor symlink", "parent symlink"} {
		t.Run(unsafe, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			target := previewExpiredAnchor(t, task)
			outside := t.TempDir()
			writeAnchorFile(t, filepath.Join(outside, "valuable.txt"))
			switch unsafe {
			case "root itself":
				group.Path = root
			case "outside root":
				group.Path = outside
			case "anchor symlink":
				if err := os.Rename(group.Path, group.Path+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, group.Path); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				group.Path = filepath.Join(root, "link", "child")
				writeAnchorFile(t, filepath.Join(outside, "child", "valuable.txt"))
			}
			group.ID = workspacegroup.ID(group.Path)
			saveAnchorFixture(t, root, group)
			target.SourceRefID = "workspace:" + group.ID
			target.WorkspaceID = group.ID
			target.Path = group.Path
			task.SourceRefs[0].ID = target.SourceRefID
			if _, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task, Mode: integration.CleanupExpired}); err == nil {
				t.Fatal("unsafe anchor accepted during preview")
			}
			if _, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err == nil {
				t.Fatal("unsafe anchor accepted during execution")
			}
			assertAnchorFileKept(t, filepath.Join(outside, "valuable.txt"))
			assertAnchorRegistered(t, root, group.ID)
		})
	}
}

func TestExpiredAnchorRequiresCompleteGitMemberReferences(t *testing.T) {
	for _, reference := range []string{"missing", "wrong path", "wrong workspace", "wrong source", "wrong kind", "matching"} {
		t.Run(reference, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			memberPath := filepath.Join(group.Path, ".pnpm-store")
			group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: memberPath, Branch: "feature"}}
			writeAnchorFile(t, filepath.Join(memberPath, ".git"))
			saveAnchorFixture(t, root, group)
			if reference != "missing" {
				ref := protocol.SourceRef{ID: "git:member", Source: "git", Kind: "worktree", Path: memberPath, WorkspaceID: group.ID}
				switch reference {
				case "wrong path":
					ref.Path = filepath.Join(group.Path, "different")
				case "wrong workspace":
					ref.WorkspaceID = "different"
				case "wrong source":
					ref.Source = "other"
				case "wrong kind":
					ref.Kind = "repository"
				}
				task.SourceRefs = append(task.SourceRefs, ref)
			}
			targets, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task, Mode: integration.CleanupExpired})
			if reference == "matching" {
				if err != nil || len(targets) != 1 {
					t.Fatalf("complete collection refused: %+v, %v", targets, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "missing its Git cleanup reference") || len(targets) != 0 {
				t.Fatalf("incomplete collection accepted: %+v, %v", targets, err)
			}
			// Do not alter safe/confirmed manual preview semantics.
			for _, mode := range []integration.CleanupMode{integration.CleanupSafe, integration.CleanupConfirmed} {
				if _, err := (Source{}).PreviewCleanup(context.Background(), integration.CleanupPreviewRequest{Task: task, Mode: mode}); err != nil {
					t.Fatalf("ordinary mode %d changed: %v", mode, err)
				}
			}
			assertAnchorFileKept(t, filepath.Join(memberPath, "v10", "content"))
			assertAnchorRegistered(t, root, group.ID)
		})
	}
}

func TestExpiredWorkspacePreflightRechecksLateProtectedMemberContent(t *testing.T) {
	for _, change := range []string{"canonical note", "nested primary checkout", "external mount", "registration removed", "configured root changed"} {
		t.Run(change, func(t *testing.T) {
			root, group, task := disposableAnchorFixture(t)
			memberPath := filepath.Join(group.Path, ".pnpm-store")
			group.Members = []workspacegroup.Member{{Repository: t.TempDir(), Path: memberPath, Branch: "feature"}}
			writeAnchorFile(t, filepath.Join(memberPath, ".git"))
			saveAnchorFixture(t, root, group)
			task.SourceRefs = append(task.SourceRefs, protocol.SourceRef{ID: "git:member", Source: "git", Kind: "worktree", Path: memberPath, WorkspaceID: group.ID})
			previewExpiredAnchor(t, task)
			if err := ValidateExpiredWorkspace(root, group.ID); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "canonical note":
				group.NotePath = filepath.Join(memberPath, "v10", "content")
				saveAnchorFixture(t, root, group)
			case "nested primary checkout":
				initAnchorRepository(t, filepath.Join(memberPath, "v10", "primary"))
			case "external mount":
				group.Sandbox = &workspacegroup.Sandbox{Name: "test-sandbox", Agent: "shell", AdditionalMounts: []workspacegroup.SandboxMount{{Path: filepath.Join(memberPath, "v10")}}}
				saveAnchorFixture(t, root, group)
			case "configured root changed":
				cfg, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				cfg.Workspace.RootDir = t.TempDir()
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				cfgPath, err := config.Path()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "registration removed":
				if err := workspacegroup.RemoveWorkspace(root, group.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateExpiredWorkspace(root, group.ID); err == nil {
				t.Fatal("late protected content accepted before member removal")
			}
			assertAnchorFileKept(t, filepath.Join(memberPath, "v10", "content"))
		})
	}
}

func TestExpiredAnchorRefusesFilesystemRootConfiguration(t *testing.T) {
	root, group, _ := disposableAnchorFixture(t)
	// The fixture stays in its temporary directory; do not write a registry or
	// anything else into /. Only inspect the registered path through os.Root.
	registry := workspacegroup.Registry{Workspaces: []workspacegroup.Workspace{group}}
	if err := validateExpiredAnchor("/", registry, group); err == nil || !strings.Contains(err.Error(), "unsafe configured workspace root") {
		t.Fatalf("filesystem root accepted: %v", err)
	}
	// On macOS, /var may be rejected as a symlink before root-policy
	// validation runs. Both paths must refuse, without depending on ordering.
	if _, err := anchorCleanupEntries("/", registry, group, nil, integration.CleanupExpired); err == nil {
		t.Fatal("filesystem root accepted by cleanup preview")
	}
	assertAnchorFileKept(t, filepath.Join(group.Path, ".pnpm-store", "v10", "content"))
	assertAnchorRegistered(t, root, group.ID)
}
