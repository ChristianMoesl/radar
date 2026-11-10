package workspace

import (
	"fmt"
	"strings"

	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

// RuntimeCleanupTargets uses registration, not possibly missing runtime observations.
// Registration is retained so ordinary open can recover stopped/missing runtimes.
func RuntimeCleanupTargets(task protocol.Task, source string) ([]protocol.CleanupTarget, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return nil, err
	}
	var targets []protocol.CleanupTarget
	seen := map[string]bool{}
	for _, ref := range task.SourceRefs {
		if ref.Source != "workspace" || ref.Kind != "workspace" || !ref.ProvidesWorkspace || seen[ref.WorkspaceID] {
			continue
		}
		group, found := workspacegroup.FindByID(registry, ref.WorkspaceID)
		if !found || ref.ID != "workspace:"+group.ID || !sameCleanPath(ref.Path, group.Path) {
			return nil, fmt.Errorf("workspace runtime cleanup reference no longer matches registration")
		}
		seen[group.ID] = true
		target := protocol.CleanupTarget{SourceRefID: ref.ID, Source: source, Path: group.Path, WorkspaceID: group.ID, Operation: map[string]string{"session_name": group.SessionName, "sandbox_name": sandboxName(group)}}
		switch source {
		case "workspace":
			target.Kind, target.ResourceID, target.ResourceRole, target.Title, target.ProvidesWorkspace = "workspace", group.ID, "workspace", group.Name, true
			target.Description = "retain workspace files and registration"
			target.Presentation = protocol.CleanupPresentation{Singular: "retained workspace", Plural: "retained workspaces"}
		case "tmux":
			if group.SessionName == "" {
				continue
			}
			target.Kind, target.ResourceID, target.ResourceRole, target.Title = "session", group.SessionName, "multiplexer", group.SessionName
			target.Presentation = protocol.CleanupPresentation{Singular: "terminal session", Plural: "terminal sessions"}
		case "sbx":
			if group.Sandbox == nil {
				continue
			}
			if strings.TrimSpace(group.Sandbox.Name) == "" {
				return nil, fmt.Errorf("registered sandbox has no runtime identity")
			}
			target.Kind, target.ResourceID, target.ResourceRole, target.Title = "sandbox", group.Sandbox.Name, "runtime", group.Sandbox.Name
			target.Presentation = protocol.CleanupPresentation{Singular: "stopped sandbox", Plural: "stopped sandboxes"}
		default:
			return nil, fmt.Errorf("unsupported workspace runtime source %q", source)
		}
		for _, observed := range task.SourceRefs {
			if observed.Source != source || (observed.Path != "" && !sameCleanPath(observed.Path, group.Path)) {
				continue
			}
			if source == "sbx" && observed.Metadata["name"] == target.ResourceID {
				target.Operation["runtime_id"] = observed.Metadata["id"]
			}
			if source == "tmux" && observed.Title == target.ResourceID {
				target.Operation["runtime_id"] = observed.ID
			}
		}
		if _, err := ValidateRuntimeCleanupTarget(root, target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// ValidateRuntimeCleanupTarget never examines publication or disposable local data.
// Callers recheck under the note lock before each runtime side effect.
func ValidateRuntimeCleanupTarget(root string, target protocol.CleanupTarget) (workspacegroup.Workspace, error) {
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return workspacegroup.Workspace{}, err
	}
	group, found := workspacegroup.FindByID(registry, target.WorkspaceID)
	if !found || !sameCleanPath(target.Path, group.Path) || target.Operation == nil || target.Operation["session_name"] != group.SessionName || target.Operation["sandbox_name"] != sandboxName(group) {
		return workspacegroup.Workspace{}, fmt.Errorf("workspace runtime registration changed; preview cleanup again")
	}
	expected := group.ID
	switch target.Source {
	case "workspace":
		if target.Kind != "workspace" {
			return group, fmt.Errorf("invalid retained workspace target")
		}
	case "tmux":
		expected = group.SessionName
	case "sbx":
		expected = sandboxName(group)
	default:
		return group, fmt.Errorf("invalid workspace runtime cleanup source")
	}
	if expected == "" || target.ResourceID != expected {
		return group, fmt.Errorf("workspace runtime identity changed; preview cleanup again")
	}
	for _, other := range registry.Workspaces {
		if other.ID == group.ID {
			continue
		}
		if (group.SessionName != "" && other.SessionName == group.SessionName) || (sandboxName(group) != "" && sandboxName(other) == sandboxName(group)) {
			return group, fmt.Errorf("workspace runtime is shared with another registration")
		}
	}
	anchor, err := openCleanupAnchor(root, group.Path)
	if err != nil {
		return group, err
	}
	defer anchor.close()
	if anchor.anchor == nil {
		return group, fmt.Errorf("workspace anchor is missing")
	}
	return group, nil
}
