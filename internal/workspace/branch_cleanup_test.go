package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

	published, err := BranchPublished(ctx, ExecRunner{}, repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if published {
		t.Fatal("unpublished branch reported as published")
	}

	runGitE2E(t, ctx, repo, "push", "-u", "origin", "feature")
	published, err = BranchPublished(ctx, ExecRunner{}, repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("pushed branch reported as unpublished")
	}
}
