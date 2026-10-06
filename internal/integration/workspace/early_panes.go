package workspace

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	sessionlayout "radar/internal/integration/tmux/layout"
)

const workspacePaneGateOption = "@radar_workspace_gate"

// createEarlyTmuxWorkspace builds the normal layout, but only the Pi pane may
// execute its command before readiness. The constructor owns cleanup until it
// returns; after that, pane gates are recoverable from the tmux server alone.
func createEarlyTmuxWorkspace(ctx context.Context, runner Runner, cwd, path, sessionName string, cfg sessionlayout.Config, piArgsText string) error {
	return createTmuxWorkspaceWithGates(ctx, runner, cwd, path, sessionName, cfg, piArgsText, nil, true)
}

func workspacePaneCommand(command, piArgsText string, early bool) (string, string) {
	if !early {
		return expandPiArgs(command, piArgsText), ""
	}
	if strings.Contains(command, sessionlayout.PiArgsPlaceholder) {
		// Preserve the prerequisite error in the Pi pane, rather than losing it
		// when an incompatible launch exits. A normal successful exit is unchanged.
		return "tmux set-option -p -t \"$TMUX_PANE\" remain-on-exit failed || exit\n" + expandPiArgs(command, piArgsText), ""
	}
	gate := "radar-pane-" + rand.Text()
	// Keep tmux's original shell and working directory, and do not parse or
	// re-quote the user's program. wait-for remembers a signal sent before the
	// waiter starts. The pane clears its option before executing the program,
	// so recovery never restarts commands, including commands that have exited.
	return "tmux wait-for " + shellQuote(gate) + " || exit\n" +
		"tmux set-option -p -u -t \"$TMUX_PANE\" " + workspacePaneGateOption + " || exit\n" + command, gate
}

// releaseWorkspacePanes only signals pending gates. The waiting shells consume
// the signals and clear their own options; another release is harmless even if
// it races that consumption or a previous creator was interrupted mid-release.
func releaseWorkspacePanes(ctx context.Context, runner Runner, path, sessionName string) error {
	output, err := runner.Run(ctx, path, "tmux", "list-panes", "-s", "-t", sessionName,
		"-f", "#{!=:#{"+workspacePaneGateOption+"},}", "-F", "#{pane_id} #{"+workspacePaneGateOption+"}")
	if err != nil {
		return &workspacePaneError{err: err}
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "%") || !strings.HasPrefix(fields[1], "radar-pane-") {
			return &workspacePaneError{err: fmt.Errorf("unexpected tmux pane gate output %q", line)}
		}
		if _, err := runner.Run(ctx, path, "tmux", "wait-for", "-S", fields[1]); err != nil {
			return &workspacePaneError{err: fmt.Errorf("signal pane %s: %w", fields[0], err)}
		}
	}
	return nil
}

type workspacePaneError struct{ err error }

func (e *workspacePaneError) Error() string { return "release workspace panes: " + e.err.Error() }
func (e *workspacePaneError) Unwrap() error { return e.err }

func isWorkspacePaneError(err error) bool {
	var paneErr *workspacePaneError
	return errors.As(err, &paneErr)
}
