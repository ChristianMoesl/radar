package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestCleanupExpiredDiscardsUnpublishedDirtyWorktreeOffline(t *testing.T) {
	f := newCleanupExpiryFixture(t, "feature")
	f.commitLocal(t)
	f.dirty(t)
	runGit(t, f.ctx, f.repo, "remote", "set-url", "origin", filepath.Join(f.home, "unreachable.git"))

	// Cached observations are not deletion authority: pin the registry identity.
	ref := f.ref()
	ref.Repo, ref.Branch, ref.WorkspaceID = "/stale/repository", "stale-branch", "stale-workspace"
	runner := &cleanupExpiryRunner{Runner: workspace.ExecRunner{}}
	targets, err := previewCleanup(f.ctx, integration.CleanupPreviewRequest{
		Task: protocol.Task{SourceRefs: []protocol.SourceRef{ref}}, Mode: integration.CleanupExpired,
	}, runner)
	if err != nil || len(targets) != 1 {
		t.Fatalf("expired preview = %+v, %v", targets, err)
	}
	target := targets[0]
	if runner.remoteChecks != 0 {
		t.Fatalf("expired preview performed %d remote checks", runner.remoteChecks)
	}
	if target.WorkspaceID != f.group.ID || target.Branch != f.branch || target.Operation["repository"] != f.repo || target.Operation["delete_branch"] != f.branch {
		t.Fatalf("target does not pin the managed identity: %+v", target)
	}
	if !hasCleanupSafety(target, "local_changes") || hasCleanupSafety(target, "unpublished_data") || hasCleanupSafety(target, "safety_check_unavailable") {
		t.Fatalf("expired preview must report dirty data without remote verification: %+v", target.Safety)
	}

	// A real execution cannot verify publication against this missing remote.
	if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err != nil {
		t.Fatal(err)
	}
	f.assertRemoved(t)
	cmd := exec.CommandContext(f.ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+f.branch)
	cmd.Dir = f.repo
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("expired local branch still exists, or could not be inspected: %v", err)
	}
}

func TestCleanupSafeStillBlocksLocalData(t *testing.T) {
	for _, test := range []struct {
		name, safety string
		prepare      func(*testing.T, *cleanupExpiryFixture)
	}{
		{"dirty", "local_changes", func(t *testing.T, f *cleanupExpiryFixture) { f.dirty(t) }},
		{"unpublished", "unpublished_data", func(t *testing.T, f *cleanupExpiryFixture) { f.commitLocal(t) }},
		{"offline", "safety_check_unavailable", func(t *testing.T, f *cleanupExpiryFixture) {
			f.commitLocal(t)
			runGit(t, f.ctx, f.repo, "remote", "set-url", "origin", filepath.Join(f.home, "unreachable.git"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			test.prepare(t, f)
			runner := &cleanupExpiryRunner{Runner: workspace.ExecRunner{}}
			// Omitted Mode must remain safe in both preview and execution.
			targets, err := previewCleanup(f.ctx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{f.ref()}}}, runner)
			if err != nil || len(targets) != 1 || !hasCleanupSafety(targets[0], test.safety) {
				t.Fatalf("safe preview = %+v, %v; want %s", targets, err, test.safety)
			}
			if runner.remoteChecks == 0 {
				t.Fatal("safe preview skipped publication verification")
			}
			registry, head := f.snapshot(t)
			if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: targets[0]}); err == nil {
				t.Fatal("safe cleanup discarded local data")
			}
			f.assertPreserved(t, registry, head)
		})
	}
}

