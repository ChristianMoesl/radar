package tmux

import (
	"context"
	"fmt"
	"strings"

	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	"radar/internal/operationlock"
)

// EnsureSession and OpenWindow are used by sandbox-shell navigation as well as
// workspace navigation. Paths alone do not identify their shared tmux runtime.
func ensureSession(ctx context.Context, runner workspace.Runner, req integration.EnsureSessionRequest) (integration.Session, error) {
	if err := ctx.Err(); err != nil {
		return integration.Session{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return integration.Session{}, fmt.Errorf("tmux session name is required")
	}
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		return integration.Session{}, err
	}
	defer releaseOperation()
	admission := sessionlease.New(false)
	defer admission.Close()
	request := sessionlease.Request{Selector: "=" + req.Name, Name: req.Name, AllowMissing: true}
	if strings.HasPrefix(req.Name, "$") {
		// Collected sandbox-shell targets may be native IDs. An absent ID
		// cannot authorize recreation under a made-up reusable name.
		request = sessionlease.Request{Selector: req.Name}
	}
	identity, err := admission.Admit(ctx, runner, request)
	if err != nil {
		return integration.Session{}, err
	}
	created := identity.ID == ""
	if created {
		window := req.FirstWindow
		if window == "" {
			window = "pi"
		}
		command := req.FirstCommand
		if command == "" {
			command = "pi"
		}
		// Capture the incarnation from this creation, not a subsequent name
		// lookup that could silently select an externally replaced session.
		args := []string{"new-session", "-d", "-s", req.Name, "-n", window, "-P", "-F", "#{pid}:#{session_created}:#{session_id}"}
		if req.Path != "" {
			args = append(args, "-c", req.Path)
		}
		args = append(args, command)
		output, err := runner.Run(ctx, "", "tmux", args...)
		if err != nil {
			return integration.Session{}, err
		}
		request.Expected = "tmux:session:" + strings.TrimSpace(output)
		identity, err = admission.Admit(ctx, runner, request)
		if err != nil {
			return integration.Session{}, err
		}
		if identity.ID == "" {
			return integration.Session{}, fmt.Errorf("created tmux session no longer exists; refresh and retry")
		}
	}
	if req.Switch {
		// Revalidate the original incarnation and dispatch its pinned ID while
		// retaining both the name and runtime admission through native return.
		identity, err = admission.Admit(ctx, runner, sessionlease.Request{Selector: identity.ID, Name: identity.Name, Expected: identity.Key()})
		if err != nil {
			return integration.Session{}, err
		}
		if _, err := runner.Run(ctx, "", "tmux", "switch-client", "-t", identity.ID); err != nil {
			return integration.Session{}, err
		}
	}
	return integration.Session{Name: req.Name, ID: identity.ID, Path: req.Path, Created: created}, nil
}

func openWindow(ctx context.Context, runner workspace.Runner, req integration.OpenWindowRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(req.SessionName) == "" {
		return fmt.Errorf("tmux session name is required")
	}
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		return err
	}
	defer releaseOperation()
	admission := sessionlease.New(false)
	defer admission.Close()
	request := sessionlease.Request{Selector: "=" + req.SessionName, Name: req.SessionName}
	if strings.HasPrefix(req.SessionName, "$") {
		request = sessionlease.Request{Selector: req.SessionName}
	}
	identity, err := admission.Admit(ctx, runner, request)
	if err != nil {
		return err
	}
	args := []string{"new-window", "-t", identity.ID + ":"}
	if req.Name != "" {
		args = append(args, "-n", req.Name)
	}
	if req.Path != "" {
		args = append(args, "-c", req.Path)
	}
	if req.Command != "" {
		args = append(args, req.Command)
	}
	_, err = runner.Run(ctx, "", "tmux", args...)
	return err
}
