package tmux

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func retireSession(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget) (protocol.CleanupTarget, error) {
	delete(target.Operation, "runtime_retired")
	root, err := workspace.DefaultRoot()
	if err != nil {
		return target, err
	}
	err = workspacegroup.WithNoteLock(root, func() error {
		group, err := workspace.ValidateRuntimeCleanupTarget(root, target)
		if err != nil {
			return err
		}
		if err := runner.LookPath("tmux"); err != nil {
			return err
		}
		output, err := runner.Run(ctx, "", "tmux", "list-sessions", "-F", sessionFormat)
		if err != nil {
			detail := strings.ToLower(output + " " + err.Error())
			if strings.Contains(detail, "no server running") || (strings.Contains(detail, "error connecting to") && strings.Contains(detail, "no such file or directory")) {
				return nil
			}
			return err
		}
		sessions, err := parseSessions(output)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.Name != target.ResourceID {
				continue
			}
			if expected := target.Operation["runtime_id"]; expected != "" && session.sourceRefID() != expected {
				return fmt.Errorf("tmux runtime identity changed; preview cleanup again")
			}
			owned := filepath.Clean(session.Path) == filepath.Clean(group.Path)
			for _, member := range group.Members {
				owned = owned || filepath.Clean(session.Path) == filepath.Clean(member.Path)
			}
			if session.Path == "" || !owned {
				return fmt.Errorf("tmux session no longer belongs to its registered workspace")
			}
			_, err := runner.Run(ctx, "", "tmux", "kill-session", "-t", session.ID)
			if err == nil {
				target.Operation["runtime_retired"] = "true"
			}
			return err
		}
		return nil // Already missing; ordinary open will recreate the session.
	})
	return target, err
}
