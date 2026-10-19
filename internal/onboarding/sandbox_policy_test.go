package onboarding

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"radar/internal/config"
)

const uninitializedPolicyOutput = "error: global network policy has not been initialized\n  try: sbx policy init <allow-all|balanced|deny-all>\n"

func TestSandboxPolicyEscapeAbortsWithoutMutationOrFallback(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	ui.cancelAt = sandboxPolicyQuestion
	base := sys.sandboxFn
	sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "policy" {
			if !reflect.DeepEqual(args, []string{"policy", "ls"}) {
				t.Fatalf("unexpected mutation: %v", args)
			}
			return uninitializedPolicyOutput, errors.New("exit status 1")
		}
		return base(ctx, args...)
	}
	if err := w.run(); !errors.Is(err, ErrAborted) {
		t.Fatal(err)
	}
	for _, title := range ui.titles {
		if strings.Contains(title, "Continue setup") || strings.Contains(title, "Retry SBX") || strings.Contains(title, "Prepare and test") {
			t.Fatal("cancellation continued setup:", title)
		}
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("cancel saved config")
	}
}

func TestSandboxPolicyInitializationIsReportedEvenWhenSaveIsCancelled(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	ui.inputs = append([]string{"deny-all"}, ui.inputs...)
	ui.decisions = []bool{true, false, false, false, false} // prepare; skip integrations; cancel save
	base := sys.sandboxFn
	initialized := false
	sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "policy" {
			switch strings.Join(args, " ") {
			case "policy ls":
				if !initialized {
					return uninitializedPolicyOutput, errors.New("exit status 1")
				}
			case "policy init deny-all":
				initialized = true
			default:
				t.Fatalf("unexpected policy change: %v", args)
			}
		}
		if args[0] == "create" && !initialized {
			t.Fatal("creation preceded policy initialization")
		}
		return base(ctx, args...)
	}
	if err := w.run(); !errors.Is(err, ErrAborted) {
		t.Fatal(err)
	}
	if ui.reviewed == nil || !strings.Contains(fmt.Sprint(ui.reviewed.completed), "SBX global network policy initialized: deny-all") {
		t.Fatal("policy change absent from review")
	}
	if !strings.Contains(ui.transcript.String(), "Cancelling will not undo these") {
		t.Fatal("missing side-effect warning")
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("cancel saved config")
	}
}

func TestSandboxPolicyDeclineCanExplicitlyDisableSBX(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	ui.inputs = append([]string{"skip"}, ui.inputs...)
	ui.decisions = []bool{false, true, false, false, false, true} // no retry; disable; skip integrations; save
	base := sys.sandboxFn
	sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "policy" {
			if !reflect.DeepEqual(args, []string{"policy", "ls"}) {
				t.Fatalf("declining mutated policy: %v", args)
			}
			return uninitializedPolicyOutput, errors.New("exit status 1")
		}
		if args[0] == "create" {
			t.Fatal("declining created sandbox")
		}
		return base(ctx, args...)
	}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SBX.Enabled == nil || *cfg.SBX.Enabled {
		t.Fatal("explicit SBX opt-out was not saved")
	}
}
