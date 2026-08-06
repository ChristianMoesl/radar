package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	"radar/internal/workspacegroup"
)

func TestAddWorktreePreviewApplyAndRetryE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "workspaces")
	primaryRepo := filepath.Join(tmp, "primary-source")
	targetRepo := filepath.Join(tmp, "target-source")
	initRepository(t, ctx, primaryRepo)
	initRepository(t, ctx, targetRepo)
	runGitE2E(t, ctx, targetRepo, "branch", "feature/add-api")

	primaryPath := filepath.Join(root, "primary", "RAD-7-work")
	runGitE2E(t, ctx, primaryRepo, "worktree", "add", "-b", "RAD-7-work", primaryPath, "HEAD")
	group := workspacegroup.Workspace{
		ID: workspacegroup.ID(primaryPath), Name: "RAD-7-work", PrimaryPath: primaryPath, SessionName: "primary-RAD-7-work",
		Members: []workspacegroup.Member{{Repository: primaryRepo, Path: primaryPath, Branch: "RAD-7-work", Primary: true, SetupScheduled: true}},
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}

	request := AddWorktreeRequest{Workspace: primaryPath, Repository: targetRepo, BranchMode: integration.WorkspaceBranchExisting, Branch: "feature/add-api", WorkspaceRoot: root}
	plan, err := PreviewAddWorktree(ctx, ExecRunner{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.WorkspaceID != group.ID || plan.Branch != "feature/add-api" || plan.Path == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := os.Stat(plan.Path); !os.IsNotExist(err) {
		t.Fatalf("preview mutated destination: %v", err)
	}

	result, err := ApplyAddWorktree(ctx, ExecRunner{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !result.WorktreeCreated || !result.WorkspaceMembershipSaved || !result.SandboxReconciled {
		t.Fatalf("result = %+v", result)
	}
	if branch := gitOutputE2E(t, ctx, plan.Path, "branch", "--show-current"); branch != "feature/add-api\n" {
		t.Fatalf("branch = %q", branch)
	}

	retry, err := ApplyAddWorktree(ctx, ExecRunner{}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.OK || retry.Path != result.Path {
		t.Fatalf("retry = %+v", retry)
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := workspacegroup.FindByMemberPath(registry, result.Path)
	if !ok || len(loaded.Members) != 2 {
		t.Fatalf("registry = %+v", registry)
	}
}

func initRepository(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, path, "init")
	runGitE2E(t, ctx, path, "config", "user.email", "radar@example.test")
	runGitE2E(t, ctx, path, "config", "user.name", "Radar Test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, ctx, path, "add", "README.md")
	runGitE2E(t, ctx, path, "commit", "-m", "initial")
}
