package workspace

import (
	"context"

	"radar/internal/integration/tmux/sessionlease"
)

// Navigation must keep the selected incarnation across readiness/registration
// checks and dispatch by its numeric ID, never resolve the reusable name again.
// The caller owns admission until the entire activation returns.
func switchLiveSession(ctx context.Context, runner Runner, cwd string, admission *sessionlease.Set, live sessionlease.Identity) error {
	current, err := admission.Admit(ctx, runner, sessionlease.Request{Selector: live.ID, Name: live.Name, Expected: live.Key()})
	if err != nil {
		return err
	}
	_, err = runner.Run(ctx, cwd, "tmux", "switch-client", "-t", current.ID)
	return err
}
