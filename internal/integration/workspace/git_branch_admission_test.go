package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

type branchMutationFixture struct {
	ctx                      context.Context
	repo, root, path, linked string
}

func newBranchMutationFixture(t *testing.T) *branchMutationFixture {
	t.Helper()
	home := physicalPath(t.TempDir())
	t.Setenv("RADAR_SOCKET", filepath.Join(home, "run", "radar.sock"))
	f := &branchMutationFixture{ctx: context.Background(), repo: filepath.Join(home, "repo"), root: filepath.Join(home, "workspaces"), linked: filepath.Join(home, "linked")}
	f.path = filepath.Join(f.root, "one")
	runGitE2E(t, f.ctx, home, "init", "--initial-branch=main", f.repo)
	runGitE2E(t, f.ctx, f.repo, "config", "user.name", "Radar Test")
	runGitE2E(t, f.ctx, f.repo, "config", "user.email", "radar@example.test")
	runGitE2E(t, f.ctx, f.repo, "commit", "--allow-empty", "-m", "initial")
	runGitE2E(t, f.ctx, f.repo, "worktree", "add", "-b", "feature", f.path)
	runGitE2E(t, f.ctx, f.repo, "worktree", "add", "-b", "other", f.linked)
	return f
}
func (f *branchMutationFixture) repository(t *testing.T, kind string) string {
	t.Helper()
	path := f.repo
	if strings.HasPrefix(kind, "linked") {
		path = f.linked
	}
	if strings.HasSuffix(kind, "alias") {
		alias := filepath.Join(filepath.Dir(f.repo), "alias")
		if err := os.Symlink(path, alias); err != nil {
			t.Fatal(err)
		}
		path = alias
	}
	return path
}
func (f *branchMutationFixture) member() workspacegroup.Member {
	return workspacegroup.Member{Repository: f.repo, Path: f.path, Branch: "feature"}
}

type pausedBranchMutationRunner struct {
	ExecRunner
	command         string
	entered, resume chan struct{}
	once            sync.Once
}

