package workspace

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/workspacelease"
)

// Retain shared cleanup admission plus exclusive short-lived writer admission.
// Agents use only the former: upgrading it would block ordinary reconciliation
// for the entire lifetime of an agent. Include both current and desired bundles
// so removed members, missing additions and shared SBX names remain protected.
// All resource admission is nonblocking; never wait while holding a partial set.
func acquireWorkspaceLeases(ctx context.Context, groups ...workspacegroup.Workspace) (func(), error) {
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	paths, sandboxes := map[string]bool{}, map[string]bool{}
	var references []string
	for _, group := range groups {
		references = append(references, group.NotePath)
		bundlePaths := []string{group.Path}
		for _, member := range group.Members {
			bundlePaths = append(bundlePaths, member.Path)
			references = append(references, member.Repository)
		}
		references = append(references, bundlePaths...)
		for _, path := range bundlePaths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			physical, err := workspacelease.CanonicalPath(path)
			if err != nil {
				return nil, err
			}
			paths[physical] = true
		}
		if group.Sandbox != nil {
			sandboxes[strings.TrimSpace(group.Sandbox.Name)] = true
			references = append(references, group.Sandbox.SharedDirectory)
			for _, mount := range group.Sandbox.AdditionalMounts {
				references = append(references, mount.Path)
			}
			for _, mount := range group.Sandbox.Mounts {
				references = append(references, strings.TrimSuffix(mount, ":ro"))
			}
		}
	}
	// A different anchor may publish a note/repository/mount inside cleanup's
	// target. Match its ancestor deletion admission, not just our own resources.
	releaseReferences, err := workspacelease.AcquireReferences(ctx, references...)
	if err != nil {
		return nil, err
	}
	releases = append(releases, releaseReferences)
	// Deduplicate physical aliases before taking exclusive mutation locks:
	// an enrolled anchor can itself be a member, or occur in both bundles.
	orderedPaths, orderedSandboxes := make([]string, 0, len(paths)), make([]string, 0, len(sandboxes))
	for path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	for name := range sandboxes {
		orderedSandboxes = append(orderedSandboxes, name)
	}
	sort.Strings(orderedPaths)
	sort.Strings(orderedSandboxes)
	acquire := func(admissions ...func() (func(), error)) error {
		for _, admit := range admissions {
			if err := ctx.Err(); err != nil {
				return err
			}
			release, err := admit()
			if err != nil {
				return err
			}
			releases = append(releases, release)
		}
		return nil
	}
	for _, path := range orderedPaths {
		if err := acquire(func() (func(), error) { return workspacelease.AcquireMutation(path) }); err != nil {
			releaseAll()
			return nil, err
		}
	}
	for _, name := range orderedSandboxes {
		if err := acquire(func() (func(), error) { return workspacelease.AcquireSandbox(name, false) }, func() (func(), error) { return workspacelease.AcquireSandboxMutation(name) }); err != nil {
			releaseAll()
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		releaseAll()
		return nil, err
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
		return workspacegroup.Workspace{}, fmt.Errorf("workspace registration changed; refresh and retry")
	}
	return actual, nil
}

func validatePlanRegistration(ctx context.Context, plan ReconcileWorkspacePlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registry, err := workspacegroup.Load(plan.root)
	if err != nil {
		return err
	}
	actual, found := workspacegroup.FindByID(registry, plan.current.ID)
	if found != plan.registered || (found && !reflect.DeepEqual(actual, plan.current)) {
		return fmt.Errorf("workspace registration changed; refresh and retry")
	}
	return nil
}
