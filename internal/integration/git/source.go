package git

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/tmux"
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

func (Source) PreviewDelete(ctx context.Context, req integration.DeletePreviewRequest) (protocol.DeletePreview, bool, error) {
	ref, ok := deleteWorktreeRef(req.Task, req.Current)
	if !ok {
		return protocol.DeletePreview{}, false, nil
	}
	mainWorkingTree, err := mainWorkingTree(ctx, ref.Path)
	if err != nil {
		return protocol.DeletePreview{}, true, err
	}
	if mainWorkingTree {
		return protocol.DeletePreview{}, true, fmt.Errorf("main working tree cannot be deleted from Radar")
	}
	status, err := gitOutput(ctx, ref.Path, "status", "--porcelain")
	if err != nil {
		return protocol.DeletePreview{}, true, err
	}
	dirty := strings.TrimSpace(status) != ""
	sandboxName := matchingSandboxName(req.Task, ref.Path)
	preview := protocol.DeletePreview{
		TaskID:         req.Task.ID,
		SourceRefID:    ref.ID,
		Source:         "git",
		Kind:           "worktree",
		Title:          ref.Title,
		Path:           ref.Path,
		Branch:         ref.Branch,
		SessionName:    tmux.SessionTarget(req.Task),
		SandboxName:    sandboxName,
		Dirty:          dirty,
		TargetLabel:    "workspace",
		ConfirmTitle:   "Delete workspace?",
		Warning:        "This will remove the git worktree.",
		SuccessMessage: "Deleted " + ref.Path,
	}
	if sandboxName != "" && !dirty {
		preview.Warning = "This will remove the git worktree and sbx sandbox."
		if preview.SessionName != "" {
			preview.Warning = "This will remove the git worktree, tmux session, and sbx sandbox."
		}
	}
	if dirty {
		preview.ConfirmTitle = "Delete dirty workspace?"
		preview.Warning = "This worktree has uncommitted changes. Deleting will permanently discard them."
		if sandboxName != "" {
			preview.Warning = "This worktree has uncommitted changes. Deleting will permanently discard them and remove the sbx sandbox."
		}
	}
	return preview, true, nil
}

func (Source) Delete(ctx context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error) {
	cfg, _ := config.Load()
	deleted, err := workspace.Delete(ctx, workspace.ExecRunner{}, preview.Path, preview.SessionName, true, preview.SandboxName, cfg.Sandbox != nil)
	if err != nil {
		return protocol.DeleteResult{}, err
	}
	return protocol.DeleteResult{
		SourceRefID: preview.SourceRefID,
		Source:      "git",
		Kind:        "worktree",
		Title:       preview.Title,
		Path:        deleted.Path,
		SessionName: deleted.SessionName,
		SandboxName: deleted.SandboxName,
	}, nil
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

func (Source) DeleteWorkspace(ctx context.Context, req integration.DeleteWorkspaceRequest) (integration.Workspace, error) {
	deleted, err := workspace.Delete(ctx, workspace.ExecRunner{}, req.Path, req.SessionName, req.Force, "", req.Sandbox)
	if err != nil {
		return integration.Workspace{}, err
	}
	return integrationWorkspace(deleted), nil
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

func deleteWorktreeRef(task protocol.Task, current protocol.CurrentContext) (protocol.SourceRef, bool) {
	for _, ref := range task.SourceRefs {
		if ref.Source != "git" || ref.Kind != "worktree" || ref.Path == "" {
			continue
		}
		if current.Empty() || currentPathMatches(ref.Path, current) {
			return ref, true
		}
	}
	return protocol.SourceRef{}, false
}

func matchingSandboxName(task protocol.Task, worktreePath string) string {
	for _, ref := range task.SourceRefs {
		if ref.Source != "sbx" || ref.Kind != "sandbox" || !samePath(ref.Path, worktreePath) {
			continue
		}
		name := strings.TrimSpace(ref.Title)
		if name != "" {
			return name
		}
		return strings.TrimPrefix(ref.ID, "sbx:sandbox:")
	}
	return ""
}

func currentPathMatches(refPath string, current protocol.CurrentContext) bool {
	refPath = filepath.Clean(refPath)
	return samePath(current.Worktree, refPath) || sameOrDescendant(current.CWD, refPath)
}

func samePath(left string, right string) bool {
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func sameOrDescendant(path string, root string) bool {
	if path == "" || root == "" {
		return false
	}
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.DeleteProvider = Source{}
var _ integration.WorkspaceProvider = Source{}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.DeleteProvider = Source{}
