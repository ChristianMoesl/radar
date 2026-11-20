package sbx

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

// Ordinary name-only targets need their actual registered owner, not just
// caller path metadata. The caller retains reusable-name admission. A live
// Service owns any discovered paths through its final provider; direct calls
// release only after the native outcome. No note callback runs SBX commands.
func cleanupManagedSandbox(ctx context.Context, root string, expected workspacegroup.Workspace, target protocol.CleanupTarget, remove func(func() error) error) error {
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
		if !found || current.Sandbox == nil || expected.Sandbox == nil || current.Sandbox.Name != expected.Sandbox.Name {
			return current, fmt.Errorf("managed sandbox registration changed; preview cleanup again")
		}
		for _, other := range registry.Workspaces {
			if other.ID != current.ID && other.Sandbox != nil && other.Sandbox.Name == current.Sandbox.Name {
				return current, fmt.Errorf("managed sandbox registration changed: name is shared; preview cleanup again")
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
		// Ordinary sandbox cleanup historically permits an already missing anchor;
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
				return fmt.Errorf("managed sandbox registration changed; preview cleanup again")
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
				return fmt.Errorf("managed sandbox filesystem identity changed; preview cleanup again")
			}
			return ctx.Err()
		})
	}
	if err := refresh(); err != nil {
		return err
	}
	if err := remove(refresh); err != nil {
		return err
	}
	return refresh()
}
