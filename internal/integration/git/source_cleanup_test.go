package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestWorktreeCleanupWaitsOnlyForManagedResources(t *testing.T) {
	for _, tc := range []struct {
		name                string
		managed             bool
		pending             bool
		deleteBranch        bool
		registrationRemoved bool
	}{
		{name: "managed branch kept", managed: true},
		{name: "member not yet persisted", managed: true, pending: true},
		{name: "managed branch deleted", managed: true, deleteBranch: true},
		{name: "managed registration removed while waiting", managed: true, registrationRemoved: true},
		{name: "standalone branch kept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home := cleanPhysicalPath(t.TempDir())
			root := filepath.Join(home, "workspaces")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			configPath := filepath.Join(home, "config", "radar", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, fmt.Appendf(nil, `{workspace: {root_dir: %q},linking_mark_prefixes: ["ABC"]}`, root), 0o600); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(home, "repo")
			anchor := filepath.Join(root, "feature")
			path := filepath.Join(anchor, "repo--feature")
			if !tc.managed {
				path = filepath.Join(home, "standalone")
			}
			runGit(t, ctx, home, "init", "--initial-branch=main", repo)
			runGit(t, ctx, repo, "config", "user.email", "radar@example.test")
			runGit(t, ctx, repo, "config", "user.name", "Radar Test")
			runGit(t, ctx, repo, "commit", "--allow-empty", "-m", "initial")
			runGit(t, ctx, repo, "worktree", "add", "-b", "feature", path)
			group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "feature", Path: anchor}
			if tc.managed && !tc.pending {
				group.Members = []workspacegroup.Member{{Repository: repo, Path: path, Branch: "feature"}}
			}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			unlock := holdWorktreeCleanupNoteLock(t, root)
			target := protocol.CleanupTarget{Source: "git", Kind: "worktree", Path: path}
			if tc.managed && !tc.pending {
				target.WorkspaceID, target.Branch = group.ID, "feature"
				target.Operation = map[string]string{"repository": repo}
			}
			if tc.deleteBranch {
				target.Operation["delete_branch"] = "feature"
			}
			done := make(chan error, 1)
			go func() {
				_, err := (Source{}).Cleanup(ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupConfirmed})
				done <- err
			}()
			if tc.managed {
				select {
				case err := <-done:
					t.Fatalf("managed cleanup bypassed the note lock: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("worktree changed while creator held the lock: %v", err)
				}
				registry, err := workspacegroup.Load(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, found := workspacegroup.FindMemberByPath(registry, path); found == tc.pending {
					t.Fatal("member registration changed while creator held the lock")
				}
				if tc.pending {
					group.Members = []workspacegroup.Member{{Repository: repo, Path: path, Branch: "feature"}}
					if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
						t.Fatal(err)
					}
				}
				if tc.registrationRemoved {
					if err := workspacegroup.RemoveMember(root, path); err != nil {
						t.Fatal(err)
					}
				}
				unlock()
			}
			select {
			case err := <-done:
				if tc.registrationRemoved {
					if err == nil || !strings.Contains(err.Error(), "registration changed") {
						t.Fatalf("cleanup did not re-read registration under the lock: %v", err)
					}
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("stale managed target was removed: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("worktree still exists: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not finish (standalone must not wait; managed must not recursively lock)")
			}
			unlock()
			command := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/feature")
			command.Dir = repo
			if exists := command.Run() == nil; exists == tc.deleteBranch {
				t.Fatalf("branch exists = %v, want %v", exists, !tc.deleteBranch)
			}
			registry, err := workspacegroup.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, found := workspacegroup.FindMemberByPath(registry, path); found {
				t.Fatal("removed worktree is still registered")
			}
		})
	}
}

func holdWorktreeCleanupNoteLock(t *testing.T, root string) func() {
	t.Helper()
	lock, err := os.OpenFile(filepath.Join(root, ".radar-notes.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) })
	return func() {
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
			t.Fatal(err)
		}
	}
}
