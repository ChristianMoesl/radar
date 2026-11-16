package workspace

import (
	"strings"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

var _ integration.WorkspaceRegistrationDiagnostics = Source{}

func (Source) DiagnosticWorkspaceRegistrations() ([]protocol.DiagnosticWorkspace, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	registry, err := workspacegroup.Load(root)
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
