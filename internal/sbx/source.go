package sbx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/ingestion"
	"radar/internal/protocol"
	"radar/internal/workspace"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "sbx"
}

func (Source) Local() bool {
	return true
}

func (Source) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status := SourceStatus(ctx)
	return ingestion.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Source) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	sourceRefs, status := FetchSandboxes(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("sbx sandbox collection failed", "detail", status.Detail)
		return ingestion.Result{SourceRefs: sourceRefs}
	}
	return ingestion.Result{SourceRefs: sourceRefs, Complete: status.Status == "ok"}
}

func (Source) PreviewDelete(ctx context.Context, req ingestion.DeletePreviewRequest) (protocol.DeletePreview, bool, error) {
	_ = ctx
	for _, ref := range req.Task.SourceRefs {
		if !IsSandboxRef(ref) {
			continue
		}
		if !req.Current.Empty() && !currentPathMatches(ref.Path, req.Current) {
			continue
		}
		name := SandboxName(ref)
		if name == "" {
			return protocol.DeletePreview{}, true, fmt.Errorf("sbx sandbox name is required")
		}
		return protocol.DeletePreview{
			TaskID:         req.Task.ID,
			SourceRefID:    ref.ID,
			Source:         "sbx",
			Kind:           "sandbox",
			Title:          name,
			Path:           ref.Path,
			TargetLabel:    "sbx sandbox",
			ConfirmTitle:   "Delete sbx sandbox?",
			Warning:        "This will remove the sbx sandbox.",
			SuccessMessage: "Deleted " + name,
		}, true, nil
	}
	return protocol.DeletePreview{}, false, nil
}

func (Source) Delete(ctx context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error) {
	return deleteSandbox(ctx, workspace.ExecRunner{}, preview)
}

func deleteSandbox(ctx context.Context, runner workspace.Runner, preview protocol.DeletePreview) (protocol.DeleteResult, error) {
	name := strings.TrimSpace(preview.Title)
	if name == "" {
		return protocol.DeleteResult{}, fmt.Errorf("sbx sandbox name is required")
	}
	if _, err := runner.Run(ctx, preview.Path, "sbx", "rm", name); err != nil {
		return protocol.DeleteResult{}, err
	}
	return protocol.DeleteResult{
		SourceRefID: preview.SourceRefID,
		Source:      "sbx",
		Kind:        "sandbox",
		Title:       name,
		Path:        preview.Path,
	}, nil
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

var _ ingestion.Source = Source{}
var _ ingestion.LocalSource = Source{}
var _ ingestion.StatusReporter = Source{}
var _ ingestion.Deleter = Source{}
