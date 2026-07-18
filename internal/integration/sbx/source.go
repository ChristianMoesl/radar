package sbx

import (
	"context"
	"fmt"
	"log/slog"
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
	result := integration.CollectResult{
		Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress),
		Complete:     status.Status == "ok",
		SourceStatus: &status,
	}
	if status.Status == "error" {
		req.Logger.Warn("sbx sandbox collection failed", "detail", status.Detail)
	}
	return result
}

func (Source) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	_ = ctx
	targets := make([]protocol.CleanupTarget, 0)
	for _, ref := range req.Task.SourceRefs {
		if !IsSandboxRef(ref) {
			continue
		}
		name := SandboxName(ref)
		if name == "" {
			return nil, fmt.Errorf("sbx sandbox name is required")
		}
		targets = append(targets, protocol.CleanupTarget{
			SourceRefID: ref.ID,
			Source:      "sbx",
			Kind:        "sandbox",
			Title:       name,
			Path:        ref.Path,
			SandboxName: name,
		})
	}
	return targets, nil
}

func (Source) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	return cleanupSandbox(ctx, workspace.ExecRunner{}, req.Target)
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
	if req.Multiplexer == nil {
		return integration.ActionResult{}, fmt.Errorf("open sbx sandbox requires an active multiplexer provider")
	}
	result, err := openShellWithMultiplexer(ctx, req.Multiplexer, req.Ref, OpenShellOptions{
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

func openShellWithMultiplexer(ctx context.Context, multiplexer integration.MultiplexerProvider, ref protocol.SourceRef, options OpenShellOptions) (OpenShellResult, error) {
	name := SandboxName(ref)
	if name == "" {
		return OpenShellResult{}, fmt.Errorf("sbx sandbox name is required")
	}
	runner := workspace.ExecRunner{}
	if err := runner.LookPath("sbx"); err != nil {
		return OpenShellResult{}, fmt.Errorf("open sbx sandbox requires %q: %w", "sbx", err)
	}
	sessionName := strings.TrimSpace(options.SessionTarget)
	if sessionName == "" {
		sessionName = SandboxSessionName(ref)
	}
	if sessionName == "" {
		return OpenShellResult{}, fmt.Errorf("multiplexer session name is required")
	}
	command := "sbx run --name " + shellQuote(name)
	path := strings.TrimSpace(ref.Path)
	result := OpenShellResult{SessionName: sessionName}
	session, err := multiplexer.EnsureSession(ctx, integration.EnsureSessionRequest{Name: sessionName, Path: path, FirstWindow: "sbx", FirstCommand: command})
	if err != nil {
		return OpenShellResult{}, err
	}
	result.CreatedSession = session.Created
	if !session.Created {
		if err := multiplexer.OpenWindow(ctx, integration.OpenWindowRequest{SessionName: sessionName, Name: "sbx", Path: path, Command: command}); err != nil {
			return OpenShellResult{}, err
		}
	}
	if options.SwitchClient {
		if err := multiplexer.Switch(ctx, integration.SessionTarget{Name: sessionName}); err != nil {
			return OpenShellResult{}, err
		}
	}
	return result, nil
}

func cleanupSandbox(ctx context.Context, runner workspace.Runner, target protocol.CleanupTarget) (protocol.CleanupTarget, error) {
	name := strings.TrimSpace(target.SandboxName)
	if name == "" {
		return protocol.CleanupTarget{}, fmt.Errorf("sbx sandbox name is required")
	}
	if _, err := runner.Run(ctx, "", "sbx", "rm", "--force", name); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return target, nil
		}
		return protocol.CleanupTarget{}, err
	}
	return target, nil
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.CleanupProvider = Source{}
var _ integration.ActionProvider = Source{}
var _ integration.RuntimeProvider = Source{}
