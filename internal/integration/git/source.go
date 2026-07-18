package git

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/workspace"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "git"
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
	source_refs, status := FetchWorktrees(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("git worktree collection failed", "detail", status.Detail)
		return integration.CollectResult{Observations: integration.ObserveRefs(source_refs, integration.SignalInProgress)}
	}
	return integration.CollectResult{Observations: integration.ObserveRefs(source_refs, integration.SignalInProgress), Complete: status.Status == "ok"}
}

func (Source) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	targets := make([]protocol.CleanupTarget, 0)
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
		targets = append(targets, protocol.CleanupTarget{
			SourceRefID: ref.ID,
			Source:      "git",
			Kind:        "worktree",
			Title:       ref.Title,
			Path:        ref.Path,
			Branch:      ref.Branch,
			Dirty:       strings.TrimSpace(status) != "",
		})
	}
	return targets, nil
}

func (Source) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if _, err := workspace.RemoveWorktree(ctx, workspace.ExecRunner{}, req.Target.Path, req.Force); err != nil {
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
	return integration.Workspace{Path: path}, true, nil
}

func (Source) Create(ctx context.Context, req integration.CreateWorkspaceRequest) (integration.Workspace, error) {
	created, err := workspace.Create(ctx, workspace.ExecRunner{}, workspace.CreateOptions{
		Repo:            req.Repo,
		Name:            req.Name,
		Branch:          req.Branch,
		Base:            req.Base,
		Path:            req.Path,
		SessionName:     req.SessionName,
		WorkspaceRoot:   req.WorkspaceRoot,
		Model:           req.Model,
		Thinking:        req.Thinking,
		Sandbox:         req.Sandbox,
		SandboxTemplate: req.SandboxTemplate,
		Switch:          req.Switch,
		ForkPiSession:   req.ForkPiSession,
	})
	if err != nil {
		return integration.Workspace{}, err
	}
	return integrationWorkspace(created), nil
}

func integrationWorkspace(workspace workspace.Workspace) integration.Workspace {
	return integration.Workspace{
		Name:        workspace.Name,
		Branch:      workspace.Branch,
		Base:        workspace.Base,
		Repo:        workspace.Repo,
		Path:        workspace.Path,
		SessionName: workspace.SessionName,
		SandboxName: workspace.SandboxName,
	}
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