func TestCleanupExpiredPreservesProtectedAndSharedBranches(t *testing.T) {
	for _, branch := range []string{"main", "master", "shared"} {
		t.Run(branch, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, branch)
			f.commitLocal(t)
			other := filepath.Join(f.home, "other-checkout")
			if branch == "shared" {
				runGit(t, f.ctx, f.repo, "worktree", "add", "--force", other, branch)
			}
			f.dirty(t)
			runGit(t, f.ctx, f.repo, "remote", "set-url", "origin", filepath.Join(f.home, "unreachable.git"))
			target := f.preview(t, integration.CleanupExpired)
			if target.Operation["delete_branch"] != "" || hasCleanupSafety(target, "deletes_local_data") {
				t.Fatalf("protected/shared branch scheduled for deletion: %+v", target)
			}
			head := f.branchHead(t)
			if branch == "shared" {
				// Execution must keep the preview's promise even if sharing ends.
				runGit(t, f.ctx, f.repo, "worktree", "remove", other)
			}
			if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err != nil {
				t.Fatal(err)
			}
			f.assertRemoved(t)
			if got := f.branchHead(t); got != head {
				t.Fatalf("preserved branch moved: %s, want %s", got, head)
			}
		})
	}
}

func TestCleanupExpiredRejectsInvalidManagedWorktrees(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *cleanupExpiryFixture)
	}{
		{"wrong_repository", func(t *testing.T, f *cleanupExpiryFixture) {
			other := filepath.Join(f.home, "different-repository")
			initCleanupExpiryRepository(t, f.ctx, other, "main")
			f.group.Members[0].Repository = other
			f.save(t)
		}},
		{"actual_branch_changed", func(t *testing.T, f *cleanupExpiryFixture) {
			runGit(t, f.ctx, f.path, "switch", "-c", "different-branch")
		}},
		{"wrong_actual_path", func(t *testing.T, f *cleanupExpiryFixture) {
			physical := filepath.Join(f.root, "physical", filepath.Base(f.path))
			f.move(t, physical)
			if err := os.Mkdir(f.path, 0o755); err != nil {
				t.Fatal(err)
			}
			gitFile, err := os.ReadFile(filepath.Join(physical, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			// A regular .git file alone does not establish the checkout's path:
			// rev-parse reports this directory while Git owns the moved one.
			if err := os.WriteFile(filepath.Join(f.path, ".git"), gitFile, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink_member", func(t *testing.T, f *cleanupExpiryFixture) {
			physical := filepath.Join(f.root, "physical", filepath.Base(f.path))
			f.move(t, physical)
			if err := os.Symlink(physical, f.path); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink_parent", func(t *testing.T, f *cleanupExpiryFixture) {
			physical := filepath.Join(f.root, "physical", filepath.Base(f.path))
			f.move(t, physical)
			if err := os.Remove(f.group.Path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Dir(physical), f.group.Path); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink_git_file", func(t *testing.T, f *cleanupExpiryFixture) {
			gitFile := filepath.Join(f.path, ".git")
			physical := filepath.Join(f.home, "linked-git-file")
			if err := os.Rename(gitFile, physical); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(physical, gitFile); err != nil {
				t.Fatal(err)
			}
		}},
		{"locked", func(t *testing.T, f *cleanupExpiryFixture) {
			runGit(t, f.ctx, f.repo, "worktree", "lock", "--reason", "must not expire", f.path)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			target := f.preview(t, integration.CleanupExpired)
			test.mutate(t, f)
			// Match even the corrupt registration, so execution must check reality,
			// not merely reject because the preview's repository pin differs.
			target.Operation["repository"] = f.group.Members[0].Repository
			registry, head := f.snapshot(t)
			if targets, err := (Source{}).PreviewCleanup(f.ctx, integration.CleanupPreviewRequest{
				Task: protocol.Task{SourceRefs: []protocol.SourceRef{f.ref()}}, Mode: integration.CleanupExpired,
			}); err == nil {
				t.Errorf("expired preview accepted invalid checkout: %+v", targets)
			}
			if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err == nil {
				t.Error("expired execution accepted invalid checkout")
			}
			f.assertPreserved(t, registry, head)
		})
	}
}

func TestCleanupExpiredRejectsRegisteredPrimaryCheckouts(t *testing.T) {
	for _, separate := range []bool{false, true} {
		name := "normal"
		if separate {
			name = "separate_git_dir"
		}
		t.Run(name, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			target := f.preview(t, integration.CleanupExpired)
			runGit(t, f.ctx, f.repo, "worktree", "remove", f.path)
			var options []string
			if separate {
				options = []string{"--separate-git-dir=" + filepath.Join(f.home, "primary.git")}
			}
			initCleanupExpiryRepository(t, f.ctx, f.path, f.branch, options...)
			f.group.Members[0].Repository = f.path
			f.save(t)
			target.Operation["repository"] = f.path
			registry, head := f.snapshot(t)
			if targets, err := (Source{}).PreviewCleanup(f.ctx, integration.CleanupPreviewRequest{
				Task: protocol.Task{SourceRefs: []protocol.SourceRef{f.ref()}}, Mode: integration.CleanupExpired,
			}); err == nil {
				t.Errorf("expired preview accepted registered primary checkout: %+v", targets)
			}
			if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err == nil {
				t.Error("expired execution accepted registered primary checkout")
			}
			f.assertPreserved(t, registry, head)
			runGit(t, f.ctx, f.path, "show-ref", "--verify", "refs/heads/"+f.branch)
		})
	}
}

func TestCleanupExpiredRefusesChangedRegistration(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *cleanupExpiryFixture)
	}{
		{"repository", func(t *testing.T, f *cleanupExpiryFixture) {
			other := filepath.Join(f.home, "different-repository")
			initCleanupExpiryRepository(t, f.ctx, other, "main")
			runGit(t, f.ctx, f.repo, "worktree", "remove", f.path)
			runGit(t, f.ctx, other, "worktree", "add", "-b", f.branch, f.path)
			f.group.Members[0].Repository = other
		}},
		{"branch", func(t *testing.T, f *cleanupExpiryFixture) {
			runGit(t, f.ctx, f.path, "switch", "-c", "replacement")
			f.group.Members[0].Branch = "replacement"
		}},
		{"member_removed", func(t *testing.T, f *cleanupExpiryFixture) { f.group.Members = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCleanupExpiryFixture(t, "feature")
			target := f.preview(t, integration.CleanupExpired)
			test.mutate(t, f)
			f.save(t)
			registry, head := f.snapshot(t)
			if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err == nil {
				t.Fatal("expired cleanup accepted a stale registration")
			}
			f.assertPreserved(t, registry, head)
		})
	}
}

