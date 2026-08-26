package git

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/pathdisplay"
	"radar/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "git", Label: "Git", DisplayOrder: 2, CleanupOrder: 2}
}

func (Source) Local() bool {
	return true
}

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	return integration.StatusResult{
		Status: protocol.SourceStatus{Name: "git", Status: "ok"},
		CanRun: true,
	}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	source_refs, status := FetchWorktrees(ctx, req.Logger, req.LinkingMarks)
	if status.Status == "error" {
		req.Logger.Warn("git worktree collection failed", "detail", status.Detail)
		return integration.CollectResult{Observations: integration.ObserveRefs(source_refs, integration.SignalInProgress), SourceStatus: &status}
	}
	return integration.CollectResult{Observations: integration.ObserveRefs(source_refs, integration.SignalInProgress), Complete: status.Status == "ok", SourceStatus: &status}
}

func (Source) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	targets := make([]protocol.CleanupTarget, 0)
	root, err := workspace.DefaultRoot()
	if err != nil {
		return nil, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return nil, err
	}
	for _, ref := range req.Task.SourceRefs {
		if ref.Source != "git" || ref.Kind != "worktree" || ref.Path == "" {
			continue
		}
		if _, err := os.Stat(ref.Path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		mainWorkingTree, err := mainWorkingTree(ctx, ref.Path)
		if err != nil {
			return nil, err
		}
		if mainWorkingTree {
			return nil, fmt.Errorf("main working tree cannot be cleaned up from Radar")
		}
		status, err := gitOutput(ctx, ref.Path, "status", "--porcelain")
		if err != nil {
			return nil, err
		}
		target := protocol.CleanupTarget{
			SourceRefID: ref.ID, Source: "git", Kind: "worktree", Title: ref.Title,
			ResourceRole: "workspace", ResourceID: ref.Path, Path: ref.Path, Branch: ref.Branch,
			ProvidesWorkspace: ref.ProvidesWorkspace, WorkspaceID: strings.TrimSpace(ref.WorkspaceID),
		}
		if strings.TrimSpace(status) != "" {
			target.Safety = append(target.Safety, protocol.CleanupSafety{
				Kind: "local_changes", Message: "uncommitted changes will be discarded", BlocksAutomatic: true,
			})
		}
		if member, managed := workspacegroup.FindMemberByPath(registry, ref.Path); managed {
			removal, err := workspace.PlanManagedWorktreeRemoval(ctx, workspace.ExecRunner{}, member)
			if err != nil {
				return nil, err
			}
			target.Branch = member.Branch
			if removal.DeleteBranch {
				target.Operation = map[string]string{"delete_branch": member.Branch}
				target.Safety = append(target.Safety, protocol.CleanupSafety{
					Kind: "deletes_local_data", Message: "deletes local branch " + member.Branch,
				})
				published, publicationErr := workspace.BranchPublished(ctx, workspace.ExecRunner{}, member.Repository, member.Branch)
				if publicationErr != nil {
					target.Safety = append(target.Safety, protocol.CleanupSafety{
						Kind: "safety_check_unavailable", Message: "branch publication could not be verified", BlocksAutomatic: true,
					})
				} else if !published {
					target.Safety = append(target.Safety, protocol.CleanupSafety{
						Kind: "unpublished_data", Message: "branch commits were not found remotely", BlocksAutomatic: true,
					})
				}
			}
		}
		target.Description = cleanupDescription(target)
		targets = append(targets, target)
	}
	return targets, nil
}

func cleanupDescription(target protocol.CleanupTarget) string {
	description := "worktree " + pathdisplay.HomeRelative(target.Path)
	details := make([]string, 0, len(target.Safety))
	for _, safety := range target.Safety {
		detail := safety.Message
		if safety.Kind == "local_changes" {
			detail = "dirty; " + detail
		}
		details = append(details, detail)
	}
	if len(details) > 0 {
		description += " (" + strings.Join(details, "; ") + ")"
	}
	return description
}

func (Source) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	root, err := workspace.DefaultRoot()
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	registry, err := workspacegroup.Load(root)
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	member, managed := workspacegroup.FindMemberByPath(registry, req.Target.Path)
	if managed {
		_, err = workspace.RemoveManagedWorktree(ctx, workspace.ExecRunner{}, member, req.Force)
	} else {
		_, err = workspace.RemoveWorktree(ctx, workspace.ExecRunner{}, req.Target.Path, req.Force)
	}
	if err != nil {
		return protocol.CleanupTarget{}, err
	}
	if err := workspacegroup.RemoveMember(root, req.Target.Path); err != nil {
		return protocol.CleanupTarget{}, err
	}
	return req.Target, nil
}

func (Source) Current(ctx context.Context, cwd string) (integration.Workspace, bool, error) {
	if strings.TrimSpace(cwd) == "" {
		return integration.Workspace{}, false, nil
	}
	path, err := workspace.ExecRunner{}.Run(ctx, cwd, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return integration.Workspace{}, false, nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return integration.Workspace{}, false, nil
	}
	branch, _ := workspace.ExecRunner{}.Run(ctx, path, "git", "branch", "--show-current")
	return integration.Workspace{Repo: path, Path: path, Branch: strings.TrimSpace(branch)}, true, nil
}

func mainWorkingTree(ctx context.Context, path string) (bool, error) {
	gitDir, err := gitOutput(ctx, path, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return false, err
	}
	return cleanPhysicalPath(gitDir) == filepath.Join(cleanPhysicalPath(path), ".git"), nil
}

func cleanPhysicalPath(path string) string {
	path = strings.TrimSpace(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.CleanupProvider = Source{}
var _ integration.WorkspaceProvider = Source{}