func (r *pausedBranchMutationRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "git" && (r.command == "update-ref" && len(args) > 0 && args[0] == "update-ref" || strings.Join(args, " ") == r.command) {
		r.once.Do(func() { close(r.entered); <-r.resume })
	}
	return r.ExecRunner.Run(ctx, cwd, name, args...)
}
func waitBranchMutation(t *testing.T, entered <-chan struct{}, result <-chan error) {
	t.Helper()
	select {
	case <-entered:
	case err := <-result:
		t.Fatal("actual Git command boundary not reached", err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual Git command boundary not reached")
	}
}

func TestGitBranchAdmissionBlocksRecreationBeforeExpectedOIDDeletion(t *testing.T) {
	for _, kind := range []string{"primary", "primary-alias", "linked", "linked-alias"} {
		t.Run(kind, func(t *testing.T) {
			f := newBranchMutationFixture(t)
			repo := f.repository(t, kind)
			entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			runner := &pausedBranchMutationRunner{command: "update-ref", entered: entered, resume: resume}
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			result := make(chan error, 1)
			go func() {
				_, err := RemoveManagedWorktree(f.ctx, runner, f.member(), integration.CleanupSafe)
				result <- err
				close(done)
			}()
			t.Cleanup(func() { unblock(); <-done })
			waitBranchMutation(t, entered, result)
			if _, err := os.Stat(f.path); !os.IsNotExist(err) {
				t.Fatal("pause was not after actual worktree removal", err)
			}
			path := filepath.Join(f.root, "replacement")
			_, err := EnsureWorktree(f.ctx, ExecRunner{}, WorktreePlan{Repo: repo, BranchMode: integration.WorkspaceBranchExisting, Name: "feature", Branch: "feature", Path: path, Root: f.root})
			if err == nil || !strings.Contains(err.Error(), "git branch mutation is in use") {
				t.Fatal("same branch was recreated before managed deletion completed", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("refused creation changed destination", err)
			}
			unblock()
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitBranchAdmissionBlocksRemovalDuringEnsureRevalidation(t *testing.T) {
	for _, kind := range []string{"primary", "primary-alias", "linked", "linked-alias"} {
		t.Run(kind, func(t *testing.T) {
			f := newBranchMutationFixture(t)
			repo := f.repository(t, kind)
			entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			runner := &pausedBranchMutationRunner{command: "worktree list --porcelain", entered: entered, resume: resume}
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			result := make(chan error, 1)
			go func() {
				_, err := EnsureWorktree(f.ctx, runner, WorktreePlan{Repo: repo, BranchMode: integration.WorkspaceBranchExisting, Name: "feature", Branch: "feature", Path: f.path, Root: f.root})
				result <- err
				close(done)
			}()
			t.Cleanup(func() { unblock(); <-done })
			waitBranchMutation(t, entered, result)
			_, err := RemoveManagedWorktree(f.ctx, ExecRunner{}, f.member(), integration.CleanupSafe)
			if err == nil || !strings.Contains(err.Error(), "git branch mutation is in use") {
				t.Fatal("managed deletion bypassed an active same-branch writer", err)
			}
			if _, err := os.Stat(f.path); err != nil {
				t.Fatal("refused removal changed worktree", err)
			}
			unblock()
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitBranchAdmissionAllowsIndependentWriters(t *testing.T) {
	for _, kind := range []string{"different-branch", "different-repository"} {
		t.Run(kind, func(t *testing.T) {
			f := newBranchMutationFixture(t)
			entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			runner := &pausedBranchMutationRunner{command: "update-ref", entered: entered, resume: resume}
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			result := make(chan error, 1)
			go func() {
				_, err := RemoveManagedWorktree(f.ctx, runner, f.member(), integration.CleanupSafe)
				result <- err
				close(done)
			}()
			t.Cleanup(func() { unblock(); <-done })
			waitBranchMutation(t, entered, result)
			repo, branch := f.repo, "independent"
			if kind == "different-repository" {
				repo = filepath.Join(filepath.Dir(f.repo), "independent-repo")
				branch = "feature"
				runGitE2E(t, f.ctx, filepath.Dir(repo), "init", "--initial-branch=main", repo)
				runGitE2E(t, f.ctx, repo, "config", "user.name", "Radar Test")
				runGitE2E(t, f.ctx, repo, "config", "user.email", "radar@example.test")
				runGitE2E(t, f.ctx, repo, "commit", "--allow-empty", "-m", "initial")
			}
			path := filepath.Join(f.root, "independent")
			created, err := EnsureWorktree(f.ctx, ExecRunner{}, WorktreePlan{Repo: repo, BranchMode: integration.WorkspaceBranchNew, Name: branch, Branch: branch, Base: "main", Path: path, Root: f.root})
			if err != nil || !created.Created {
				t.Fatal("independent writer was blocked", created, err)
			}
			unblock()
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("independent worktree changed", err)
			}
			runGitE2E(t, f.ctx, repo, "show-ref", "--verify", "refs/heads/"+branch)
		})
	}
}

type branchOutcomeRunner struct {
	ExecRunner
	run func(context.Context, string, string, ...string) (string, error)
}

func (r branchOutcomeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	return r.run(ctx, cwd, name, args...)
}

func TestGitBranchAdmissionReleasesAfterOutcomes(t *testing.T) {
	for _, operation := range []string{"ensure", "remove"} {
		for _, kind := range []string{"success", "planning-error", "effect-error", "cancelled", "identity-error", "empty-identity", "relative-identity", "dangling-identity", "missing-identity", "file-identity"} {
			t.Run(operation+"/"+kind, func(t *testing.T) {
				f := newBranchMutationFixture(t)
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				branch := "feature"
				if operation == "ensure" {
					branch = "fresh"
				}
				stop := errors.New("fixture Git failure")
				effects := 0
				runner := branchOutcomeRunner{run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					command := strings.Join(args, " ")
					if name == "git" && command == "rev-parse --path-format=absolute --git-common-dir" {
						switch kind {
						case "identity-error":
							return "", stop
						case "empty-identity":
							return "", nil
						case "relative-identity":
							return ".git", nil
						case "missing-identity":
							return filepath.Join(filepath.Dir(f.repo), "missing"), nil
						case "file-identity":
							path := filepath.Join(filepath.Dir(f.repo), "not-a-directory")
							if err := os.WriteFile(path, nil, 0o600); err != nil {
								return "", err
							}
							return path, nil
						case "dangling-identity":
							path := filepath.Join(filepath.Dir(f.repo), "dangling")
							if err := os.Symlink(filepath.Join(filepath.Dir(f.repo), "missing"), path); err != nil {
								return "", err
							}
							return path, nil
						}
					}
					if name == "git" && command == "worktree list --porcelain" {
						if kind == "planning-error" {
							return "", stop
						}
						if kind == "cancelled" {
							cancel()
							return "", ctx.Err()
						}
					}
					if name == "git" && (strings.Contains(command, "worktree add") || strings.Contains(command, "worktree remove") || strings.HasPrefix(command, "update-ref")) {
						effects++
						if kind == "effect-error" {
							return "", stop
						}
					}
					return (ExecRunner{}).Run(ctx, cwd, name, args...)
				}}
				var err error
				if operation == "ensure" {
					_, err = EnsureWorktree(ctx, runner, WorktreePlan{Repo: f.repo, BranchMode: integration.WorkspaceBranchNew, Name: branch, Branch: branch, Base: "main", Path: filepath.Join(f.root, "fresh"), Root: f.root})
				} else {
					_, err = RemoveManagedWorktree(ctx, runner, f.member(), integration.CleanupSafe)
				}
				if (err == nil) != (kind == "success") {
					t.Fatal("wrong operation outcome", err)
				}
				if kind == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
				if kind != "success" && kind != "effect-error" && effects != 0 {
					t.Fatal("failed identity/plan had effects", effects)
				}
				release, err := workspacelease.AcquireGitBranchMutation(filepath.Join(f.repo, ".git"), branch)
				if err != nil {
					t.Fatal("branch admission leaked", err)
				}
				release()
			})
		}
	}
}

func TestGitBranchAdmissionCoversCopyRollbackAndPartialDeletion(t *testing.T) {
	for _, kind := range []string{"copy-rollback", "ref-delete-error"} {
		t.Run(kind, func(t *testing.T) {
			f := newBranchMutationFixture(t)
			branch := "feature"
			if kind == "copy-rollback" {
				branch = "fresh"
				if err := os.Mkdir(filepath.Join(f.repo, "bad-copy"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.repo, ".radar.yaml"), []byte("copy_files: [bad-copy]\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			observed := 0
			stop := errors.New("fixture ref deletion failed")
			runner := branchOutcomeRunner{run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				command := strings.Join(args, " ")
				check := kind == "copy-rollback" && strings.HasPrefix(command, "worktree remove --force") || kind == "ref-delete-error" && strings.HasPrefix(command, "update-ref")
				if name == "git" && check {
					observed++
					if release, err := workspacelease.AcquireGitBranchMutation(filepath.Join(f.repo, ".git"), branch); err == nil {
						release()
						t.Error("branch admission dropped before rollback/ref deletion")
						return "", fmt.Errorf("branch admission dropped before rollback/ref deletion")
					}
					if kind == "ref-delete-error" {
						return "", stop
					}
				}
				return (ExecRunner{}).Run(ctx, cwd, name, args...)
			}}
			var err error
			if kind == "copy-rollback" {
				_, err = EnsureWorktree(f.ctx, runner, WorktreePlan{Repo: f.repo, BranchMode: integration.WorkspaceBranchNew, Name: branch, Branch: branch, Base: "main", Path: filepath.Join(f.root, "fresh"), Root: f.root})
			} else {
				_, err = RemoveManagedWorktree(f.ctx, runner, f.member(), integration.CleanupSafe)
			}
			if err == nil || observed != 1 {
				t.Fatal("partial outcome boundary not covered", observed, err)
			}
			if kind == "ref-delete-error" && !strings.Contains(err.Error(), "worktree removed but local branch") {
				t.Fatal("partial effect hidden", err)
			}
			if kind == "copy-rollback" {
				if _, err := os.Stat(filepath.Join(f.root, "fresh")); !os.IsNotExist(err) {
					t.Fatal("rollback did not remove worktree", err)
				}
			}
			release, err := workspacelease.AcquireGitBranchMutation(filepath.Join(f.repo, ".git"), branch)
			if err != nil {
				t.Fatal("partial outcome leaked branch admission", err)
			}
			release()
		})
	}
}

func TestGitBranchAdmissionPreCancellationHasNoCommands(t *testing.T) {
	for _, operation := range []string{"ensure", "remove"} {
		t.Run(operation, func(t *testing.T) {
			f := newBranchMutationFixture(t)
			ctx, cancel := context.WithCancel(f.ctx)
			cancel()
			calls := 0
			runner := branchOutcomeRunner{run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
				calls++
				return (ExecRunner{}).Run(ctx, cwd, name, args...)
			}}
			var err error
			if operation == "ensure" {
				_, err = EnsureWorktree(ctx, runner, WorktreePlan{Repo: f.repo, BranchMode: integration.WorkspaceBranchExisting, Branch: "feature", Path: f.path, Root: f.root})
			} else {
				_, err = RemoveManagedWorktree(ctx, runner, f.member(), integration.CleanupSafe)
			}
			if !errors.Is(err, context.Canceled) || calls != 0 {
				t.Fatal("precancelled operation ran commands", calls, err)
			}
		})
	}
}

func TestGitBranchAdmissionCoversActualCreation(t *testing.T) {
	for _, branch := range []string{"feature", "refs/heads/feature", "origin/feature", "refs/remotes/origin/feature"} {
		t.Run(branch, func(t *testing.T) {
			f := newBranchMutationFixture(t)
			runGitE2E(t, f.ctx, f.repo, "worktree", "remove", f.path)
			entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			runner := &pausedBranchMutationRunner{command: "worktree add " + f.path + " feature", entered: entered, resume: resume}
			var once sync.Once
			unblock := func() { once.Do(func() { close(resume) }) }
			result := make(chan error, 1)
			go func() {
				_, err := EnsureWorktree(f.ctx, runner, WorktreePlan{Repo: f.repo, BranchMode: integration.WorkspaceBranchExisting, Name: "feature", Branch: branch, Path: f.path, Root: f.root})
				result <- err
				close(done)
			}()
			t.Cleanup(func() { unblock(); <-done })
			waitBranchMutation(t, entered, result)
			if _, err := RemoveManagedWorktree(f.ctx, ExecRunner{}, f.member(), integration.CleanupSafe); err == nil || !strings.Contains(err.Error(), "git branch mutation is in use") {
				t.Fatal("cleanup bypassed actual worktree creation", err)
			}
			unblock()
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(f.path); err != nil {
				t.Fatal("creation did not complete", err)
			}
			if _, err := RemoveManagedWorktree(f.ctx, ExecRunner{}, f.member(), integration.CleanupSafe); err != nil {
				t.Fatal("completed creation leaked branch admission", err)
			}
		})
	}
}

func TestGitBranchAdmissionPreservesExpectedOIDGuard(t *testing.T) {
	f := newBranchMutationFixture(t)
	runGitE2E(t, f.ctx, f.linked, "commit", "--allow-empty", "-m", "later commit")
	newer, err := (ExecRunner{}).Run(f.ctx, f.linked, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	moved := false
	runner := branchOutcomeRunner{run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 0 && args[0] == "update-ref" && !moved {
			moved = true
			if _, err := (ExecRunner{}).Run(ctx, cwd, "git", "update-ref", "refs/heads/feature", strings.TrimSpace(newer)); err != nil {
				return "", err
			}
		}
		return (ExecRunner{}).Run(ctx, cwd, name, args...)
	}}
	removed, err := RemoveManagedWorktree(f.ctx, runner, f.member(), integration.CleanupSafe)
	if !moved || err == nil || !strings.Contains(err.Error(), "worktree removed but local branch") || removed.Path != f.path {
		t.Fatal("moved branch was not an honest partial outcome", removed, err)
	}
	if current, err := (ExecRunner{}).Run(f.ctx, f.repo, "git", "rev-parse", "refs/heads/feature"); err != nil || current != newer {
		t.Fatal("moved branch was deleted or changed", err)
	}
	if _, err := os.Stat(f.path); !os.IsNotExist(err) {
		t.Fatal("worktree was not actually removed", err)
	}
	release, err := workspacelease.AcquireGitBranchMutation(filepath.Join(f.repo, ".git"), "feature")
	if err != nil {
		t.Fatal("partial outcome leaked admission", err)
	}
	release()
}