func TestCleanupExpiredRefusesObservedWorktree(t *testing.T) {
	f := newCleanupExpiryFixture(t, "feature")
	f.commitLocal(t)
	f.dirty(t)
	f.group.Members = nil
	f.save(t)
	target := f.preview(t, integration.CleanupSafe)
	if target.Operation["delete_branch"] != "" || target.WorkspaceID != "" {
		t.Fatalf("observed worktree received managed deletion authority: %+v", target)
	}
	registry, head := f.snapshot(t)
	if _, err := (Source{}).Cleanup(f.ctx, integration.CleanupRequest{Target: target, Mode: integration.CleanupExpired}); err == nil || !strings.Contains(err.Error(), "registered worktree") {
		t.Fatalf("observed expiry must refuse without registration: %v", err)
	}
	f.assertPreserved(t, registry, head)
}

type cleanupExpiryRunner struct {
	workspace.Runner
	remoteChecks int
}

func (r *cleanupExpiryRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "gh" || (name == "git" && len(args) > 0 && args[0] == "fetch") {
		r.remoteChecks++
	}
	return r.Runner.Run(ctx, cwd, name, args...)
}

type cleanupExpiryFixture struct {
	ctx                            context.Context
	home, root, repo, path, branch string
	group                          workspacegroup.Workspace
}

func newCleanupExpiryFixture(t *testing.T, branch string) *cleanupExpiryFixture {
	t.Helper()
	home := cleanPhysicalPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeGitTestConfig(t, home)
	root, err := workspace.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	f := &cleanupExpiryFixture{ctx: context.Background(), home: home, root: root, repo: filepath.Join(home, "repo"), branch: branch}
	initCleanupExpiryRepository(t, f.ctx, f.repo, "main")
	remote := filepath.Join(home, "origin.git")
	runGit(t, f.ctx, home, "init", "--bare", remote)
	runGit(t, f.ctx, f.repo, "remote", "add", "origin", remote)
	runGit(t, f.ctx, f.repo, "push", "origin", "main")
	anchor := filepath.Join(root, branch)
	f.path = filepath.Join(anchor, "repo--"+branch)
	if branch == "main" {
		runGit(t, f.ctx, f.repo, "switch", "--detach")
		runGit(t, f.ctx, f.repo, "worktree", "add", f.path, branch)
	} else {
		runGit(t, f.ctx, f.repo, "worktree", "add", "-b", branch, f.path)
	}
	f.group = workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: branch, Path: anchor,
		Members: []workspacegroup.Member{{Repository: f.repo, Path: f.path, Branch: branch}},
	}
	f.save(t)
	return f
}

