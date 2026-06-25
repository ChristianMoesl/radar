package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/contracttest"
	"radar/internal/protocol"
)

func TestWorktreesSkipsPrunableEntries(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	stale := filepath.Join(home, "stale")

	runGit(t, ctx, home, "init", "repo")
	runGit(t, ctx, repo, "config", "user.email", "radar@example.com")
	runGit(t, ctx, repo, "config", "user.name", "Radar")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repo, "add", "README.md")
	runGit(t, ctx, repo, "commit", "-m", "init")
	runGit(t, ctx, repo, "worktree", "add", "-b", "feature/RAD-123-stale", stale)
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}

	items, err := worktrees(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Path == stale {
			t.Fatalf("worktrees() included prunable worktree: %#v", items)
		}
	}
}

func TestWorktreeSourceRefContract(t *testing.T) {
	ref := worktree{Path: "/work/repo/RAD-123-fix", Branch: "RAD-123-fix", Head: "abc"}.SourceRef(context.Background())
	contracttest.AssertValidSourceRefs(t, "git", []protocol.SourceRef{ref})
}

func TestPreviewDeleteRejectsMainWorkingTree(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	runGit(t, ctx, filepath.Dir(repo), "init", "repo")

	_, ok, err := Source{}.PreviewDelete(ctx, integration.DeletePreviewRequest{Task: protocol.Task{ID: 1, SourceRefs: []protocol.SourceRef{{Source: "git", Kind: "worktree", Path: repo}}}})
	if err == nil {
		t.Fatal("PreviewDelete() error = nil, want main working tree error")
	}
	if !ok {
		t.Fatal("PreviewDelete() ok = false, want true for rejected git target")
	}
}

func TestGitRootsDefaultsToCwdAndWorkspaces(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "not-a-workspace")
	workspace := filepath.Join(home, "workspaces", "repo", "feature")
	otherWorkspace := filepath.Join(home, "workspaces", "other", "fix")
	for _, dir := range []string{cwd, workspace, otherWorkspace, filepath.Join(home, "workspaces", "repo")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_GIT_REPOS", "")
	t.Chdir(cwd)

	roots := gitRoots()
	assertContainsRoot(t, roots, cwd)
	assertContainsRoot(t, roots, workspace)
	assertContainsRoot(t, roots, otherWorkspace)
	assertMissingRoot(t, roots, filepath.Join(home, "workspaces", "repo"))
}

func TestGitRootsIncludesTmuxSessionPaths(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "cwd")
	sessionPath := filepath.Join(home, "work", "repo")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nprintf '%s\\n' '"+sessionPath+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("PATH", bin)
	t.Setenv("RADAR_GIT_REPOS", "")
	t.Chdir(cwd)

	roots := gitRoots()
	assertContainsRoot(t, roots, cwd)
	assertContainsRoot(t, roots, sessionPath)
}

func TestGitRootsEnvOverridesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_GIT_REPOS", "~/repo:/tmp/other")

	roots := gitRoots()
	want := []string{filepath.Join(home, "repo"), "/tmp/other"}
	if len(roots) != len(want) {
		t.Fatalf("gitRoots() = %#v, want %#v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("gitRoots() = %#v, want %#v", roots, want)
		}
	}
}

func runGit(t *testing.T, ctx context.Context, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func assertContainsRoot(t *testing.T, roots []string, want string) {
	t.Helper()
	for _, root := range roots {
		if root == want {
			return
		}
	}
	t.Fatalf("gitRoots() = %#v, missing %q", roots, want)
}

func assertMissingRoot(t *testing.T, roots []string, want string) {
	t.Helper()
	for _, root := range roots {
		if root == want {
			t.Fatalf("gitRoots() = %#v, unexpectedly contained %q", roots, want)
		}
	}
}
