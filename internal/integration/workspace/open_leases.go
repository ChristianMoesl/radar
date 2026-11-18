package workspace

import (
	"context"
	"fmt"
	"reflect"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

// Lease the registered bundle, never the caller's possibly nested/member path.
// These are the same path identities cleanup already leases exclusively. Keep
// the lock-file scheme and note/registry critical sections unchanged.
func acquireActivationLeases(ctx context.Context, group workspacegroup.Workspace) (func(), error) {
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	paths := []string{group.Path}
	for _, member := range group.Members {
		paths = append(paths, member.Path)
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			releaseAll()
			return nil, err
		}
		release, err := workspacelease.Acquire(path, false)
		if err != nil {
			releaseAll()
			return nil, err
		}
		releases = append(releases, release)
	}
	return releaseAll, nil
}

func activationRegistration(ctx context.Context, root, current string, expected workspacegroup.Workspace) (workspacegroup.Workspace, error) {
	if err := ctx.Err(); err != nil {
		return workspacegroup.Workspace{}, err
	}
	_, actual, found, err := RegisteredWorkspace(current, root)
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	if !found || !reflect.DeepEqual(actual, expected) {
		return workspacegroup.Workspace{}, fmt.Errorf("workspace registration changed; retry activation")
	}
	return actual, nil
}
