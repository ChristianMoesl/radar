package sbx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"radar/internal/integration"
	"radar/internal/protocol"
	"radar/internal/taskrefs"
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

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	status := SourceStatus(ctx)
	return integration.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	sourceRefs, status := FetchSandboxes(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("sbx sandbox collection failed", "detail", status.Detail)
		return integration.CollectResult{Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress)}
	}
	return integration.CollectResult{Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress), Complete: status.Status == "ok"}
}

func (Source) PreviewDelete(ctx context.Context, req integration.DeletePreviewRequest) (protocol.DeletePreview, bool, error) {
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

func (Source) Actions(ctx context.Context, req integration.ActionRequest) []integration.Action {
	_ = ctx
	if !IsSandboxRef(req.Ref) {
		return nil
	}
	return []integration.Action{{
		PreferredKey: "s",
		Source:       "Docker sbx",
		Label:        req.Label,
		Detail:       "sbx run --name " + SandboxName(req.Ref),
		ID:           OpenShellAction,
		Ref:          req.Ref,
	}}
}

func (Source) RunAction(ctx context.Context, req integration.RunActionRequest) (integration.ActionResult, error) {
	if req.ActionID != OpenShellAction {
		return integration.ActionResult{}, fmt.Errorf("unknown sbx action: %s", req.ActionID)
	}
	result, err := OpenShell(ctx, workspace.ExecRunner{}, req.Ref, OpenShellOptions{
		SessionTarget: taskrefs.SessionTarget(req.Task),
		SwitchClient:  req.SwitchClient,
	})
	if err != nil {
		return integration.ActionResult{}, err
	}
	message := "Opened sbx sandbox in " + result.SessionName
	if result.CreatedSession {
		message = "Created " + result.SessionName + " and opened sbx sandbox"
	}
	return integration.ActionResult{Message: message, Refresh: true, Quit: req.SwitchClient}, nil
}

func deleteSandbox(ctx context.Context, runner workspace.Runner, preview protocol.DeletePreview) (protocol.DeleteResult, error) {
	name := strings.TrimSpace(preview.Title)
	if name == "" {
		return protocol.DeleteResult{}, fmt.Errorf("sbx sandbox name is required")
	}
	if _, err := runner.Run(ctx, "", "sbx", "rm", "--force", name); err != nil {
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

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.DeleteProvider = Source{}
var _ integration.ActionProvider = Source{}
var _ integration.RuntimeProvider = Source{}
