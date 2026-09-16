package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func forgetMissingMember(ctx context.Context, runner Runner, root string, target protocol.CleanupTarget) error {
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return err
	}
	group, found := workspacegroup.FindByID(registry, target.WorkspaceID)
	if !found {
		return nil
	}
	var member *workspacegroup.Member
	for _, candidate := range group.Members {
		if filepath.Clean(candidate.Path) == filepath.Clean(target.Path) {
			copy := candidate
			member = &copy
			break
		}
	}
	if member == nil {
		return nil
	}
	registered, err := inspectMissingMember(ctx, runner, *member)
	if err != nil {
		return err
	}
	if registered {
		// No --force: a reappearing dirty or locked worktree must stop cleanup.
		if _, err := runner.Run(ctx, member.Repository, "git", "worktree", "remove", member.Path); err != nil {
			return err
		}
	}
	// Deliberately do not remove the local branch: its publication is irrelevant
	// when all we are discarding is a registration for an absent directory.
	return workspacegroup.RemoveMember(root, member.Path)
}

// Validate before any cleanup target is executed, and recheck at execution.
func inspectMissingMember(ctx context.Context, runner Runner, member workspacegroup.Member) (bool, error) {
	if _, err := os.Lstat(member.Path); !os.IsNotExist(err) {
		if err != nil {
			return false, err
		}
		return false, fmt.Errorf("worktree reappeared; refusing to forget it: %s", member.Path)
	}
	output, err := runner.Run(ctx, member.Repository, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	found, selected := false, false
	for _, line := range strings.Split(output, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			selected = physicalPath(path) == physicalPath(member.Path)
			found = found || selected
		} else if selected && (line == "locked" || strings.HasPrefix(line, "locked ")) {
			return false, fmt.Errorf("missing worktree registration is locked: %s", member.Path)
		} else if line == "" {
			selected = false
		}
	}
	return found, nil
}
