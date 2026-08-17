package workspace

import (
	"context"
	"fmt"
	"strings"

	"radar/internal/workspacegroup"
)

func validatePrimaryWorkspaceTaskLink(root, path, taskLinkingKey string) error {
	taskLinkingKey = strings.TrimSpace(taskLinkingKey)
	if taskLinkingKey == "" {
		return nil
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return err
	}
	if existing, found := workspacegroup.FindByMemberPath(registry, path); found {
		return validateTaskLink(existing.PrimaryPath, existing.TaskLinkingKey, taskLinkingKey)
	}
	return nil
}

func validateTaskLink(path, existingKey, requestedKey string) error {
	existingKey = strings.TrimSpace(existingKey)
	requestedKey = strings.TrimSpace(requestedKey)
	if existingKey != "" && requestedKey != "" && existingKey != requestedKey {
		return fmt.Errorf("workspace at %s is already linked to another task; clean it up before reusing it for this task", path)
	}
	return nil
}

func registerPrimaryWorkspace(ctx context.Context, runner Runner, plan WorktreePlan, sessionName, sandboxName string, sandbox sandboxSettings, setupScheduled bool, taskLinkingKey string) error {
	taskLinkingKey = strings.TrimSpace(taskLinkingKey)
	workspace := workspacegroup.Workspace{
		ID: workspacegroup.ID(plan.Path), Name: plan.Name, PrimaryPath: plan.Path,
		SessionName: sessionName, TaskLinkingKey: taskLinkingKey,
		Members: []workspacegroup.Member{{Repository: plan.Repo, Path: plan.Path, Branch: plan.Branch, Primary: true, SetupScheduled: setupScheduled}},
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
		if existing, found := workspacegroup.FindByMemberPath(*registry, plan.Path); found {
			if err := validateTaskLink(existing.PrimaryPath, existing.TaskLinkingKey, taskLinkingKey); err != nil {
				return err
			}
			if taskLinkingKey != "" {
				existing.TaskLinkingKey = taskLinkingKey
			}
			workspacegroup.Put(registry, existing)
			return nil
		}
		workspacegroup.Put(registry, workspace)
		return nil
	})
}
