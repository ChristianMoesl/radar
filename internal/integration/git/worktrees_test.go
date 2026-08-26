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
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/linking"
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
	runGit(t, ctx, repo, "worktree", "add", "-b", "feature/XYZ-123-stale", stale)
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

func TestRegisteredWorktreeSourceRefIncludesWorkspaceLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo", "work")
	ref := (worktree{Path: path, Branch: "feature", Head: "abc"}).SourceRef(context.Background(), linking.MarkMatcher{}, workspaceGroupLink{
		ID: "workspace-id", TaskLinkingKey: "obsidian:task:one",
	})
	if ref.ProvidesWorkspace {
		t.Fatalf("registered member should defer workspace capability to its anchor: %+v", ref)
	}
	if ref.Metadata["workspace_id"] != "workspace-id" {
		t.Fatalf("metadata = %+v", ref.Metadata)
	}
	found := map[string]bool{}
	for _, key := range ref.LinkingKeys {
		found[key] = true
	}
	for _, want := range []string{"workspace-group:workspace-id", "obsidian:task:one"} {
		if !found[want] {
			t.Fatalf("linking keys = %+v, want %q", ref.LinkingKeys, want)
		}
	}
}

func TestWorktreeSourceRefContract(t *testing.T) {
	ref := worktree{Path: "/work/repo/XYZ-123-fix", Branch: "XYZ-123-fix", Head: "abc"}.SourceRef(context.Background(), linking.NewMarkMatcher([]string{"XYZ"}))
	contracttest.AssertValidSourceRefs(t, "git", []protocol.SourceRef{ref})
	if !ref.ProvidesWorkspace {
		t.Fatalf("git worktree does not provide workspace: %+v", ref)
	}
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
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeGitTestConfig(t, home)
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
	if targets[0].Source != "git" || targets[0].Kind != "worktree" || targets[0].Path != worktree || targets[0].Operation["delete_branch"] != "" {
		t.Fatalf("cleanup target = %+v, want observed worktree without branch deletion", targets[0])
	}
}

