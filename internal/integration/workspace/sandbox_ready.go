package workspace

import (
	"context"
	"errors"
	"time"

	sbxclient "radar/internal/integration/sbx/client"
	sbxsettings "radar/internal/integration/sbx/settings"
	"radar/internal/integration/workspace/group"
)

const sandboxReadyTimeout = 60 * time.Second

// sandboxReadinessError deliberately retains only a safe context cause. Provider
// errors and output can contain argv, environment values, or other private data.
type sandboxReadinessError struct{ cause error }

func (e *sandboxReadinessError) Error() string {
	switch {
	case errors.Is(e.cause, context.DeadlineExceeded):
		return "SBX sandbox readiness check timed out; runtime retained for inspection and retry"
	case errors.Is(e.cause, context.Canceled):
		return "SBX sandbox readiness check canceled; runtime retained for inspection and retry"
	default:
		return "SBX sandbox readiness check failed; runtime retained for inspection and retry"
	}
}

func (e *sandboxReadinessError) Unwrap() error { return e.cause }

func isSandboxReadinessError(err error) bool {
	var readyErr *sandboxReadinessError
	return errors.As(err, &readyErr)
}

func sandboxRuntimeFailurePhase(err error) string {
	if isSandboxReadinessError(err) {
		return "sandbox_ready"
	}
	return "sandbox"
}

func waitForSandboxReady(ctx context.Context, runner Runner, anchor string, sandbox *workspacegroup.Sandbox) error {
	if sandbox == nil || len(sandbox.ReadyCommand) == 0 {
		return nil
	}
	if err := sbxsettings.ValidateReadyCommand(sandbox.ReadyCommand); err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, sandboxReadyTimeout)
	defer cancel()
	if err := readyCtx.Err(); err != nil {
		return &sandboxReadinessError{cause: err}
	}
	args := append([]string{"exec", "--workdir", anchor, sandbox.Name}, sandbox.ReadyCommand...)
	_, err := sbxclient.New(runner).Run(readyCtx, anchor, args...)
	// Check context even when a runner returns success at the deadline boundary.
	if readyCtx.Err() != nil {
		return &sandboxReadinessError{cause: readyCtx.Err()}
	}
	if err != nil {
		var cause error
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			cause = context.DeadlineExceeded
		case errors.Is(err, context.Canceled):
			cause = context.Canceled
		}
		return &sandboxReadinessError{cause: cause}
	}
	return nil
}
