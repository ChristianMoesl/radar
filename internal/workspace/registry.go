package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/workspacegroup"
)

func validateWorkspaceTaskLink(root, path, taskLinkingKey string) error {
	taskLinkingKey = strings.TrimSpace(taskLinkingKey)
	if taskLinkingKey == "" {
		return nil
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return err
	}
	if existing, found := workspacegroup.FindByContainingPath(registry, path); found {
		return validateTaskLink(existing.Path, existing.TaskLinkingKey, taskLinkingKey)
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

func registerWorkspace(root string, value workspacegroup.Workspace) error {
	return workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
		if existing, found := workspacegroup.FindByID(*registry, value.ID); found {
			if err := validateTaskLink(existing.Path, existing.TaskLinkingKey, value.TaskLinkingKey); err != nil {
				return err
			}
		}
		if existing, found := workspacegroup.FindByContainingPath(*registry, value.Path); found && existing.ID != value.ID {
			return fmt.Errorf("workspace path %s belongs to Radar workspace %q", value.Path, existing.Name)
		}
		if value.TaskLinkingKey != "" {
			if existing, found := workspacegroup.FindByTaskLinkingKey(*registry, value.TaskLinkingKey); found && existing.ID != value.ID {
				return fmt.Errorf("task is already linked to workspace at %s", existing.Path)
			}
		}
		for _, member := range value.Members {
			if existing, found := workspacegroup.FindByMemberPath(*registry, member.Path); found && existing.ID != value.ID {
				return fmt.Errorf("worktree %s belongs to Radar workspace %q", member.Path, existing.Name)
			}
		}
		workspacegroup.Put(registry, value)
		return nil
	})
}

func existingWorkspaceForTask(root, taskLinkingKey string) (workspacegroup.Workspace, bool, error) {
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return workspacegroup.Workspace{}, false, err
	}
	returnValue, found := workspacegroup.FindByTaskLinkingKey(registry, strings.TrimSpace(taskLinkingKey))
	return returnValue, found, nil
}

func existingWorkspaceForMember(root, repository, branch string) (workspacegroup.Workspace, workspacegroup.Member, bool, error) {
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return workspacegroup.Workspace{}, workspacegroup.Member{}, false, err
	}
	for _, group := range registry.Workspaces {
		for _, member := range group.Members {
			if sameCleanPath(member.Repository, repository) && strings.TrimSpace(member.Branch) == strings.TrimSpace(branch) {
				return group, member, true, nil
			}
		}
	}
	return workspacegroup.Workspace{}, workspacegroup.Member{}, false, nil
}

func RegisteredWorkspace(current, configuredRoot string) (string, workspacegroup.Workspace, bool, error) {
	root, err := workspaceRoot(configuredRoot)
	if err != nil {
		return "", workspacegroup.Workspace{}, false, err
	}
	current, err = filepath.Abs(strings.TrimSpace(current))
	if err != nil {
		return "", workspacegroup.Workspace{}, false, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return "", workspacegroup.Workspace{}, false, err
	}
	group, found := workspacegroup.FindByContainingPath(registry, current)
	return root, group, found, nil
}

func workspaceAnchorPath(root, name, taskLinkingKey string) (string, error) {
	baseName := WorkspaceDirectoryName(name)
	base := filepath.Join(root, baseName)
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return "", err
	}
	for _, group := range registry.Workspaces {
		if sameCleanPath(group.Path, base) {
			if taskLinkingKey != "" && group.TaskLinkingKey == taskLinkingKey {
				return group.Path, nil
			}
			return filepath.Join(root, baseName+"--"+sandboxNameHash("workspace", name+"\x00"+taskLinkingKey)), nil
		}
	}
	if _, err := os.Lstat(base); err == nil {
		return filepath.Join(root, baseName+"--"+sandboxNameHash("workspace", name+"\x00"+taskLinkingKey)), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return base, nil
}
