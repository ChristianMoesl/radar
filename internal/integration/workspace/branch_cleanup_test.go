package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"radar/internal/integration/workspace/group"
	"strings"
	"testing"
)

func TestBranchPublishedRequiresBranchTipOnRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "repo")
	runGitE2E(t, ctx, root, "init", "--bare", remote)
	runGitE2E(t, ctx, root, "clone", remote, repo)
	runGitE2E(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, repo, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "add", "README.md")
	runGitE2E(t, ctx, repo, "commit", "-m", "initial")
	runGitE2E(t, ctx, repo, "push", "-u", "origin", "HEAD:main")
	runGitE2E(t, ctx, repo, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "add", "feature.txt")
	runGitE2E(t, ctx, repo, "commit", "-m", "feature")

	published, err := BranchPublishedOrMerged(ctx, ExecRunner{}, repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if published {
		t.Fatal("unpublished branch reported as published")
	}

	runGitE2E(t, ctx, repo, "push", "-u", "origin", "feature")
	published, err = BranchPublishedOrMerged(ctx, ExecRunner{}, repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("pushed branch reported as unpublished")
	}
}

func TestSharedBranchIsKeptWhenRemovingOneWorktree(t *testing.T) {
	ctx := context.Background()
	root := physicalPath(t.TempDir())
	repo := filepath.Join(root, "repo")
	runGitE2E(t, ctx, root, "init", repo)
	runGitE2E(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, repo, "config", "user.name", "Radar Test")
	runGitE2E(t, ctx, repo, "commit", "--allow-empty", "-m", "initial")
	one, two := filepath.Join(root, "one"), filepath.Join(root, "two")
	runGitE2E(t, ctx, repo, "worktree", "add", "-b", "feature", one)
	runGitE2E(t, ctx, repo, "worktree", "add", "--force", two, "feature")
	member := workspacegroup.Member{Repository: repo, Path: one, Branch: "feature"}
	plan, err := PlanManagedWorktreeRemoval(ctx, ExecRunner{}, member)
	if err != nil || plan.DeleteBranch {
		t.Fatalf("shared branch should be kept: %+v %v", plan, err)
	}
	if _, err := RemoveManagedWorktree(ctx, ExecRunner{}, member, false); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "show-ref", "--verify", "refs/heads/feature")
	if _, err := os.Stat(two); err != nil {
		t.Fatalf("other worktree removed: %v", err)
	}
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Fatalf("selected worktree not removed: %v", err)
	}
}

type localMergeProofRunner struct {
	ExecRunner
	proof string
}

func (r localMergeProofRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, error) {
	if name == "gh" {
		return r.proof, nil
	}
	if name == "git" && strings.Join(args, " ") == "remote get-url origin" {
		return "https://github.com/acme/app.git", nil
	}
	return r.ExecRunner.Run(ctx, cwd, name, args...)
}

func TestSquashMergedDeletedBranchIsSafeButLaterLocalCommitIsNot(t *testing.T) {
	ctx := context.Background()
	root := physicalPath(t.TempDir())
	repo, remote := filepath.Join(root, "repo"), filepath.Join(root, "remote.git")
	runGitE2E(t, ctx, root, "init", "--bare", remote)
	runGitE2E(t, ctx, root, "clone", remote, repo)
	runGitE2E(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, repo, "config", "user.name", "Radar Test")
	runGitE2E(t, ctx, repo, "switch", "-c", "main")
	runGitE2E(t, ctx, repo, "commit", "--allow-empty", "-m", "initial")
	runGitE2E(t, ctx, repo, "push", "origin", "main")
	runGitE2E(t, ctx, repo, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "add", "feature")
	runGitE2E(t, ctx, repo, "commit", "-m", "feature")
	runGitE2E(t, ctx, repo, "push", "origin", "feature")
	head, err := (ExecRunner{}).Run(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "switch", "main")
	runGitE2E(t, ctx, repo, "merge", "--squash", "feature")
	runGitE2E(t, ctx, repo, "commit", "-m", "squashed feature")
	merge, err := (ExecRunner{}).Run(ctx, repo, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "push", "origin", "main", ":feature")
	proof := fmt.Sprintf(`[{"merged_at":"2026-01-01","merge_commit_sha":%q,"head":{"sha":%q},"base":{"repo":{"full_name":"acme/app"}}}]`, merge, head)
	runner := localMergeProofRunner{proof: proof}
	got, err := BranchPublishedOrMerged(ctx, runner, repo, "feature")
	if err != nil || !got {
		t.Fatalf("squash merge not recognised: %v %v", got, err)
	}
	runGitE2E(t, ctx, repo, "switch", "feature")
	runGitE2E(t, ctx, repo, "commit", "--allow-empty", "-m", "local work after merge")
	got, err = BranchPublishedOrMerged(ctx, runner, repo, "feature")
	if err != nil || got {
		t.Fatalf("new unpublished commit accepted: %v %v", got, err)
	}
}
