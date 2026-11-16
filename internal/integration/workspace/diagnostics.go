package workspace

import (
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
		item := protocol.DiagnosticWorkspace{ID: group.ID, Path: group.Path, Registered: true, Feedback: group.Feedback, FeedbackOrigin: protocol.DiagnosticText(group.FeedbackOrigin), NotePath: protocol.DiagnosticText(group.NotePath), SessionName: protocol.DiagnosticText(group.SessionName), Members: []protocol.DiagnosticMember{}, Mounts: []protocol.DiagnosticMount{}, Ports: []protocol.PortForward{}}
		for _, member := range group.Members {
			if len(item.Members) == protocol.DiagnosticChildLimit {
				item.Truncated = true
				break
			}
			item.Members = append(item.Members, protocol.DiagnosticMember{Path: protocol.DiagnosticText(member.Path), Repository: protocol.DiagnosticText(member.Repository)})
		}
		if group.Sandbox != nil {
			item.SandboxName, item.SharedDirectory = protocol.DiagnosticText(group.Sandbox.Name), protocol.DiagnosticText(group.Sandbox.SharedDirectory)
			for _, mount := range group.Sandbox.Mounts {
				if len(item.Mounts) == protocol.DiagnosticChildLimit {
					item.Truncated = true
					break
				}
				item.Mounts = append(item.Mounts, protocol.DiagnosticMount{Path: protocol.DiagnosticText(mount)})
			}
			for _, mount := range group.Sandbox.AdditionalMounts {
				if len(item.Mounts) == protocol.DiagnosticChildLimit {
					item.Truncated = true
					break
				}
				item.Mounts = append(item.Mounts, protocol.DiagnosticMount{Path: protocol.DiagnosticText(mount.Path), ReadOnly: mount.ReadOnly})
			}
			for _, port := range group.Sandbox.Ports {
				if len(item.Ports) == protocol.DiagnosticChildLimit {
					item.Truncated = true
					break
				}
				item.Ports = append(item.Ports, protocol.PortForward{HostPort: port.HostPort, SandboxPort: port.SandboxPort})
			}
		}
		items = append(items, item)
	}
	return items, nil
}
