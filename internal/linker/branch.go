package linker

import (
	"strings"

	"radar.nvim/internal/protocol"
	"radar.nvim/internal/workspace"
)

func linkSourceRefsByBranch(items []protocol.Task, sourceRefs []protocol.SourceRef) []protocol.Task {
	for i := range items {
		for _, itemRef := range items[i].SourceRefs {
			for _, sourceRef := range sourceRefs {
				if sourceRefBranchesMatch(itemRef, sourceRef) {
					items[i].SourceRefs = appendSourceRef(items[i].SourceRefs, sourceRef)
				}
			}
		}
	}
	return items
}

func sourceRefBranchesMatch(left, right protocol.SourceRef) bool {
	if normalizedBranch(left.Branch) == "" || normalizedBranch(right.Branch) == "" {
		return false
	}
	if !branchLinkableSource(left) || !branchLinkableSource(right) {
		return false
	}
	if !repoNamesCompatible(left, right) {
		return false
	}
	return normalizedBranch(left.Branch) == normalizedBranch(right.Branch)
}

func branchLinkableSource(ref protocol.SourceRef) bool {
	return (ref.Source == "github" && ref.Kind == "pull_request") || (ref.Source == "git" && ref.Kind == "worktree")
}

func normalizedBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/remotes/")
	branch = strings.TrimPrefix(branch, "origin/")
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if branch == "" {
		return ""
	}
	return workspace.BranchName(branch)
}

func repoNamesCompatible(left, right protocol.SourceRef) bool {
	leftName := sourceRefRepoName(left)
	rightName := sourceRefRepoName(right)
	return leftName != "" && rightName != "" && leftName == rightName
}

func sourceRefRepoName(ref protocol.SourceRef) string {
	return normalizeRepoName(ref.Repo)
}

func normalizeRepoName(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimPrefix(repo, "git@github.com:")
	if strings.Contains(repo, "://") || strings.Contains(repo, "@") {
		return ""
	}
	return repo
}
