package tmux

import (
	"context"
	"fmt"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/operationlock"
	"radar/internal/sessionlease"
)

func switchSession(ctx context.Context, runner workspace.Runner, target integration.SessionTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	selector := firstNonEmpty(target.ID, target.Name)
	if selector == "" {
		return fmt.Errorf("tmux session target is required")
	}
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		return err
	}
	defer releaseOperation()
	admission := sessionlease.New(false)
	defer admission.Close()
	identity, err := admission.Admit(ctx, runner, sessionlease.Request{Selector: selector})
	if err != nil {
		return err
	}
	_, err = runner.Run(ctx, "", "tmux", "switch-client", "-t", identity.ID)
	return err
}
