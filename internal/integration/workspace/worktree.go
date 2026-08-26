package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration"
)

type WorktreeOptions struct {
	Repo          string
	BranchMode    integration.WorkspaceBranchMode
	Name          string
	Branch        string
	Base          string
	Path          string
	WorkspaceRoot string
	AllowExisting bool
}

type WorktreePlan struct {
	Repo       string
	RepoConfig RepoConfig
	Name       string
	BranchMode integration.WorkspaceBranchMode
	Branch     string
	Base       string
	Path       string
	Root       string
	Existing   bool
}

type WorktreeResult struct {
	Plan    WorktreePlan
	Created bool
}

// PlanWorktree validates the same repository, branch, and destination rules used
// by workspace creation without changing Git or the filesystem.
func PlanWorktree(ctx context.Context, runner Runner, options WorktreeOptions) (WorktreePlan, error) {
	if err := runner.LookPath("git"); err != nil {
		return WorktreePlan{}, fmt.Errorf("worktree creation requires %q: %w", "git", err)
	}
	if options.BranchMode != integration.WorkspaceBranchExisting && options.BranchMode != integration.WorkspaceBranchNew {
		return WorktreePlan{}, fmt.Errorf("workspace branch mode must be existing or new")
	}
	repo, err := runner.Run(ctx, options.Repo, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return WorktreePlan{}, err
	}
	repo, err = filepath.Abs(strings.TrimSpace(repo))
	if err != nil {
		return WorktreePlan{}, err
	}
	repo = filepath.Clean(repo)
	repoConfig, err := loadRepoConfig(repo)
	if err != nil {
		return WorktreePlan{}, err
	}
	name := strings.TrimSpace(options.Name)
	branch := strings.TrimSpace(options.Branch)
	switch options.BranchMode {
	case integration.WorkspaceBranchExisting:
		branch = normalizeExistingBranch(branch)
		if branch == "" {
			return WorktreePlan{}, fmt.Errorf("existing branch is required")
		}
		if name == "" {
			name = branch
		}
	case integration.WorkspaceBranchNew:
		if name == "" {
			return WorktreePlan{}, fmt.Errorf("new branch name is required")
		}
		if branch == "" {
			branch = BranchName(name)
		}
		if strings.TrimSpace(options.Base) == "" {
			return WorktreePlan{}, fmt.Errorf("new branch base is required")
		}
	}
	root := strings.TrimSpace(options.WorkspaceRoot)
	if root == "" {
		root, err = DefaultRoot()
		if err != nil {
			return WorktreePlan{}, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return WorktreePlan{}, err
	}
	root = filepath.Clean(root)
	path := strings.TrimSpace(options.Path)
	if path == "" {
		path = filepath.Join(root, WorktreeDirectoryName(repo, name))
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return WorktreePlan{}, err
	}
	path = filepath.Clean(path)
	plan := WorktreePlan{Repo: repo, RepoConfig: repoConfig, Name: name, BranchMode: options.BranchMode, Branch: branch, Base: strings.TrimSpace(options.Base), Path: path, Root: root}

	if existingPath, ok, err := worktreePathForBranch(ctx, runner, repo, branch); err != nil {
		return WorktreePlan{}, err
	} else if ok {
		existingPath = filepath.Clean(existingPath)
		if options.BranchMode == integration.WorkspaceBranchNew && !(options.AllowExisting && existingPath == path) {
			return WorktreePlan{}, fmt.Errorf("branch %q already exists; choose the existing branch instead", branch)
		}
		if !isWorkspacePath(existingPath, root) {
			return WorktreePlan{}, fmt.Errorf("branch %q is checked out at %s; detach that checkout before creating a Radar workspace", branch, existingPath)
		}
		if options.Path != "" && existingPath != path {
			return WorktreePlan{}, fmt.Errorf("branch %q is already checked out at %s, not requested destination %s", branch, existingPath, path)
		}
		plan.Path = existingPath
		plan.Existing = true
		return plan, nil
	}
	if _, err := os.Stat(path); err == nil {
		return WorktreePlan{}, fmt.Errorf("workspace destination is occupied by unrelated content: %s", path)
	} else if !os.IsNotExist(err) {
		return WorktreePlan{}, err
	}
	if options.BranchMode == integration.WorkspaceBranchExisting {
		if !refExists(ctx, runner, repo, "refs/heads/"+branch) && !refExists(ctx, runner, repo, "refs/remotes/origin/"+branch) {
			return WorktreePlan{}, fmt.Errorf("branch %q does not exist locally or on origin", branch)
		}
	} else if refExists(ctx, runner, repo, "refs/heads/"+branch) || refExists(ctx, runner, repo, "refs/remotes/origin/"+branch) {
		return WorktreePlan{}, fmt.Errorf("branch %q already exists; choose the existing branch instead", branch)
	} else if _, err := runner.Run(ctx, repo, "git", "rev-parse", "--verify", "--quiet", strings.TrimSpace(options.Base)+"^{commit}"); err != nil {
		return WorktreePlan{}, fmt.Errorf("new branch base %q does not exist", strings.TrimSpace(options.Base))
	}
	return plan, nil
}

// EnsureWorktree applies a validated plan and recognizes an already-correct
// worktree, making retries safe.
func EnsureWorktree(ctx context.Context, runner Runner, plan WorktreePlan) (WorktreeResult, error) {
	revalidated, err := PlanWorktree(ctx, runner, WorktreeOptions{
		Repo: plan.Repo, BranchMode: plan.BranchMode, Name: plan.Name, Branch: plan.Branch,
		Base: plan.Base, Path: plan.Path, WorkspaceRoot: plan.Root, AllowExisting: true,
	})
	if err != nil {
		return WorktreeResult{}, err
	}
	if revalidated.Existing {
		if err := copyConfiguredFiles(revalidated.Repo, revalidated.Path, revalidated.RepoConfig.CopyFiles); err != nil {
			return WorktreeResult{}, err
		}
		return WorktreeResult{Plan: revalidated}, nil
	}
	if err := os.MkdirAll(filepath.Dir(revalidated.Path), 0o755); err != nil {
		return WorktreeResult{}, err
	}
	args := []string{"worktree", "add"}
	switch revalidated.BranchMode {
	case integration.WorkspaceBranchExisting:
		if refExists(ctx, runner, revalidated.Repo, "refs/heads/"+revalidated.Branch) {
			args = append(args, revalidated.Path, revalidated.Branch)
		} else {
			args = append(args, "--track", "-b", revalidated.Branch, revalidated.Path, "origin/"+revalidated.Branch)
		}
	case integration.WorkspaceBranchNew:
		args = append(args, "-b", revalidated.Branch, revalidated.Path, revalidated.Base)
	}
	if _, err := runner.Run(ctx, revalidated.Repo, "git", args...); err != nil {
		return WorktreeResult{}, err
	}
	if err := copyConfiguredFiles(revalidated.Repo, revalidated.Path, revalidated.RepoConfig.CopyFiles); err != nil {
		_, _ = runner.Run(ctx, revalidated.Repo, "git", "worktree", "remove", "--force", revalidated.Path)
		return WorktreeResult{}, err
	}
	return WorktreeResult{Plan: revalidated, Created: true}, nil
}
