package tmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"radar/internal/agentservice"
	"radar/internal/cleanup"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/operationlock"
	"radar/internal/protocol"
)

// Ordinary collected/name-only targets need their actual registered owner, not
// just caller path metadata. Native name/incarnation admission is held by the
// caller. Service extends its live real path bundle; direct calls release their
// own paths only after the final native outcome. No note callback runs tmux.
func cleanupManagedSession(ctx context.Context, root string, expected workspacegroup.Workspace, target protocol.CleanupTarget, remove func(func() error, string) error) error {
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		return err
	}
	defer releaseOperation()
	var owner workspacegroup.Workspace
	var rootInfo, anchorInfo os.FileInfo
	var releasePaths func()
	defer func() {
		if releasePaths != nil {
			releasePaths()
		}
	}()
	checkOwner := func(registry workspacegroup.Registry) (workspacegroup.Workspace, error) {
		current, found := workspacegroup.FindByID(registry, expected.ID)
		if !found || current.SessionName != expected.SessionName {
			return current, fmt.Errorf("managed tmux session registration changed; preview cleanup again")
		}
		for _, other := range registry.Workspaces {
			if other.ID != current.ID && other.SessionName == current.SessionName {
				return current, fmt.Errorf("managed tmux session registration changed: name is shared; preview cleanup again")
			}
		}
		if err := agentservice.CheckWorkspaceRemoval(current.Path); err != nil {
			return current, err
		}
		return current, nil
	}
	err = workspacegroup.WithNoteLock(root, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		registry, err := workspacegroup.Load(root)
		if err != nil {
			return err
		}
		owner, err = checkOwner(registry)
		if err != nil {
			return err
		}
		paths := []string{owner.Path}
		if target.Path != "" {
			paths = append(paths, target.Path)
		}
		releasePaths, err = cleanup.AcquirePaths(ctx, paths...)
		if err != nil {
			return err
		}
		rootInfo, err = os.Stat(root)
		if err != nil {
			return err
		}
		anchorInfo, err = os.Stat(owner.Path)
		// Ordinary session cleanup historically permits an already missing anchor;
		// unlike runtime retirement it does not grant filesystem deletion authority.
		if os.IsNotExist(err) {
			anchorInfo = nil
			return nil
		}
		return err
	})
	if err != nil {
		return err
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
			registry, err := workspacegroup.Load(root)
			if err != nil {
				return err
			}
			current, err := checkOwner(registry)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(owner, current) {
				return fmt.Errorf("managed tmux session registration changed; preview cleanup again")
			}
			currentRoot, err := os.Stat(root)
			if err != nil {
				return err
			}
			currentAnchor, err := os.Stat(owner.Path)
			if os.IsNotExist(err) {
				currentAnchor = nil
			} else if err != nil {
				return err
			}
			sameAnchor := anchorInfo == nil && currentAnchor == nil || anchorInfo != nil && currentAnchor != nil && os.SameFile(anchorInfo, currentAnchor)
			if !os.SameFile(rootInfo, currentRoot) || !sameAnchor {
				return fmt.Errorf("managed tmux session filesystem identity changed; preview cleanup again")
			}
			return ctx.Err()
		})
	}
	return remove(refresh, owner.SessionName)
}
