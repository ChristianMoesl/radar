package git

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/ingestion"
	"radar/internal/protocol"
	"radar/internal/tmux"
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

func (Source) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	return ingestion.StatusResult{
		Status: protocol.SourceStatus{Name: "git", Status: "ok"},
		CanRun: true,
	}
}

func (Source) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	source_refs, status := FetchWorktrees(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("git worktree collection failed", "detail", status.Detail)
		return ingestion.Result{SourceRefs: source_refs}
	}
	return ingestion.Result{SourceRefs: source_refs, Complete: status.Status == "ok"}
}

func (Source) PreviewDelete(ctx context.Context, req ingestion.DeletePreviewRequest) (protocol.DeletePreview, bool, error) {
	ref, ok := deleteWorktreeRef(req.Task, req.Current)
	if !ok {
		return protocol.DeletePreview{}, false, nil
	}
	status, err := gitOutput(ctx, ref.Path, "status", "--porcelain")
	if err != nil {
		return protocol.DeletePreview{}, true, err
	}
	return protocol.DeletePreview{
		TaskID:      req.Task.ID,
		SourceRefID: ref.ID,
		Source:      "git",
		Kind:        "worktree",
		Title:       ref.Title,
		Path:        ref.Path,
		Branch:      ref.Branch,
		SessionName: tmux.SessionTarget(req.Task),
		Dirty:       strings.TrimSpace(status) != "",
	}, true, nil
}

func (Source) Delete(ctx context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error) {
	deleted, err := workspace.Delete(ctx, workspace.ExecRunner{}, preview.Path, preview.SessionName, true)
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
	}, nil
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
