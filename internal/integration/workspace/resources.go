package workspace

import (
	"context"
	"fmt"
	"path/filepath"

	"radar/internal/integration"
)

// InspectWorkspaceResources rereads registration, member resources and the live
// owned sandbox on every lifecycle hook. It does no Git status, port, catalogue,
// enrollment or opening-resource discovery, and retains no cross-request cache.
func InspectWorkspaceResources(ctx context.Context, runner Runner, current, root string) (integration.WorkspaceResources, error) {
	result := integration.WorkspaceResources{Members: []integration.WorkspaceResourceMember{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	_, group, found, err := RegisteredWorkspace(current, root)
	if err != nil || !found {
		return result, err
	}
	result.Registered, result.WorkspaceID, result.WorkspacePath = true, group.ID, group.Path
	if group.NotePath != "" {
		result.Note = &integration.WorkspaceContextNote{Path: group.NotePath, WorkspacePath: filepath.Join(group.Path, "notes.md"), LinkingKey: group.NoteKey()}
	}
	for _, member := range group.Members {
		result.Members = append(result.Members, integration.WorkspaceResourceMember{
			Repository: member.Repository, Path: member.Path,
			InstructionFiles: memberInstructionFiles(member.Path), SkillPaths: memberSkillPaths(member.Path),
		})
	}
	if group.Sandbox != nil {
		observation := observeSandbox(ctx, runner, group)
		sandbox := &integration.WorkspaceResourceSandbox{Name: group.Sandbox.Name, SharedDirectory: group.Sandbox.SharedDirectory}
		sandbox.SharedDirectoryReady = observation.found && sharedDirectoryReady(sandbox.SharedDirectory, sandboxWorkspaceMounts(observation.actual))
		if observation.err != nil {
			sandbox.RuntimeError = observation.err.Error()
		} else if !observation.found {
			sandbox.RuntimeError = fmt.Sprintf("sandbox %s is absent; reconcile the retained desired state to provision or retry", sandbox.Name)
		}
		result.Sandbox = sandbox
	}
	if err := ctx.Err(); err != nil {
		return integration.WorkspaceResources{}, err
	}
	return result, nil
}
