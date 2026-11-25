package workspace

import (
	"context"
	"fmt"
	"strings"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

var _ integration.WorkspaceRegistrationDiagnostics = Source{}
var _ integration.WorkspaceRetirementPreview = Source{}

func (Source) DiagnosticRetirementTargets(ctx context.Context, task protocol.Task, workspaceID string) ([]protocol.CleanupTarget, error) {
	// Keep only the selected workspace anchor. Runtime refs are retained solely
	// for the native planner's cached-lifetime matching; it does not probe them.
	refs := make([]protocol.SourceRef, 0, len(task.SourceRefs))
	for _, ref := range task.SourceRefs {
		if ref.Source != "workspace" || ref.WorkspaceID == workspaceID {
			refs = append(refs, ref)
		}
	}
	task.SourceRefs = refs
	targets := []protocol.CleanupTarget{}
	for _, source := range []string{"tmux", "sbx", "workspace"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		planned, err := RuntimeCleanupTargets(task, source)
		if err != nil {
			return nil, err
		}
		if len(planned) > 1 {
			return nil, fmt.Errorf("retirement preview requires one registered workspace")
		}
		targets = append(targets, planned...)
	}
	return targets, ctx.Err()
}

func (Source) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	registry, err := workspacegroup.DiagnosticLoad(root)
	if err != nil {
		return nil, err
	}
	items := make([]protocol.DiagnosticWorkspace, 0, len(registry.Workspaces))
	for _, group := range registry.Workspaces {
		item := protocol.DiagnosticWorkspace{ID: group.ID, Path: group.Path, Registered: true, Feedback: group.Feedback, FeedbackOrigin: protocol.DiagnosticText(group.FeedbackOrigin), NotePath: protocol.DiagnosticText(group.NotePath), SessionName: group.SessionName, Members: []protocol.DiagnosticMember{}, Mounts: []protocol.DiagnosticMount{}, Ports: []protocol.PortForward{}}
		for _, member := range group.Members {
			item.Members = append(item.Members, protocol.DiagnosticMember{Path: member.Path, Repository: member.Repository})
		}
		if group.Sandbox != nil {
			item.SandboxName, item.SharedDirectory = group.Sandbox.Name, protocol.DiagnosticText(group.Sandbox.SharedDirectory)
			for _, mount := range group.Sandbox.Mounts {
				item.Mounts = append(item.Mounts, protocol.DiagnosticMount{Path: strings.TrimSuffix(mount, ":ro"), ReadOnly: strings.HasSuffix(mount, ":ro")})
			}
			for _, mount := range group.Sandbox.AdditionalMounts {
				item.Mounts = append(item.Mounts, protocol.DiagnosticMount{Path: mount.Path, ReadOnly: mount.ReadOnly})
			}
			for _, port := range group.Sandbox.Ports {
				item.Ports = append(item.Ports, protocol.PortForward{HostPort: port.HostPort, SandboxPort: port.SandboxPort})
			}
		}
		items = append(items, item)
	}
	return items, nil
}
