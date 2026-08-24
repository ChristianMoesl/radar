package workspaceanchor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
	"radar/internal/workspace"
	"radar/internal/workspacegroup"
)

type Source struct{}

func NewSource() Source { return Source{} }

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "workspace", Label: "Radar workspace", DisplayOrder: 2, CleanupOrder: 3}
}

func (Source) Local() bool { return true }

func (Source) Status(context.Context, *slog.Logger) integration.StatusResult {
	return integration.StatusResult{Status: protocol.SourceStatus{Name: "workspace", Status: "ok"}, CanRun: true}
}

func (Source) Collect(_ context.Context, req integration.CollectRequest) integration.CollectResult {
	root, err := workspace.DefaultRoot()
	if err != nil {
		status := protocol.SourceStatus{Name: "workspace", Status: "error", Detail: err.Error()}
		return integration.CollectResult{SourceStatus: &status}
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		status := protocol.SourceStatus{Name: "workspace", Status: "error", Detail: err.Error()}
		return integration.CollectResult{SourceStatus: &status}
	}
	observations := make([]integration.Observation, 0, len(registry.Workspaces))
	problems := make([]string, 0)
	for _, group := range registry.Workspaces {
		if group.NotePath != "" {
			refreshed, refreshErr := workspace.RefreshWorkspaceNote(root, group)
			if refreshErr != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", group.NotePath, refreshErr))
			} else {
				group = refreshed
			}
		}
		if info, statErr := os.Stat(group.Path); statErr != nil || !info.IsDir() {
			problems = append(problems, fmt.Sprintf("%s: workspace anchor is missing", group.Path))
		}
		if group.NotePath != "" {
			if _, statErr := os.Stat(group.NotePath); statErr != nil {
				problems = append(problems, fmt.Sprintf("%s: canonical note is missing", group.NotePath))
			}
		}
		canonical := linking.WorkspaceKey(group.Path)
		ref := protocol.SourceRef{
			ID: "workspace:" + group.ID, EntityID: "workspace:" + group.ID,
			Source: "workspace", SourceLabel: "Radar workspace", Kind: "workspace",
			Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkspace,
			Authority: protocol.SourceRefAuthorityNone, Title: group.Name, Path: group.Path,
			ProvidesWorkspace: true, CanonicalKey: canonical,
			LinkingKeys:  linking.Keys(canonical, linking.WorkspaceGroupKey(group.ID), group.TaskLinkingKey),
			Presentation: protocol.SourceRefPresentation{WorkspaceName: group.Name},
			Metadata:     map[string]string{"workspace_id": group.ID},
		}
		if group.NotePath != "" {
			ref.Metadata["note_path"] = group.NotePath
		}
		observations = append(observations, integration.Observation{Ref: ref})
	}
	status := protocol.SourceStatus{Name: "workspace", Status: "ok", Detail: fmt.Sprintf("%d workspaces", len(observations))}
	complete := true
	if len(problems) > 0 {
		sort.Strings(problems)
		status.Status = "partial"
		status.Detail = strings.Join(problems, "; ")
		complete = false
	}
	return integration.CollectResult{Observations: observations, Complete: complete, SourceStatus: &status}
}

func (Source) PreviewCleanup(_ context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	root, err := workspace.DefaultRoot()
	if err != nil {
		return nil, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return nil, err
	}
	targets := make([]protocol.CleanupTarget, 0)
	for _, ref := range req.Task.SourceRefs {
		if ref.Source != "workspace" || ref.Kind != "workspace" {
			continue
		}
		id := strings.TrimPrefix(ref.ID, "workspace:")
		group, found := workspacegroup.FindByID(registry, id)
		if !found {
			continue
		}
		if unknown, err := unknownAnchorEntries(group); err != nil {
			return nil, err
		} else if len(unknown) > 0 {
			return nil, fmt.Errorf("workspace anchor contains unknown files: %s", strings.Join(unknown, ", "))
		}
		targets = append(targets, protocol.CleanupTarget{SourceRefID: ref.ID, Source: "workspace", Kind: "workspace", Title: group.Name, Path: group.Path})
	}
	return targets, nil
}

func (Source) Cleanup(_ context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	root, err := workspace.DefaultRoot()
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	id := strings.TrimPrefix(req.Target.SourceRefID, "workspace:")
	group, found := workspacegroup.FindByID(registry, id)
	if !found {
		return req.Target, nil
	}
	if len(group.Members) > 0 {
		return protocol.CleanupTarget{}, fmt.Errorf("workspace still contains %d managed worktree(s)", len(group.Members))
	}
	unknown, err := unknownAnchorEntries(group)
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	if len(unknown) > 0 {
		return protocol.CleanupTarget{}, fmt.Errorf("workspace anchor contains unknown files: %s", strings.Join(unknown, ", "))
	}
	link := filepath.Join(group.Path, "note.md")
	if info, statErr := os.Lstat(link); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(link); err != nil {
			return protocol.CleanupTarget{}, err
		}
	}
	if err := os.Remove(group.Path); err != nil && !os.IsNotExist(err) {
		return protocol.CleanupTarget{}, err
	}
	if err := workspacegroup.RemoveWorkspace(root, group.ID); err != nil {
		return protocol.CleanupTarget{}, err
	}
	return req.Target, nil
}

func unknownAnchorEntries(group workspacegroup.Workspace) ([]string, error) {
	entries, err := os.ReadDir(group.Path)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	managed := map[string]bool{"note.md": group.NotePath != ""}
	for _, member := range group.Members {
		managed[filepath.Base(member.Path)] = true
	}
	unknown := make([]string, 0)
	for _, entry := range entries {
		path := filepath.Join(group.Path, entry.Name())
		if !managed[entry.Name()] || (entry.Name() == "note.md" && entry.Type()&os.ModeSymlink == 0) {
			unknown = append(unknown, path)
		}
	}
	sort.Strings(unknown)
	return unknown, nil
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.CleanupProvider = Source{}
