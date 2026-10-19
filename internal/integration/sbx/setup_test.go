package sbx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"radar/internal/config"
	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

const uninitializedPolicyOutput = "error: global network policy has not been initialized\n  try: sbx policy init <allow-all|balanced|deny-all>\n"

func TestSandboxStarterDetectsConcurrentEditsAndNeverOverwrites(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			_, _, _, _ = providerFixture(t)
			path, _ := config.Path()
			cfg := config.Default().SBX
			first, err := planSandboxFiles(path, &cfg)
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := first.apply(); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := planSandboxFiles(path, &cfg)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(plan.root, "spec.yaml")
			if err := os.MkdirAll(plan.root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("user change"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := plan.apply(); err == nil {
				t.Fatal("concurrent change accepted")
			}
			got, _ := os.ReadFile(file)
			if string(got) != "user change" {
				t.Fatal("user change overwritten")
			}
		})
	}
}

func TestSandboxStarterPreservesAndHidesExistingEnvFile(t *testing.T) {
	_, _, _, _ = providerFixture(t)
	path, _ := config.Path()
	cfg := config.Default().SBX
	p, err := planSandboxFiles(path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.apply(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.EnvFile, []byte("USER_SECRET=do-not-display\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err = planSandboxFiles(path, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range p.reviewFiles() {
		if strings.Contains(file.Details, "do-not-display") || file.Action != "UNCHANGED" {
			t.Fatal(file)
		}
	}
	if err := p.apply(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg.EnvFile)
	if string(got) != "USER_SECRET=do-not-display\n" {
		t.Fatal("env rewritten")
	}
}

func TestSandboxProbeCleanupAfterCancellation(t *testing.T) {
	w, _, sys, _ := providerFixture(t)
	base := sys.SandboxFn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleaned := false
	sys.SandboxFn = func(callCtx context.Context, args ...string) (string, error) {
		if args[0] == "create" {
			cancel()
			return "", context.Canceled
		}
		if args[0] == "rm" {
			cleaned = true
			if callCtx.Err() != nil {
				t.Fatal("cleanup inherited cancelled context")
			}
		}
		return base(callCtx, args...)
	}
	cfg := config.Default().SBX
	err := w.probeSandbox(ctx, cfg, nil)
	if err == nil || !cleaned {
		t.Fatalf("err=%v cleaned=%t", err, cleaned)
	}
}

func TestSandboxSetupE2E(t *testing.T) {
	if os.Getenv("RADAR_SBX_SETUP_E2E") != "1" || runtime.GOOS != "darwin" {
		t.Skip("set RADAR_SBX_SETUP_E2E=1 on an authenticated macOS SBX host")
	}
	root, err := os.MkdirTemp("", "radar-setup-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(root)
		}
	}()
	cfg := config.Default().SBX
	plan, err := planSandboxFiles(filepath.Join(root, "config.yaml"), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-existing hook is user-owned and must run from the read-only mount.
	hooks := filepath.Join(plan.root, "startup.d")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	script := []byte("#!/bin/sh\nset -eu\ntest \"$(id -u)\" = 1000\ntest ! -w \"$PWD\"\nprintf fixture > \"$HOME/startup-fixture\"\n")
	if err := os.WriteFile(filepath.Join(hooks, "10-fixture"), script, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.ReadyCommand = []string{"sh", "-c", `sandbox-startup run && test "$(cat "$HOME/startup-fixture")" = fixture`}
	ui := &setuptest.UI{}
	w := setupWizard{ui: ui, system: setup.RealSystem{}, completed: func(effect string) { ui.Completed = append(ui.Completed, effect) }}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	err = w.probeSandbox(ctx, cfg, plan)
	t.Log(ui.Transcript.String())
	var cleanupErr *sandboxCleanupError
	if errors.As(err, &cleanupErr) {
		keep = true
		t.Logf("Fixture retained at %s until cleanup is completed", root)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("probe wrote config")
	}
	if _, err := os.Stat(filepath.Join(plan.root, "spec.yaml")); !os.IsNotExist(err) {
		t.Fatal("probe published starter before review")
	}
}

func TestSandboxSetupRequiresSBX048(t *testing.T) {
	for _, tt := range []struct {
		version   string
		supported bool
	}{
		{`{"client":{"version":"v0.46.0"}}`, false},
		{`{"client":{"version":"v0.47.9"}}`, false},
		{`{"client":{"version":"v0.48.0"}}`, true},
		{`{"client":{"version":"v0.48.1"}}`, true},
		{`{"client":{"version":"v0.49.0"}}`, false},
		{`{"client":{"version":"v1.0.0"}}`, false},
		{"unknown", false},
	} {
		t.Run(tt.version, func(t *testing.T) {
			w, _, sys, _ := providerFixture(t)
			listed := false
			sys.SandboxFn = func(_ context.Context, args ...string) (string, error) {
				switch args[0] {
				case "version":
					return tt.version, nil
				case "ls":
					listed = true
					// Stop after the version gate, without provisioning anything.
					return "", errors.New("runtime unavailable")
				default:
					t.Fatalf("unexpected command: %v", args)
					return "", nil
				}
			}
			err := w.prepareSandbox(context.Background(), config.SBXConfig{}, nil)
			if listed != tt.supported || err == nil {
				t.Fatalf("listed = %v, error = %v", listed, err)
			}
			if !tt.supported && !strings.Contains(err.Error(), "SBX 0.48.x is required") {
				t.Fatalf("missing upgrade guidance: %v", err)
			}
		})
	}
}

func TestSandboxPolicyInitializesOnlyTheSelectedPolicy(t *testing.T) {
	for _, policy := range []string{"balanced", "deny-all", "allow-all"} {
		t.Run(policy, func(t *testing.T) {
			w, ui, sys, _ := providerFixture(t)
			ui.Inputs = []string{policy}
			initialized := false
			sys.SandboxFn = func(ctx context.Context, args ...string) (string, error) {
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
			if !reflect.DeepEqual(sys.Calls, want) {
				t.Fatal(sys.Calls)
			}
			if len(ui.Questions) != 1 || ui.Questions[0].Initial != "balanced" {
				t.Fatal(ui.Questions)
			}
			var values []string
			for _, option := range ui.Questions[0].Choices {
				values = append(values, option.Value)
			}
			if !reflect.DeepEqual(values, []string{"balanced", "deny-all", "allow-all", "skip"}) {
				t.Fatal(values)
			}
			if !reflect.DeepEqual(ui.Completed, []string{"SBX global network policy initialized: " + policy}) {
				t.Fatal(ui.Completed)
			}
			transcript := ui.Transcript.String()
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
	w, ui, sys, _ := providerFixture(t)
	if err := w.prepareSandboxPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sys.Calls, []string{"sbx policy ls"}) || len(ui.Questions) != 0 || len(ui.Completed) != 0 {
		t.Fatalf("calls=%v questions=%v completed=%v", sys.Calls, ui.Questions, ui.Completed)
	}
}

func TestSandboxPolicyFailuresBlockCreationAndHideDiagnostics(t *testing.T) {
	for _, failure := range []string{"inspection", "authentication", "inspection timeout", "init", "init timeout", "concurrent initialization", "verification", "still uninitialized", "decline", "invalid choice"} {
		t.Run(failure, func(t *testing.T) {
			w, ui, sys, _ := providerFixture(t)
			ui.Inputs = []string{"balanced"}
			if failure == "decline" {
				ui.Inputs = []string{"skip"}
			}
			if failure == "invalid choice" {
				ui.Inputs = []string{"unexpected"}
			}
			base := sys.SandboxFn
			checks, inits := 0, 0
			sys.SandboxFn = func(ctx context.Context, args ...string) (string, error) {
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
			if strings.Contains(err.Error()+ui.Transcript.String(), "PRIVATE_SECRET") {
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
				if len(ui.Questions) != 0 {
					t.Fatal("unrelated error offered policy initialization")
				}
			}
			if (failure == "verification" || failure == "still uninitialized") && len(ui.Completed) != 1 {
				t.Fatal("successful init side effect not recorded before verification failed")
			}
		})
	}
}

func providerFixture(t *testing.T) (setupWizard, *setuptest.UI, *setuptest.System, string) {
	t.Helper()
	home := setuptest.Home(t)
	ui := &setuptest.UI{}
	sys := &setuptest.System{SandboxFn: healthySandbox(t)}
	w := setupWizard{ui: ui, system: sys, completed: func(effect string) { ui.Completed = append(ui.Completed, effect) }, extensions: func(context.Context, string, string) error { return nil }}
	return w, ui, sys, home
}
func healthySandbox(t *testing.T) func(context.Context, ...string) (string, error) {
	t.Helper()
	return func(ctx context.Context, args ...string) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Minute {
			t.Fatal("unbounded SBX call")
		}
		switch args[0] {
		case "version":
			return `{"client":{"version":"v0.48.0"}}`, nil
		case "ls":
			return `[]`, nil
		case "kit":
			if _, err := os.ReadFile(filepath.Join(args[2], "spec.yaml")); err != nil {
				return "", err
			}
		case "exec":
			if len(args) > 5 && args[4] == "sh" {
				anchor, token := args[len(args)-2], args[len(args)-1]
				if got, err := os.ReadFile(filepath.Join(anchor, "host-check")); err != nil || string(got) != token {
					t.Fatal("missing host fixture")
				}
				if err := os.WriteFile(filepath.Join(anchor, "sandbox-check"), []byte(token), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return "PRIVATE_PROVIDER_OUTPUT", nil
	}
}

func TestSandboxExplicitPublishedRecipeIsPreserved(t *testing.T) {
	_, _, _, _ = providerFixture(t)
	for _, key := range []string{"kit:\n    name: docker.io/christianmoesl/radar-kit:latest", "ready_command: []", "additional_mounts: []", "env_file: ''"} {
		path, _ := config.Path()
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, []byte("sbx:\n  "+key+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		draft, err := config.LoadSetupDraft()
		if err != nil {
			t.Fatal(err)
		}
		if !hasSandboxRecipe(draft) {
			t.Fatalf("explicit %s lost", key)
		}
	}
}

func TestSandboxRecipeAliasesAndMergesAreExplicit(t *testing.T) {
	_, _, _, _ = providerFixture(t)
	for _, data := range []string{
		"recipe: &recipe\n  kit: {name: custom}\nsbx: *recipe\n",
		"recipe: &recipe\n  kit: {name: custom}\nsbx:\n  <<: *recipe\n",
	} {
		path, _ := config.Path()
		_ = os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		draft, err := config.LoadSetupDraft()
		if err != nil {
			t.Fatal(err)
		}
		if !hasSandboxRecipe(draft) {
			t.Fatal("aliased recipe would be overwritten")
		}
	}
}
