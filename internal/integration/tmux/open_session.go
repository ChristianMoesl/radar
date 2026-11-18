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

// Keep the whole command-opening handoff admitted. Splitting existence, window
// creation and switching into separate provider calls lets cleanup enter their
// gaps or lets a later name lookup silently select a replacement runtime.
func openSession(ctx context.Context, runner workspace.Runner, req integration.OpenSessionRequest) (integration.Session, error) {
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
	window := req.Window
	if window == "" {
		window = "pi"
	}
	command := req.Command
	if command == "" {
		command = "pi"
	}
	created := identity.ID == ""
	if created {
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
	} else {
		args := []string{"new-window", "-t", identity.ID + ":", "-n", window}
		if req.Path != "" {
			args = append(args, "-c", req.Path)
		}
		args = append(args, command)
		if _, err := runner.Run(ctx, "", "tmux", args...); err != nil {
			return integration.Session{}, err
		}
	}
	if req.Switch {
		// Revalidate the original incarnation, not the reusable caller name,
		// after window/session creation and before the final native handoff.
		identity, err = admission.Admit(ctx, runner, sessionlease.Request{Selector: identity.ID, Name: identity.Name, Expected: identity.Key()})
		if err != nil {
			return integration.Session{}, err
		}
		if _, err := runner.Run(ctx, "", "tmux", "switch-client", "-t", identity.ID); err != nil {
			return integration.Session{}, err
		}
	}
	return integration.Session{Name: identity.Name, ID: identity.ID, Path: req.Path, Created: created}, nil
}
