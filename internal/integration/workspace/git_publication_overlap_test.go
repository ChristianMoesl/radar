package workspace_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/cleanup"
	"radar/internal/integration"
	gitsource "radar/internal/integration/git"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func pauseGitPublication(t *testing.T, root string, owner workspacegroup.Workspace, service bool) (func(), <-chan error, string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	repo, remote := filepath.Join(home, "repo"), filepath.Join(home, "origin.git")
	path := filepath.Join(owner.Path, "member")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(realGit, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run(home, "init", "--bare", "--initial-branch=main", remote)
	run(home, "init", "--initial-branch=main", repo)
	run(repo, "config", "user.name", "Radar Test")
	run(repo, "config", "user.email", "radar@example.test")
	run(repo, "commit", "--allow-empty", "-m", "initial")
	run(repo, "remote", "add", "origin", remote)
	run(repo, "push", "origin", "main")
	run(repo, "worktree", "add", "-b", "feature", path)
	owner.Members = []workspacegroup.Member{{Repository: repo, Path: path, Branch: "feature"}}
	if err := workspacegroup.Update(root, func(r *workspacegroup.Registry) error {
		for i := range r.Workspaces {
			if r.Workspaces[i].ID == owner.ID {
				r.Workspaces[i] = owner
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	gate := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$*\" = 'fetch --prune origin' ]; then\n : > %q\n while [ ! -e %q ]; do sleep 0.01; done\nfi\nexec %q \"$@\"\n", filepath.Join(gate, "entered"), filepath.Join(gate, "resume"), realGit)
	if err := os.WriteFile(filepath.Join(gate, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	var once sync.Once
	unblock := func() {
		once.Do(func() {
			if err := os.WriteFile(filepath.Join(gate, "resume"), nil, 0600); err != nil {
				t.Error(err)
			}
		})
	}
	target := protocol.CleanupTarget{Source: "git", Kind: "worktree", Path: path, WorkspaceID: owner.ID, Branch: "feature", Operation: map[string]string{"repository": repo, "delete_branch": "feature"}}
	finished, done := make(chan error, 1), make(chan struct{})
	go func() {
		var err error
		if service {
			_, err = cleanup.New([]integration.CleanupProvider{gitsource.Source{}}).Execute(context.Background(), protocol.CleanupPreview{Targets: []protocol.CleanupTarget{target}}, cleanup.ExecuteOptions{})
		} else {
			_, err = (gitsource.Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target})
		}
		finished <- err
		close(done)
	}()
	t.Cleanup(func() { unblock(); <-done })
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(gate, "entered")); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatal("actual publication fetch not reached", err)
		case <-deadline:
			t.Fatal("actual publication fetch not reached")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return unblock, finished, path
}

func TestGitPublicationAllowsUnrelatedRecovery(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, sandbox := range []bool{false, true} {
			for _, entry := range []string{"open", "create-existing"} {
				t.Run(fmt.Sprintf("service=%t/%s/sandbox=%t", service, entry, sandbox), func(t *testing.T) {
					root, owner, other, _ := sandboxOverlapFixture(t, sandbox)
					unblock, finished, path := pauseGitPublication(t, root, owner, service)
					before, err := workspacegroup.Load(root)
					if err != nil {
						t.Fatal(err)
					}
					runner := &recoverySandboxRunner{}
					recovered, done := make(chan error, 1), make(chan struct{})
					go func() {
						recovered <- recoverSandboxOverlap(context.Background(), runner, root, other, entry)
						close(done)
					}()
					t.Cleanup(func() { unblock(); <-done })
					select {
					case err := <-recovered:
						if err != nil {
							t.Fatal("recovery fixture failed", err)
						}
					case <-time.After(3 * time.Second):
						unblock()
						<-done
						if err := <-recovered; err != nil {
							t.Fatal("recovery fixture failed after release", err)
						}
						t.Fatal("unrelated recovery stalled inside actual managed Git publication I/O")
					}
					log := strings.Join(runner.calls, "\n")
					if !strings.Contains(log, "tmux new-session") || !strings.Contains(log, "tmux switch-client") || sandbox && !strings.Contains(log, "sbx create") {
						t.Fatal("missing recovery effects", log)
					}
					after, err := workspacegroup.Load(root)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("recovery changed registration", err)
					}
					for _, g := range []workspacegroup.Workspace{owner, other} {
						data, err := os.ReadFile(g.NotePath)
						if err != nil || string(data) != "canonical "+g.Name+"\n" {
							t.Fatal("canonical note changed", err)
						}
					}
					unblock()
					if err := <-finished; err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatal("worktree not removed", err)
					}
					for _, p := range []string{owner.Path, path} {
						release, err := workspacelease.Acquire(p, true)
						if err != nil {
							t.Fatal("admission leaked", err)
						}
						release()
					}
				})
			}
		}
	}
}

func TestGitPublicationRejectsConflictingRecovery(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, entry := range []string{"open", "create-existing"} {
			for _, alias := range []bool{false, true} {
				t.Run(fmt.Sprintf("service=%t/%s/alias=%t", service, entry, alias), func(t *testing.T) {
					root, owner, other, _ := sandboxOverlapFixture(t, true)
					unblock, finished, _ := pauseGitPublication(t, root, owner, service)
					if alias {
						path := filepath.Join(root, "alias")
						if err := os.Symlink(owner.Path, path); err != nil {
							t.Fatal(err)
						}
						oldID := other.ID
						other.ID, other.Path = workspacegroup.ID(path), path
						if err := workspacegroup.Update(root, func(r *workspacegroup.Registry) error {
							for i := range r.Workspaces {
								if r.Workspaces[i].ID == oldID {
									r.Workspaces[i] = other
								}
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
						owner = other
					}
					runner := &recoverySandboxRunner{}
					if err := recoverSandboxOverlap(context.Background(), runner, root, owner, entry); err == nil || !strings.Contains(err.Error(), "in use") {
						t.Fatal("conflicting recovery escaped admission", err)
					}
					log := strings.Join(runner.calls, "\n")
					for _, effect := range []string{"tmux new-session", "tmux switch-client", "sbx create"} {
						if strings.Contains(log, effect) {
							t.Fatal("conflict had effects", log)
						}
					}
					unblock()
					if err := <-finished; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
