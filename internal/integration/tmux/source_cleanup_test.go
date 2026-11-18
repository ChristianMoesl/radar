package tmux

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

	"radar/internal/cleanup"
	"radar/internal/integration"
	gitsource "radar/internal/integration/git"
	sbxsource "radar/internal/integration/sbx"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestSessionCleanupWaitsOnlyForManagedResources(t *testing.T) {
	for _, tc := range []struct {
		name                string
		managed             bool
		nameTarget          bool
		registrationChanged bool
		missing             bool
	}{
		{name: "managed runtime ID", managed: true},
		{name: "managed session name", managed: true, nameTarget: true},
		{name: "managed registration changed while waiting", managed: true, registrationChanged: true},
		{name: "standalone"},
		{name: "missing standalone", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, "workspaces")
			anchor := filepath.Join(root, "feature")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			configPath := filepath.Join(home, "config", "radar", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, fmt.Appendf(nil, `{workspace: {root_dir: %q},linking_mark_prefixes: ["ABC"]}`, root), 0o600); err != nil {
				t.Fatal(err)
			}
			group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "feature", Path: anchor, SessionName: "managed-session"}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			callsPath := filepath.Join(home, "calls")
			name := "standalone-session"
			if tc.managed {
				name = group.SessionName
			}
			identity := "12:34:$7"
			if tc.missing {
				identity = "12::"
			}
			body := fmt.Sprintf(`#!/bin/sh
if [ "$1" = display-message ]; then
 if [ "$5" = '#{session_name}' ]; then printf '%%s\n' %q; else printf '%%s\n' '%s'; fi
 exit 0
fi
printf '%%s\n' "$*" >> %q
`, name, identity, callsPath)
			t.Setenv("RADAR_SOCKET", filepath.Join(home, "radar.sock"))
			if err := os.WriteFile(filepath.Join(home, "tmux"), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", home+string(os.PathListSeparator)+os.Getenv("PATH"))
			unlock := holdSessionCleanupNoteLock(t, root)
			target := protocol.CleanupTarget{Source: "tmux", Kind: "session", ResourceID: "$7", Title: "standalone-session", Path: anchor}
			if tc.managed {
				target.Title = group.SessionName
			}
			if tc.nameTarget {
				target.ResourceID, target.Title = group.SessionName, ""
			}
			done := make(chan error, 1)
			go func() {
				_, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target})
				done <- err
			}()
			if tc.managed {
				select {
				case err := <-done:
					t.Fatalf("managed cleanup bypassed the note lock: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				if data, err := os.ReadFile(callsPath); !os.IsNotExist(err) {
					t.Fatalf("session was touched while creator held the lock: %s, %v", data, err)
				}
				if tc.registrationChanged {
					if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						registry.Workspaces[0].SessionName = "replacement-session"
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				unlock()
			}
			select {
			case err := <-done:
				if tc.registrationChanged {
					if err == nil || !strings.Contains(err.Error(), "registration changed") {
						t.Fatalf("cleanup did not re-read registration under the lock: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not finish (standalone must not wait; managed must not recursively lock)")
			}
			unlock()
			data, err := os.ReadFile(callsPath)
			if tc.registrationChanged {
				if !os.IsNotExist(err) {
					t.Fatalf("stale managed target was removed: %s, %v", data, err)
				}
			} else {
				want := ""
				if !tc.missing {
					want = "kill-session -t $7\n"
				}
				if (err != nil && !(tc.missing && os.IsNotExist(err))) || string(data) != want {
					t.Fatalf("session cleanup calls = %q, want %q, error = %v", data, want, err)
				}
			}
		})
	}
}

func holdSessionCleanupNoteLock(t *testing.T, root string) func() {
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

func TestManagedWorkspaceCleanupDoesNotRecursivelyLock(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "workspaces")
	anchor := filepath.Join(root, "feature")
	repo := filepath.Join(home, "repo")
	member := filepath.Join(anchor, "repo--feature")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	configPath := filepath.Join(home, "config", "radar", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, fmt.Appendf(nil, `{workspace: {root_dir: %q},linking_mark_prefixes: ["ABC"]}`, root), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--initial-branch=main", repo},
		{"-C", repo, "config", "user.name", "Radar Test"},
		{"-C", repo, "config", "user.email", "radar@example.test"},
		{"-C", repo, "commit", "--allow-empty", "-m", "initial"},
		{"-C", repo, "worktree", "add", "-b", "feature", member},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	for _, name := range []string{"tmux", "sbx"} {
		body := "#!/bin/sh\nif [ \"$1\" = ls ]; then printf '[]'; fi\nexit 0\n"
		if name == "tmux" {
			body = "#!/bin/sh\nif [ \"$1\" = display-message ]; then if [ \"$5\" = '#{session_name}' ]; then echo managed-session; else echo '12:34:$7'; fi; fi\nexit 0\n"
		}
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RADAR_SOCKET", filepath.Join(home, "radar.sock"))
	t.Setenv("PATH", home+string(os.PathListSeparator)+os.Getenv("PATH"))
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "feature", Path: anchor, SessionName: "managed-session",
		Sandbox: &workspacegroup.Sandbox{Name: "managed-sandbox", Agent: "shell"},
		Members: []workspacegroup.Member{{Repository: repo, Path: member, Branch: "feature"}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	service := cleanup.New([]integration.CleanupProvider{Source{}, sbxsource.NewSource(), gitsource.NewSource(), workspace.Source{}})
	preview := protocol.CleanupPreview{Targets: []protocol.CleanupTarget{
		{Source: "tmux", Kind: "session", Title: group.SessionName, ResourceID: "$7"},
		{Source: "sbx", Kind: "sandbox", ResourceID: group.Sandbox.Name},
		{Source: "git", Kind: "worktree", Path: member, WorkspaceID: group.ID, Branch: "feature", Operation: map[string]string{"repository": repo}},
		{Source: "workspace", Kind: "workspace", SourceRefID: "workspace:" + group.ID, ResourceID: group.ID, Path: anchor},
	}}
	done := make(chan error, 1)
	go func() {
		_, err := service.Execute(context.Background(), preview, cleanup.ExecuteOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("whole workspace cleanup recursively acquired the note lock")
	}
	if _, err := os.Stat(anchor); !os.IsNotExist(err) {
		t.Fatalf("anchor still exists after whole workspace cleanup: %v", err)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatalf("registry after whole workspace cleanup = %+v, error = %v", registry, err)
	}
}
