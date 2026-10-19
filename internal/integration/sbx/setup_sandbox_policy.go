package sbx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"radar/internal/setup"
)

const sandboxPolicyQuestion = "Initialize SBX's global network policy?"

func (w *setupWizard) prepareSandboxPolicy(ctx context.Context) error {
	w.ui.Print("Checking SBX global network policy…\n")
	output, err := w.sbxOutput(ctx, 15*time.Second, "policy", "ls")
	if err == nil {
		w.ui.Print("✓ Existing SBX network policy preserved\n")
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Only SBX's explicit uninitialized-policy diagnostic permits initialization.
	// Authentication, governance and other failures must not trigger a policy change.
	if !strings.Contains(output, "error: global network policy has not been initialized") {
		return fmt.Errorf("cannot check SBX global network policy; inspect `sbx policy ls` on the host and retry (diagnostics withheld)")
	}
	w.ui.Print("SBX requires an initial network policy before starting its first sandbox.\nThis is global to all local SBX sandboxes, not just Radar; kit and per-sandbox rules apply on top.\nChoosing a policy runs `sbx policy init <choice>` immediately. Cancelling setup later does not undo it.\n")
	policy, err := w.ui.Input(setup.Question{
		Title:   sandboxPolicyQuestion,
		Initial: "balanced",
		Choices: []setup.Choice{
			{Value: "balanced", Label: "balanced — typical development traffic, including AI services and package registries (recommended)"},
			{Value: "deny-all", Label: "deny-all — block outbound traffic unless explicitly allowed"},
			{Value: "allow-all", Label: "allow-all — allow all outbound traffic"},
			{Value: "skip", Label: "Not now — leave the policy uninitialized"},
		},
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch policy {
	case "balanced", "deny-all", "allow-all":
	case "skip":
		return fmt.Errorf("SBX network policy initialization was declined; initialize it with `sbx policy init` on the host or continue setup without SBX")
	default:
		return fmt.Errorf("invalid SBX network policy choice")
	}
	// Local `policy init` is one-time-only: it refuses an existing policy, even
	// if another process initialized it while the prompt was open. Never reset it.
	w.ui.Print("Running `sbx policy init %s`…\n", policy)
	if _, err := w.sbxOutput(ctx, 15*time.Second, "policy", "init", policy); err != nil {
		return fmt.Errorf("SBX network policy initialization failed or timed out; check `sbx policy ls` on the host before retrying (diagnostics withheld)")
	}
	w.completed("SBX global network policy initialized: " + policy)
	if _, err := w.sbxOutput(ctx, 15*time.Second, "policy", "ls"); err != nil {
		return fmt.Errorf("SBX network policy initialization completed but verification failed; check `sbx policy ls` on the host and retry (diagnostics withheld)")
	}
	w.ui.Print("✓ SBX global network policy initialized: %s\n", policy)
	return nil
}
