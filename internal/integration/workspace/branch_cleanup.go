package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"radar/internal/integration/workspace/group"
)

// BranchPublishedOrMerged verifies publication or an exact merged GitHub PR head.
// A branch name or task completion status alone is never proof of publication.
func BranchPublishedOrMerged(ctx context.Context, runner Runner, repository, branch string) (bool, error) {
	repository = strings.TrimSpace(repository)
	branch = strings.TrimSpace(branch)
	if repository == "" || branch == "" {
		return false, fmt.Errorf("repository and branch are required")
	}
	runner = verificationRetryRunner{Runner: runner}
	if err := FetchBranches(ctx, runner, repository); err != nil {
		return false, err
	}
	ref := "refs/heads/" + branch
	if _, err := runner.Run(ctx, repository, "git", "show-ref", "--verify", "--quiet", ref); err != nil {
		return false, fmt.Errorf("local branch %q is unavailable: %w", branch, err)
	}
	output, err := runner.Run(ctx, repository, "git", "for-each-ref", "--format=%(refname)", "--contains="+ref, "refs/remotes/origin")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(output) != "" {
		return true, nil
	}
	return branchMergedOnGitHub(ctx, runner, repository, ref)
}

type ManagedWorktreeRemovalPlan struct {
	DeleteBranch bool
}

// PlanManagedWorktreeRemoval preserves protected repository branches. Other
// managed branches are deleted with their worktree when no other worktree uses
// them.
func PlanManagedWorktreeRemoval(ctx context.Context, runner Runner, member workspacegroup.Member) (ManagedWorktreeRemovalPlan, error) {
	branch := strings.TrimSpace(member.Branch)
	if branch == "" {
		return ManagedWorktreeRemovalPlan{}, fmt.Errorf("managed worktree branch is required")
	}
	protected, err := protectedRepositoryBranch(ctx, runner, member.Repository, branch)
	if err != nil {
		return ManagedWorktreeRemovalPlan{}, err
	}
	if protected {
		return ManagedWorktreeRemovalPlan{}, nil
	}
	if otherPath, err := branchCheckoutOutside(ctx, runner, member.Repository, branch, member.Path); err != nil {
		return ManagedWorktreeRemovalPlan{}, err
	} else if otherPath != "" {
		// The other checkout preserves the branch and its commits.
		return ManagedWorktreeRemovalPlan{}, nil
	}
	return ManagedWorktreeRemovalPlan{DeleteBranch: true}, nil
}

// RemoveManagedWorktree removes a registered worktree. Non-protected local
// branches are deleted using their expected old object ID so branch movement
// during cleanup fails safely.
func RemoveManagedWorktree(ctx context.Context, runner Runner, member workspacegroup.Member, force bool) (Workspace, error) {
	plan, err := PlanManagedWorktreeRemoval(ctx, runner, member)
	if err != nil {
		return Workspace{}, err
	}
	if !plan.DeleteBranch {
		return RemoveWorktree(ctx, runner, member.Path, force)
	}
	branch := strings.TrimSpace(member.Branch)
	ref := "refs/heads/" + branch
	head, err := runner.Run(ctx, member.Repository, "git", "rev-parse", "--verify", ref)
	if err != nil {
		return Workspace{}, fmt.Errorf("inspect managed branch %q: %w", branch, err)
	}
	head = strings.TrimSpace(head)
	removed, err := RemoveWorktree(ctx, runner, member.Path, force)
	if err != nil {
		return Workspace{}, err
	}
	if _, err := runner.Run(ctx, member.Repository, "git", "update-ref", "-d", ref, head); err != nil {
		return removed, fmt.Errorf("worktree removed but local branch %q could not be deleted: %w", branch, err)
	}
	return removed, nil
}

func protectedRepositoryBranch(ctx context.Context, runner Runner, repository, branch string) (bool, error) {
	if branch == "main" || branch == "master" {
		return true, nil
	}
	current, err := runner.Run(ctx, repository, "git", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil && strings.TrimSpace(current) == branch {
		return true, nil
	}
	remoteHead, err := runner.Run(ctx, repository, "git", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil && strings.TrimPrefix(strings.TrimSpace(remoteHead), "origin/") == branch {
		return true, nil
	}
	return false, nil
}

func branchCheckoutOutside(ctx context.Context, runner Runner, repository, branch, allowedPath string) (string, error) {
	output, err := runner.Run(ctx, repository, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	allowedPath = physicalPath(allowedPath)
	currentPath := ""
	for _, line := range append(strings.Split(output, "\n"), "") {
		if line == "" {
			currentPath = ""
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "worktree":
			currentPath = physicalPath(value)
		case "branch":
			if strings.TrimPrefix(value, "refs/heads/") == branch && currentPath != "" && currentPath != allowedPath {
				return currentPath, nil
			}
		}
	}
	return "", nil
}

func physicalPath(path string) string {
	path = filepath.Clean(path)
	// Resolve the nearest existing parent as well: missing worktrees may still
	// be recorded by Git under a physical path (for example /private/var).
	for parent := path; ; parent = filepath.Dir(parent) {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			rel, _ := filepath.Rel(parent, path)
			return filepath.Join(resolved, rel)
		}
		if filepath.Dir(parent) == parent {
			return path
		}
	}
}
