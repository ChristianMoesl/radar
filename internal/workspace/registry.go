package workspace

import (
	"context"

	"radar/internal/workspacegroup"
)

func registerPrimaryWorkspace(ctx context.Context, runner Runner, plan WorktreePlan, sessionName, sandboxName string, sandbox sandboxSettings, setupScheduled bool) error {
	workspace := workspacegroup.Workspace{
		ID: workspacegroup.ID(plan.Path), Name: plan.Name, PrimaryPath: plan.Path,
		SessionName: sessionName,
		Members:     []workspacegroup.Member{{Repository: plan.Repo, Path: plan.Path, Branch: plan.Branch, Primary: true, SetupScheduled: setupScheduled}},
	}
	if sandboxName != "" {
		mounts, err := sandboxMounts(ctx, runner, plan.Path, sandbox.AdditionalMounts)
		if err != nil {
			return err
		}
		if actual, found, listErr := findSandbox(ctx, runner, plan.Path, sandboxName); listErr == nil && found {
			mounts = sandboxWorkspaceMounts(actual)
		}
		workspace.Sandbox = &workspacegroup.Sandbox{Name: sandboxName, Agent: sandbox.Kit.Name, KitPath: ExpandPath(sandbox.Kit.Path), Mounts: mounts}
	}
	return workspacegroup.Update(plan.Root, func(registry *workspacegroup.Registry) error {
		workspacegroup.Put(registry, workspace)
		return nil
	})
}
