package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/obsidian"
	"radar/internal/integration/workspace/group"
	"radar/internal/linking"
	"radar/internal/protocol"
)

type Source struct {
	noteAuthor integration.WorkspaceNoteAuthor
}

func NewSource(author integration.WorkspaceNoteAuthor) Source { return Source{noteAuthor: author} }

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "workspace", Label: "Radar workspace", DisplayOrder: 2, CleanupOrder: 3}
}

func (Source) Local() bool { return true }

func (Source) Status(context.Context, *slog.Logger) integration.StatusResult {
	return integration.StatusResult{Status: protocol.SourceStatus{Name: "workspace", Status: "ok"}, CanRun: true}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	root, disposable, err := loadAnchorCleanupSettings()
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
			refreshed, refreshErr := RefreshWorkspaceNote(root, group)
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
			ProvidesWorkspace: true, WorkspaceEntry: true, WorkspaceID: group.ID, CanonicalKey: canonical,
			LinkingKeys:  linking.Keys(canonical, linking.WorkspaceGroupKey(group.ID), group.TaskLinkingKey, group.NoteLinkingKey),
			Presentation: protocol.SourceRefPresentation{WorkspaceName: group.Name},
			Metadata:     map[string]string{"workspace_id": group.ID},
		}
		if group.NotePath != "" {
			ref.Metadata["note_path"] = group.NotePath
			ref.WorkspaceAnchorPath = group.NotePath
		}
		if _, err := anchorCleanupEntries(root, registry, group, disposable, integration.CleanupSafe); err != nil {
			ref.CleanupIssues = append(ref.CleanupIssues, err.Error())
		}
		for _, member := range group.Members {
			if _, err := os.Lstat(member.Path); os.IsNotExist(err) {
				if _, err := inspectMissingMember(ctx, ExecRunner{}, member); err != nil {
					ref.CleanupIssues = append(ref.CleanupIssues, fmt.Sprintf("%s: %v", member.Path, err))
				}
			}
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

func (Source) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	root, disposable, err := loadAnchorCleanupSettings()
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
		entries, err := anchorCleanupEntries(root, registry, group, disposable, req.Mode)
		if err != nil {
			return nil, err
		}
		// A vanished member has no files to discard. Forget its targeted Git
		// worktree registration, but retain its branch and all commits.
		for _, member := range group.Members {
			if _, err := os.Lstat(member.Path); os.IsNotExist(err) {
				if _, err := inspectMissingMember(ctx, ExecRunner{}, member); err != nil {
					return nil, err
				}
				targets = append(targets, protocol.CleanupTarget{
					SourceRefID: ref.ID, Source: "workspace", Kind: "missing_member", WorkspaceID: group.ID,
					Path: member.Path, Title: member.Branch, ResourceID: member.Path,
					Presentation: protocol.CleanupPresentation{Singular: "stale worktree registration", Plural: "stale worktree registrations"},
					Description:  "forget missing worktree " + member.Path + " (branch kept)",
				})
			} else if err != nil {
				return nil, err
			} else if req.Mode == integration.CleanupExpired && !slices.ContainsFunc(req.Task.SourceRefs, func(memberRef protocol.SourceRef) bool {
				return memberRef.Source == "git" && memberRef.Kind == "worktree" &&
					memberRef.WorkspaceID == group.ID && sameCleanPath(memberRef.Path, member.Path)
			}) {
				// A partial Git collection must not let runtime targets execute
				// before the anchor discovers that a member was never removed.
				return nil, fmt.Errorf("registered workspace member is missing its Git cleanup reference: %s", member.Path)
			}
		}
		description := "workspace anchor " + group.Path
		if group.Sandbox != nil && group.Sandbox.SharedDirectory != "" {
			description += " and shared temporary files (including screenshots) in " + group.Sandbox.SharedDirectory
		}
		target := protocol.CleanupTarget{
			SourceRefID: ref.ID, Source: "workspace", Kind: "workspace", Title: group.Name, Path: group.Path,
			Presentation: protocol.CleanupPresentation{Singular: "workspace directory", Plural: "workspace directories"},
			Description:  description, ResourceRole: "workspace", ResourceID: group.ID,
			ProvidesWorkspace: true, WorkspaceID: group.ID,
		}
		addDisposableEntriesPreview(&target, entries.disposable)
		addExpiredEntriesPreview(&target, entries.expired)
		targets = append(targets, target)
	}
	return targets, nil
}

func (Source) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	root, err := DefaultRoot()
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	if req.Target.Kind == "missing_member" {
		err := forgetMissingMember(ctx, ExecRunner{}, root, req.Target)
		return req.Target, err
	}
	var removed workspacegroup.Workspace
	err = workspacegroup.WithNoteLock(root, func() error {
		var err error
		removed, err = removeWorkspaceAnchor(root, req.Target, req.Mode)
		return err
	})
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	if removed.NotePath != "" && strings.HasPrefix(removed.NoteKey(), "obsidian:task:") {
		if err := obsidian.NewSource().ArchiveCompletedNote(root, removed.NotePath, removed.NoteKey()); err != nil {
			return req.Target, fmt.Errorf("workspace removed, but note archiving failed: %w", err)
		}
	}
	return req.Target, nil
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.CleanupProvider = Source{}
