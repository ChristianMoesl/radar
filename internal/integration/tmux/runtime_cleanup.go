package tmux

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"radar/internal/agentservice"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/operationlock"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func retireSession(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget, verify func() (sessionlease.Identity, error), pathsAdmitted bool) (protocol.CleanupTarget, error) {
	// Never mutate the caller's preview while stamping the result.
	target.Operation = maps.Clone(target.Operation)
	delete(target.Operation, "runtime_retired")
	if target.Source != "tmux" || target.Kind != "session" {
		return target, fmt.Errorf("invalid registered tmux retirement target")
	}
	if err := ctx.Err(); err != nil {
		return target, err
	}
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		return target, err
	}
	defer releaseOperation()
	root, err := workspace.DefaultRoot()
	if err != nil {
		return target, err
	}
	var owner workspacegroup.Workspace
	var rootInfo, anchorInfo os.FileInfo
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		owner, err = workspace.ValidateRuntimeCleanupTarget(root, target)
		if err != nil {
			return err
		}
		rootInfo, err = os.Stat(root)
		if err != nil {
			return err
		}
		anchorInfo, err = os.Stat(owner.Path)
		return err
	})
	if err != nil {
		return target, err
	}
	// Service already holds the complete canonical bundle. Public direct
	// retirement must retain the actual owner's path as well as native leases.
	if !pathsAdmitted {
		if err := agentservice.CheckWorkspaceRemoval(owner.Path); err != nil {
			return target, err
		}
		release, err := workspacelease.Acquire(owner.Path, true)
		if err != nil {
			return target, err
		}
		defer release()
	}
	refresh := func() error {
		return workspacegroup.WithNoteLock(root, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			configured, err := workspace.DefaultRoot()
			if err != nil {
				return err
			}
			if filepath.Clean(configured) != filepath.Clean(root) {
				return fmt.Errorf("configured workspace root changed; preview cleanup again")
			}
			current, err := workspace.ValidateRuntimeCleanupTarget(root, target)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(owner, current) {
				return fmt.Errorf("workspace runtime registration changed; preview cleanup again")
			}
			currentRoot, err := os.Stat(root)
			if err != nil {
				return err
			}
			currentAnchor, err := os.Stat(owner.Path)
			if err != nil {
				return err
			}
			if !os.SameFile(rootInfo, currentRoot) || !os.SameFile(anchorInfo, currentAnchor) {
				return fmt.Errorf("workspace runtime filesystem identity changed; preview cleanup again")
			}
			return ctx.Err()
		})
	}
	if err := refresh(); err != nil {
		return target, err
	}
	// Native name/incarnation admission spans every command, but the global
	// note callback covers only fresh metadata decisions, never provider I/O.
	if err := runner.LookPath("tmux"); err != nil {
		return target, err
	}
	identity, err := verify()
	if err != nil {
		return target, err
	}
	if err := refresh(); err != nil {
		return target, err
	}
	if identity.ID == "" {
		return target, nil
	}
	output, err := runner.Run(ctx, "", "tmux", "list-sessions", "-F", sessionFormat)
	if err != nil {
		detail := strings.ToLower(output + " " + err.Error())
		if strings.Contains(detail, "no server running") || (strings.Contains(detail, "error connecting to") && strings.Contains(detail, "no such file or directory")) {
			return target, refresh()
		}
		return target, err
	}
	if err := refresh(); err != nil {
		return target, err
	}
	sessions, err := parseSessions(output)
	if err != nil {
		return target, err
	}
	for _, session := range sessions {
		if session.Name != target.ResourceID {
			continue
		}
		if session.sourceRefID() != identity.Key() {
			return target, fmt.Errorf("tmux runtime identity changed; preview cleanup again")
		}
		owned := filepath.Clean(session.Path) == filepath.Clean(owner.Path)
		for _, member := range owner.Members {
			owned = owned || filepath.Clean(session.Path) == filepath.Clean(member.Path)
		}
		if session.Path == "" || !owned {
			return target, fmt.Errorf("tmux session no longer belongs to its registered workspace")
		}
		if _, err := runner.Run(ctx, "", "tmux", "kill-session", "-t", session.ID); err != nil {
			return target, err
		}
		if err := refresh(); err != nil {
			return target, err
		}
		// A successful kill exit is not evidence that this lifetime and its
		// reusable registered name are absent. Reuse native exact admission's
		// missing/replacement checks while the original bundle is still held.
		remaining, err := verify()
		if err != nil {
			return target, err
		}
		if err := refresh(); err != nil {
			return target, err
		}
		if remaining.ID != "" {
			return target, fmt.Errorf("registered tmux session still exists after retirement")
		}
		target.Operation["runtime_retired"] = "true"
		return target, nil
	}
	return target, nil // Already missing; ordinary open will recreate the session.
}