func initCleanupExpiryRepository(t *testing.T, ctx context.Context, path, branch string, options ...string) {
	t.Helper()
	args := append([]string{"init", "-b", branch}, options...)
	runGit(t, ctx, filepath.Dir(path), append(args, path)...)
	runGit(t, ctx, path, "config", "user.email", "radar@example.test")
	runGit(t, ctx, path, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, path, "add", ".")
	runGit(t, ctx, path, "commit", "-m", "initial")
}

func (f *cleanupExpiryFixture) ref() protocol.SourceRef {
	return protocol.SourceRef{ID: "git:worktree:" + f.path, Source: "git", Kind: "worktree", Repo: f.repo, Path: f.path, Branch: f.branch}
}

func (f *cleanupExpiryFixture) save(t *testing.T) {
	t.Helper()
	if err := workspacegroup.Save(f.root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{f.group}}); err != nil {
		t.Fatal(err)
	}
}

func (f *cleanupExpiryFixture) preview(t *testing.T, mode integration.CleanupMode) protocol.CleanupTarget {
	t.Helper()
	targets, err := (Source{}).PreviewCleanup(f.ctx, integration.CleanupPreviewRequest{Task: protocol.Task{SourceRefs: []protocol.SourceRef{f.ref()}}, Mode: mode})
	if err != nil || len(targets) != 1 {
		t.Fatalf("preview = %+v, %v", targets, err)
	}
	return targets[0]
}

func (f *cleanupExpiryFixture) commitLocal(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.path, "local.txt"), []byte("unpublished\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.ctx, f.path, "add", "local.txt")
	runGit(t, f.ctx, f.path, "commit", "-m", "local only")
}

func (f *cleanupExpiryFixture) dirty(t *testing.T) {
	t.Helper()
	for name, content := range map[string]string{"README.md": "modified\n", "scratch.txt": "untracked\n"} {
		if err := os.WriteFile(filepath.Join(f.path, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *cleanupExpiryFixture) move(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.ctx, f.repo, "worktree", "move", f.path, path)
}

func (f *cleanupExpiryFixture) branchHead(t *testing.T) string {
	t.Helper()
	head, err := gitOutput(f.ctx, f.repo, "rev-parse", "--verify", "refs/heads/"+f.branch)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(head)
}

func (f *cleanupExpiryFixture) snapshot(t *testing.T) ([]byte, string) {
	t.Helper()
	data, err := os.ReadFile(workspacegroup.Path(f.root))
	if err != nil {
		t.Fatal(err)
	}
	return data, f.branchHead(t)
}

func (f *cleanupExpiryFixture) assertPreserved(t *testing.T, registry []byte, head string) {
	t.Helper()
	if _, err := os.Stat(f.path); err != nil {
		t.Errorf("refused cleanup removed the checkout: %v", err)
	}
	data, gotHead := f.snapshot(t)
	if string(data) != string(registry) || gotHead != head {
		t.Errorf("refused cleanup changed the registry or branch: head=%s, want %s; registry=%s", gotHead, head, data)
	}
}

func (f *cleanupExpiryFixture) assertRemoved(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(f.path); !os.IsNotExist(err) {
		t.Fatalf("expired worktree still exists: %v", err)
	}
	registry, err := workspacegroup.Load(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := workspacegroup.FindMemberByPath(registry, f.path); exists {
		t.Fatal("removed member remains registered")
	}
	output, err := gitOutput(f.ctx, f.repo, "worktree", "list", "--porcelain")
	if err != nil || strings.Contains(output, "worktree "+f.path+"\n") {
		t.Fatalf("expired worktree remains in Git: %s, %v", output, err)
	}
}
