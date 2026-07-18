package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
	return "tmux"
}

func (Source) Local() bool {
	return true
}

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	status := SourceStatus(ctx)
	return integration.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	sourceRefs, status := FetchSessions(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("tmux session collection failed", "detail", status.Detail)
		return integration.CollectResult{Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress)}
	}
	return integration.CollectResult{Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress), Complete: status.Status == "ok"}
}

func (Source) PreviewCleanup(ctx context.Context, req integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	_ = ctx
	targets := make([]protocol.CleanupTarget, 0)
	for _, ref := range req.Task.SourceRefs {
		if ref.Source != "tmux" || ref.Kind != "session" {
			continue
		}
		session := SessionTarget(protocol.Task{Kind: "session", SourceRefs: []protocol.SourceRef{ref}})
		if session == "" {
			return nil, fmt.Errorf("tmux session has no cleanup target")
		}
		targets = append(targets, protocol.CleanupTarget{
			SourceRefID: ref.ID,
			Source:      "tmux",
			Kind:        "session",
			Title:       ref.Title,
			Path:        ref.Path,
			SessionName: session,
		})
	}
	return targets, nil
}

func (Source) Cleanup(ctx context.Context, req integration.CleanupRequest) (protocol.CleanupTarget, error) {
	if _, err := workspace.RemoveSession(ctx, workspace.ExecRunner{}, req.Target.SessionName); err != nil {
		return protocol.CleanupTarget{}, err
	}
	return req.Target, nil
}

func (Source) Current(ctx context.Context) (integration.SessionContext, bool, error) {
	if os.Getenv("TMUX") == "" {
		return integration.SessionContext{}, false, nil
	}
	output, err := workspace.ExecRunner{}.Run(ctx, "", "tmux", "display-message", "-p", "#{session_name}\t#{session_id}\t#{pane_current_path}")
	if err != nil {
		return integration.SessionContext{}, false, err
	}
	fields := strings.Split(output, "\t")
	if len(fields) < 2 {
		return integration.SessionContext{}, false, fmt.Errorf("unexpected tmux current context output: %q", output)
	}
	current := integration.SessionContext{Name: fields[0], ID: fields[1]}
	if len(fields) > 2 {
		current.Path = fields[2]
	}
	return current, true, nil
}

func (Source) EnsureSession(ctx context.Context, req integration.EnsureSessionRequest) (integration.Session, error) {
	runner := workspace.ExecRunner{}
	if strings.TrimSpace(req.Name) == "" {
		return integration.Session{}, fmt.Errorf("tmux session name is required")
	}
	created := false
	if _, err := runner.Run(ctx, "", "tmux", "has-session", "-t", req.Name); err != nil {
		window := req.FirstWindow
		if window == "" {
			window = "pi"
		}
		command := req.FirstCommand
		if command == "" {
			command = "pi"
		}
		args := []string{"new-session", "-d", "-s", req.Name, "-n", window}
		if req.Path != "" {
			args = append(args, "-c", req.Path)
		}
		args = append(args, command)
		if _, err := runner.Run(ctx, "", "tmux", args...); err != nil {
			return integration.Session{}, err
		}
		created = true
	}
	if req.Switch {
		if err := (Source{}).Switch(ctx, integration.SessionTarget{Name: req.Name}); err != nil {
			return integration.Session{}, err
		}
	}
	return integration.Session{Name: req.Name, Path: req.Path, Created: created}, nil
}

func (Source) OpenWindow(ctx context.Context, req integration.OpenWindowRequest) error {
	if strings.TrimSpace(req.SessionName) == "" {
		return fmt.Errorf("tmux session name is required")
	}
	args := []string{"new-window", "-t", req.SessionName + ":"}
	if req.Name != "" {
		args = append(args, "-n", req.Name)
	}
	if req.Path != "" {
		args = append(args, "-c", req.Path)
	}
	if req.Command != "" {
		args = append(args, req.Command)
	}
	_, err := workspace.ExecRunner{}.Run(ctx, "", "tmux", args...)
	return err
}

func (Source) Switch(ctx context.Context, target integration.SessionTarget) error {
	tmuxTarget := firstNonEmpty(target.ID, target.Name)
	if tmuxTarget == "" {
		return fmt.Errorf("tmux session target is required")
	}
	_, err := workspace.ExecRunner{}.Run(ctx, "", "tmux", "switch-client", "-t", tmuxTarget)
	return err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var _ integration.Source = Source{}
var _ integration.LocalSource = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.CleanupProvider = Source{}
var _ integration.MultiplexerProvider = Source{}
