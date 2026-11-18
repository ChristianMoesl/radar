package tmux

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/sessionlease"
)

type navigationRunner struct {
	switchCall func(context.Context, []string) error
	switches   int
	queries    int
}

func (*navigationRunner) LookPath(string) error { return nil }
func (r *navigationRunner) Run(ctx context.Context, _ string, _ string, args ...string) (string, error) {
	if args[0] == "display-message" {
		r.queries++
		if args[4] == "#{session_name}" {
			return "shared-runtime\n", nil
		}
		return "12:34:$7\n", nil
	}
	if args[0] == "switch-client" {
		r.switches++
		if r.switchCall != nil {
			return "", r.switchCall(ctx, args)
		}
	}
	return "", nil
}

func TestDirectSwitchRejectsRuntimeCleanupAliases(t *testing.T) {
	for _, selector := range []string{"shared-runtime", "=shared-runtime", "shared-r", "shared-*", "$7"} {
		t.Run(selector, func(t *testing.T) {
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			runner := &navigationRunner{}
			cleanup := sessionlease.New(true)
			defer cleanup.Close()
			if _, err := cleanup.Admit(context.Background(), runner, sessionlease.Request{Selector: "$7"}); err != nil {
				t.Fatal(err)
			}
			err := switchSession(context.Background(), runner, integration.SessionTarget{Name: selector})
			if err == nil || !strings.Contains(err.Error(), "tmux session is in use") || runner.switches != 0 {
				t.Fatal("switch bypassed runtime admission", err, runner.switches)
			}
		})
	}
}

func TestDirectSwitchPinsIDAndHoldsAdmissionThroughEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &navigationRunner{}
			runner.switchCall = func(ctx context.Context, args []string) error {
				if len(args) != 3 || args[2] != "$7" {
					t.Errorf("native switch not pinned to admitted ID: %v", args)
				}
				cleanup := sessionlease.New(true)
				defer cleanup.Close()
				if _, err := cleanup.Admit(context.Background(), runner, sessionlease.Request{Selector: "$7"}); err == nil {
					t.Error("cleanup entered active native switch")
				}
				if outcome == "failure" {
					return errors.New("fixture native switch failed")
				}
				if outcome == "cancelled" {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			err := switchSession(ctx, runner, integration.SessionTarget{Name: "shared-r"})
			if (err != nil) != (outcome != "success") || runner.switches != 1 {
				t.Fatal(err, runner.switches)
			}
			if outcome == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			cleanup := sessionlease.New(true)
			defer cleanup.Close()
			if _, err := cleanup.Admit(context.Background(), runner, sessionlease.Request{Selector: "$7"}); err != nil {
				t.Fatal("switch leaked admission", err)
			}
		})
	}
}

func TestDirectSwitchPreCancelledHasNoNativeCalls(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &navigationRunner{}
	if err := switchSession(ctx, runner, integration.SessionTarget{Name: "shared-runtime"}); !errors.Is(err, context.Canceled) || runner.queries != 0 || runner.switches != 0 {
		t.Fatal("pre-cancelled navigation entered native runtime", err, runner)
	}
}