func TestManagedWorktreeCleanupDeletesItsLocalBranch(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeGitTestConfig(t, home)
	root, err := workspace.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	anchor := filepath.Join(root, "small-fix")
	worktreePath := filepath.Join(anchor, "repo--small-fix")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, home, "init", "repo")
	runGit(t, ctx, repo, "config", "user.email", "radar@example.com")
	runGit(t, ctx, repo, "config", "user.name", "Radar")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repo, "add", "README.md")
	runGit(t, ctx, repo, "commit", "-m", "init")
	runGit(t, ctx, repo, "worktree", "add", "-b", "small-fix", worktreePath)
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "small-fix", Path: anchor,
		Members: []workspacegroup.Member{{Repository: repo, Path: worktreePath, Branch: "small-fix"}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	ref := protocol.SourceRef{ID: "git:worktree:" + worktreePath, Source: "git", Kind: "worktree", Path: worktreePath, Branch: "small-fix", Title: "small-fix"}
	targets, err := Source{}.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{ID: 1, SourceRefs: []protocol.SourceRef{ref}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Operation["delete_branch"] != "small-fix" || !hasCleanupSafety(targets[0], "safety_check_unavailable") {
		t.Fatalf("cleanup target = %+v, want managed branch with unknown publication", targets)
	}
	if _, err := (Source{}).Cleanup(ctx, integration.CleanupRequest{Target: targets[0]}); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/small-fix")
	command.Dir = repo
	if err := command.Run(); err == nil {
		t.Fatal("managed branch still exists")
	}
}

func TestManagedWorktreeCleanupPreservesProtectedBranch(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeGitTestConfig(t, home)
	root, err := workspace.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	anchor := filepath.Join(root, "main")
	worktreePath := filepath.Join(anchor, "repo--main")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, home, "init", "repo")
	runGit(t, ctx, repo, "config", "user.email", "radar@example.com")
	runGit(t, ctx, repo, "config", "user.name", "Radar")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ctx, repo, "add", "README.md")
	runGit(t, ctx, repo, "commit", "-m", "init")
	runGit(t, ctx, repo, "branch", "-M", "main")
	runGit(t, ctx, repo, "switch", "--detach")
	runGit(t, ctx, repo, "worktree", "add", worktreePath, "main")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(anchor), Name: "main", Path: anchor,
		Members: []workspacegroup.Member{{Repository: repo, Path: worktreePath, Branch: "main"}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	ref := protocol.SourceRef{ID: "git:worktree:" + worktreePath, Source: "git", Kind: "worktree", Path: worktreePath, Branch: "main", Title: "main"}
	targets, err := Source{}.PreviewCleanup(ctx, integration.CleanupPreviewRequest{Task: protocol.Task{ID: 1, SourceRefs: []protocol.SourceRef{ref}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Operation["delete_branch"] != "" || len(targets[0].Safety) != 0 {
		t.Fatalf("cleanup target = %+v, want protected branch preservation", targets)
	}
	if _, err := (Source{}).Cleanup(ctx, integration.CleanupRequest{Target: targets[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	command := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/main")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatalf("protected branch was deleted: %v", err)
	}
}

func TestGitRootsOnlyIncludesConfiguredWorkspaces(t *testing.T) {
	home := t.TempDir()
	dataHome := filepath.Join(home, "data")
	workspaceRoot := filepath.Join(dataHome, "radar", "workspaces")
	cwd := filepath.Join(home, "not-a-workspace")
	workspace := filepath.Join(workspaceRoot, "repo--feature")
	otherWorkspace := filepath.Join(workspaceRoot, "other--fix")
	for _, dir := range []string{cwd, workspace, otherWorkspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{workspace, otherWorkspace} {
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /tmp/test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", dataHome)
	writeGitTestConfig(t, home)
	t.Chdir(cwd)

	roots := gitRoots()
	assertContainsRoot(t, roots, workspace)
	assertContainsRoot(t, roots, otherWorkspace)
	assertMissingRoot(t, roots, cwd)
}

func TestFetchWorktreesReturnsCompleteEmptyResultWithoutWorkspaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	writeGitTestConfig(t, home)

	refs, status := FetchWorktrees(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), linking.NewMarkMatcher([]string{"XYZ"}))
	if status.Status != "ok" || len(refs) != 0 {
		t.Fatalf("FetchWorktrees() refs=%+v status=%+v, want complete empty result", refs, status)
	}
}

func TestPathInWorkspaceRootAcceptsNestedMembers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	inside := filepath.Join(root, "radar--feature")
	nested := filepath.Join(inside, "nested")
	for _, path := range []string{inside, nested} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if !pathInWorkspaceRoot(inside, root) {
		t.Fatalf("pathInWorkspaceRoot(%q, %q) = false, want true", inside, root)
	}
	if !pathInWorkspaceRoot(nested, root) {
		t.Fatalf("pathInWorkspaceRoot(%q, %q) = false, want true", nested, root)
	}
	for _, outside := range []string{filepath.Join(filepath.Dir(root), "repo")} {
		if pathInWorkspaceRoot(outside, root) {
			t.Fatalf("pathInWorkspaceRoot(%q, %q) = true, want false", outside, root)
		}
	}
}

func TestFetchWorktreesOnlyIncludesConfiguredWorkspaceRoot(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	repositoryDir := filepath.Join(home, "repos")
	repo := filepath.Join(repositoryDir, "radar")
	linkedWorktree := filepath.Join(home, "workspaces", "radar--feature")
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
	runGit(t, ctx, repo, "worktree", "add", "-b", "feature/XYZ-123", linkedWorktree)

	configHome := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := []byte(`{"linking_mark_prefixes":["XYZ"],"workspace":{"root_dir":"` + filepath.Join(home, "workspaces") + `"}}`)
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	refs, status := FetchWorktrees(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), linking.NewMarkMatcher([]string{"XYZ"}))
	if status.Status != "ok" {
		t.Fatalf("FetchWorktrees() status = %+v", status)
	}
	if len(refs) != 1 || cleanPhysicalPath(refs[0].Path) != cleanPhysicalPath(linkedWorktree) {
		t.Fatalf("FetchWorktrees() refs = %+v, want only %q", refs, linkedWorktree)
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

func hasCleanupSafety(target protocol.CleanupTarget, kind string) bool {
	for _, safety := range target.Safety {
		if safety.Kind == kind {
			return true
		}
	}
	return false
}

func writeGitTestConfig(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, "config", "radar", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"linking_mark_prefixes":["XYZ"]}`), 0o600); err != nil {
		t.Fatal(err)
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
