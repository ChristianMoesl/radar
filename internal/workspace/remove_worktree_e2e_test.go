package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemoveWorktreePreservesBranchE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "init")
	runGitE2E(t, ctx, repo, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, repo, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, repo, "add", "README.md")
	runGitE2E(t, ctx, repo, "commit", "-m", "initial")

	worktree := filepath.Join(t.TempDir(), "feature")
	runGitE2E(t, ctx, repo, "worktree", "add", "-b", "feature", worktree)
	if _, err := RemoveWorktree(ctx, ExecRunner{}, worktree, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	runGitE2E(t, ctx, repo, "show-ref", "--verify", "refs/heads/feature")
}
