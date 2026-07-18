package git

import (
	"context"
	"io"
	"log/slog"
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

func TestPreviewCleanupRejectsMainWorkingTree(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	runGit(t, ctx, filepath.Dir(repo), "init", "repo")

	_, err := Source{}.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{ID: 1, SourceRefs: []protocol.SourceRef{{Source: "git", Kind: "worktree", Path: repo}}}})
	if err == nil {
		t.Fatal("PreviewCleanup() error = nil, want main working tree error")
	}
}

func TestPreviewCleanupReturnsWorktreeTarget(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	worktree := filepath.Join(home, "worktrees", "small-fix")
	runGit(t, ctx, home, "init", "repo")
	runGit(t, ctx, repo, "config", "user.email", "radar@example.com")
	runGit(t, ctx, repo, "config", "user.name", "Radar")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repo, "add", "README.md")
	runGit(t, ctx, repo, "commit", "-m", "init")
	runGit(t, ctx, repo, "worktree", "add", "-b", "small-fix", worktree)

	targets, err := Source{}.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{ID: 1, SourceRefs: []protocol.SourceRef{
		{ID: "git:worktree:" + worktree, Source: "git", Kind: "worktree", Path: worktree, Branch: "small-fix", Title: "small-fix"},
		{ID: "tmux:session:$1", Source: "tmux", Kind: "session", Title: "repo-small-fix"},
		{ID: "sbx:sandbox:radar-repo-small-fix", Source: "sbx", Kind: "sandbox", Title: "radar-repo-small-fix", Path: worktree},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("cleanup targets = %+v, want one worktree", targets)
	}
	if targets[0].Source != "git" || targets[0].Kind != "worktree" || targets[0].Path != worktree {
		t.Fatalf("cleanup target = %+v", targets[0])
	}
}

func TestGitRootsDefaultsToCwdAndWorkspaces(t *testing.T) {
	home := t.TempDir()
	dataHome := filepath.Join(home, "data")
	workspaceRoot := filepath.Join(dataHome, "radar", "workspaces")
	cwd := filepath.Join(home, "not-a-workspace")
	workspace := filepath.Join(workspaceRoot, "repo", "feature")
	otherWorkspace := filepath.Join(workspaceRoot, "other", "fix")
	for _, dir := range []string{cwd, workspace, otherWorkspace, filepath.Join(workspaceRoot, "repo")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("RADAR_GIT_REPOS", "")
	t.Chdir(cwd)

	roots := gitRoots()
	assertContainsRoot(t, roots, cwd)
	assertContainsRoot(t, roots, workspace)
	assertContainsRoot(t, roots, otherWorkspace)
	assertMissingRoot(t, roots, filepath.Join(workspaceRoot, "repo"))
}

func TestPathInRepositoryDirs(t *testing.T) {
	home := t.TempDir()
	repositoryDir := filepath.Join(home, "repos")
	inside := filepath.Join(repositoryDir, "radar")
	outside := filepath.Join(home, "workspaces", "radar", "feature")
	for _, path := range []string{inside, outside} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if !pathInRepositoryDirs(inside, []string{repositoryDir}) {
		t.Fatalf("pathInRepositoryDirs(%q) = false, want true", inside)
	}
	if pathInRepositoryDirs(outside, []string{repositoryDir}) {
		t.Fatalf("pathInRepositoryDirs(%q) = true, want false", outside)
	}
}

func TestFetchWorktreesExcludesConfiguredRepositoryDirs(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	repositoryDir := filepath.Join(home, "repos")
	repo := filepath.Join(repositoryDir, "radar")
	linkedWorktree := filepath.Join(home, "workspaces", "radar", "feature")
	if err := os.MkdirAll(repositoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repositoryDir, "init", "radar")
	runGit(t, ctx, repo, "config", "user.email", "radar@example.com")
	runGit(t, ctx, repo, "config", "user.name", "Radar")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repo, "add", "README.md")
	runGit(t, ctx, repo, "commit", "-m", "init")
	runGit(t, ctx, repo, "worktree", "add", "-b", "feature/RAD-123", linkedWorktree)

	configHome := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := []byte(`{"repository_dirs":["` + repositoryDir + `"]}`)
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("RADAR_GIT_REPOS", repo)

	refs, status := FetchWorktrees(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if status.Status != "ok" {
		t.Fatalf("FetchWorktrees() status = %+v", status)
	}
	if len(refs) != 1 || cleanPhysicalPath(refs[0].Path) != cleanPhysicalPath(linkedWorktree) {
		t.Fatalf("FetchWorktrees() refs = %+v, want only %q", refs, linkedWorktree)
	}
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
