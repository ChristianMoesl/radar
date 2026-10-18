package onboarding

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/config"
)

const uninitializedPolicyOutput = "error: global network policy has not been initialized\n  try: sbx policy init <allow-all|balanced|deny-all>\n"

func TestSandboxPolicyInitializesOnlyTheSelectedPolicy(t *testing.T) {
	for _, policy := range []string{"balanced", "deny-all", "allow-all"} {
		t.Run(policy, func(t *testing.T) {
			w, ui, sys, _ := sandboxFixture(t)
			ui.inputs = []string{policy}
			initialized := false
			sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("unbounded policy call")
				}
				switch strings.Join(args, " ") {
				case "policy ls":
					if !initialized {
						return uninitializedPolicyOutput, errors.New("exit status 1")
					}
				case "policy init " + policy:
					if initialized {
						t.Fatal("existing policy was initialized again")
					}
					initialized = true
				default:
					t.Fatalf("unexpected call: %v", args)
				}
				return "PRIVATE_PROVIDER_OUTPUT", nil
			}
			if err := w.prepareSandboxPolicy(context.Background()); err != nil {
				t.Fatal(err)
			}
			// Retrying setup after a later failure must preserve the new policy.
			if err := w.prepareSandboxPolicy(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := []string{"sbx policy ls", "sbx policy init " + policy, "sbx policy ls", "sbx policy ls"}
			if !reflect.DeepEqual(sys.calls, want) {
				t.Fatal(sys.calls)
			}
			if len(ui.questions) != 1 || ui.questions[0].initial != "balanced" {
				t.Fatal(ui.questions)
			}
			var values []string
			for _, option := range ui.questions[0].choices {
				values = append(values, option.value)
			}
			if !reflect.DeepEqual(values, []string{"balanced", "deny-all", "allow-all", "skip"}) {
				t.Fatal(values)
			}
			if !reflect.DeepEqual(w.completed, []string{"SBX global network policy initialized: " + policy}) {
				t.Fatal(w.completed)
			}
			transcript := ui.transcript.String()
			for _, warning := range []string{"global to all local SBX sandboxes", "Cancelling setup later does not undo it"} {
				if !strings.Contains(transcript, warning) || strings.Index(transcript, warning) > strings.Index(transcript, sandboxPolicyQuestion) {
					t.Fatalf("missing warning before selection: %s", transcript)
				}
			}
			if strings.Contains(transcript, "PRIVATE_PROVIDER_OUTPUT") {
				t.Fatal("provider output leaked")
			}
		})
	}
}

func TestSandboxPolicyPreservesExistingPolicyWithoutPrompting(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	if err := w.prepareSandboxPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sys.calls, []string{"sbx policy ls"}) || len(ui.questions) != 0 || len(w.completed) != 0 {
		t.Fatalf("calls=%v questions=%v completed=%v", sys.calls, ui.questions, w.completed)
	}
}

func TestSandboxPolicyFailuresBlockCreationAndHideDiagnostics(t *testing.T) {
	for _, failure := range []string{"inspection", "authentication", "inspection timeout", "init", "init timeout", "concurrent initialization", "verification", "still uninitialized", "decline", "invalid choice"} {
		t.Run(failure, func(t *testing.T) {
			w, ui, sys, _ := sandboxFixture(t)
			ui.inputs = []string{"balanced"}
			if failure == "decline" {
				ui.inputs = []string{"skip"}
			}
			if failure == "invalid choice" {
				ui.inputs = []string{"unexpected"}
			}
			base := sys.sandboxFn
			checks, inits := 0, 0
			sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
				if args[0] == "create" {
					t.Fatal("creation ran after a policy failure")
				}
				if args[0] != "policy" {
					return base(ctx, args...)
				}
				switch args[1] {
				case "ls":
					checks++
					if failure == "inspection" || failure == "verification" && checks > 1 {
						return "PRIVATE_SECRET runtime offline", errors.New("PRIVATE_SECRET")
					}
					if failure == "authentication" {
						return "PRIVATE_SECRET run sbx login", errors.New("PRIVATE_SECRET")
					}
					if failure == "inspection timeout" {
						return "PRIVATE_SECRET", context.DeadlineExceeded
					}
					return uninitializedPolicyOutput, errors.New("PRIVATE_SECRET")
				case "init":
					inits++
					if failure == "init" || failure == "concurrent initialization" {
						return "PRIVATE_SECRET policy already initialized or unavailable", errors.New("PRIVATE_SECRET")
					}
					if failure == "init timeout" {
						return "PRIVATE_SECRET", context.DeadlineExceeded
					}
					return "PRIVATE_SECRET", nil
				default:
					t.Fatalf("unexpected policy mutation: %v", args)
					return "", nil
				}
			}
			err := w.prepareSandbox(context.Background(), config.Default().SBX, nil)
			if err == nil {
				t.Fatal("policy failure accepted")
			}
			if strings.Contains(err.Error()+ui.transcript.String(), "PRIVATE_SECRET") {
				t.Fatal("private diagnostic leaked")
			}
			wantInits := 1
			switch failure {
			case "inspection", "authentication", "inspection timeout", "decline", "invalid choice":
				wantInits = 0
			}
			if inits != wantInits {
				t.Fatalf("init calls: %d want %d", inits, wantInits)
			}
			if strings.HasPrefix(failure, "inspection") || failure == "authentication" {
				if len(ui.questions) != 0 {
					t.Fatal("unrelated error offered policy initialization")
				}
			}
			if (failure == "verification" || failure == "still uninitialized") && len(w.completed) != 1 {
				t.Fatal("successful init side effect not recorded before verification failed")
			}
		})
	}
}

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
