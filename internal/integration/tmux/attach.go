package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"radar/internal/integration"
	"radar/internal/integration/tmux/sessionlease"
	"radar/internal/integration/workspace"
	"radar/internal/operationlock"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// AttachCommand lets the foreground UI release its terminal before attaching.
// Workspace creation owns starting the session; browsing never starts tmux.
func (Source) AttachCommand(ctx context.Context, target integration.SessionTarget) (integration.SessionAttachment, error) {
	return prepareAttach(ctx, workspace.ExecRunner{}, target)
}

func prepareAttach(ctx context.Context, runner workspace.Runner, target integration.SessionTarget) (integration.SessionAttachment, error) {
	if err := ctx.Err(); err != nil {
		return integration.SessionAttachment{}, err
	}
	selector := firstNonEmpty(target.ID, target.Name)
	if selector == "" {
		return integration.SessionAttachment{}, fmt.Errorf("tmux session target is required")
	}
	releaseOperation, err := operationlock.Acquire(false)
	if err != nil {
		return integration.SessionAttachment{}, err
	}
	admission := sessionlease.New(false)
	identity, err := admission.Admit(ctx, runner, sessionlease.Request{Selector: selector})
	if err != nil {
		releaseOperation()
		return integration.SessionAttachment{}, err
	}
	var once sync.Once
	return integration.SessionAttachment{
		Command: exec.CommandContext(ctx, "tmux", "attach-session", "-t", identity.ID),
		Validate: func() error {
			_, err := admission.Admit(ctx, runner, sessionlease.Request{Selector: identity.ID, Expected: identity.Key()})
			return err
		},
		Release: func() { once.Do(func() { admission.Close(); releaseOperation() }) },
	}, nil
}
func SupportsPopup(version string) bool {
	match := regexp.MustCompile(`^tmux (?:next-)?([0-9]+)\.([0-9]+)`).FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return major > 3 || major == 3 && minor >= 2
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
