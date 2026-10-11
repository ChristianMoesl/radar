package onboarding

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
	"radar/sandbox"
)

func sandboxFixture(t *testing.T) (wizard, *fakeUI, *fakeSystem, string) {
	t.Helper()
	w, ui, sys, home := fixture(t)
	ui.sandboxChoice = true
	delete(sys.missing, "sbx")
	dir := filepath.Join(home, ".pi", "agent")
	pkg := filepath.Join(dir, "npm", "node_modules", "@christianmoesl", "pi-sbx")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@christianmoesl/pi-sbx","version":"0.6.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"packages":["npm:@christianmoesl/pi-radar","npm:@christianmoesl/pi-sbx"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	sys.sandboxFn = healthySandbox(t)
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
			return "sbx version: v0.46.0 fixture", nil
		case "ls":
			return `{"sandboxes":[]}`, nil
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

func TestSandboxSetupCreatesReviewedStarterOnlyOnSave(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(fmt.Sprint(save), func(t *testing.T) {
			w, ui, sys, home := sandboxFixture(t)
			ui.decisions = []bool{true, false, false, false, save}
			root := filepath.Join(home, "config", "radar", "sandbox")
			ui.beforeConfirm = func(title string) {
				if title != "Save this configuration?" {
					return
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("starter published before review")
				}
				if ui.reviewed == nil || !strings.Contains(ui.reviewed.sandbox, "verified") {
					t.Fatal("missing readiness summary")
				}
				for _, file := range ui.reviewed.files {
					if file.path == filepath.Join(root, "spec.yaml") && file.details != sandbox.KitSpec {
						t.Fatal("wrong kit preview")
					}
				}
			}
			err := w.run()
			if !save {
				if !errors.Is(err, ErrAborted) {
					t.Fatal(err)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("cancel wrote starter")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				if cfg.SBX.Enabled == nil || !*cfg.SBX.Enabled || cfg.SBX.Kit.Name != root || cfg.SBX.Kit.Path != "" {
					t.Fatalf("config: %+v", cfg.SBX)
				}
				if !reflect.DeepEqual(cfg.SBX.ReadyCommand, []string{"sandbox-startup", "run"}) || !reflect.DeepEqual(cfg.SBX.AdditionalMounts, []string{filepath.Join(root, "startup.d") + ":ro"}) {
					t.Fatal(cfg.SBX)
				}
				for _, file := range []string{"spec.yaml", "sandbox.env", "startup.d/README.md"} {
					info, err := os.Stat(filepath.Join(root, file))
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("%s: %v", file, err)
					}
				}
				data, _ := os.ReadFile(cfg.SBX.EnvFile)
				if string(data) != "SBX_STARTUP_DIR="+filepath.Join(root, "startup.d")+"\n" {
					t.Fatal(string(data))
				}
			}
			calls := strings.Join(sys.calls, "\n")
			if !strings.Contains(calls, "sandbox-startup run") || !strings.Contains(calls, "sbx rm --force radar-setup-") {
				t.Fatal(calls)
			}
			if strings.Contains(ui.transcript.String(), "PRIVATE_PROVIDER_OUTPUT") {
				t.Fatal("provider output leaked")
			}
			if !ui.defaults[sandboxQuestion] {
				t.Fatal("new macOS setup must recommend SBX")
			}
		})
	}
}

func TestSandboxSetupFailureCanExplicitlyDisable(t *testing.T) {
	for _, failure := range []string{"missing", "old version", "auth", "null list", "extension", "create", "ready", "sharing", "decline preparation"} {
		t.Run(failure, func(t *testing.T) {
			w, ui, sys, home := sandboxFixture(t)
			base := sys.sandboxFn
			sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
				if failure == "old version" && args[0] == "version" {
					return "sbx version: v0.42.0", nil
				}
				if failure == "null list" && args[0] == "ls" {
					return "null", nil
				}
				if failure == "auth" && args[0] == "ls" {
					return "PRIVATE_SECRET runtime offline", errors.New("PRIVATE_SECRET")
				}
				if failure == "create" && args[0] == "create" || failure == "ready" && args[0] == "exec" && args[4] == "sandbox-startup" || failure == "sharing" && args[0] == "exec" && args[4] == "sh" {
					return "PRIVATE_SECRET", errors.New("PRIVATE_SECRET")
				}
				return base(ctx, args...)
			}
			ui.decisions = []bool{false, true, false, false, false, true} // no retry; explicitly disable; skip integrations; save
			switch failure {
			case "missing":
				sys.missing["sbx"] = true
			case "create", "ready", "sharing":
				ui.decisions = append([]bool{true}, ui.decisions...)
			case "decline preparation":
				ui.decisions = append([]bool{false}, ui.decisions...)
			case "extension":
				_ = os.Remove(filepath.Join(home, ".pi", "agent", "npm", "node_modules", "@christianmoesl", "pi-sbx", "package.json"))
				ui.decisions = append([]bool{false}, ui.decisions...)
			}
			if err := w.run(); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SBX.Enabled == nil || *cfg.SBX.Enabled {
				t.Fatal("failure silently saved enabled")
			}
			if _, err := os.Stat(filepath.Join(home, "config", "radar", "sandbox")); !os.IsNotExist(err) {
				t.Fatal("disabled setup wrote kit")
			}
			if strings.Contains(ui.transcript.String(), "PRIVATE_SECRET") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestSandboxSetupRetryAndLogin(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	base := sys.sandboxFn
	versionCalls, listCalls := 0, 0
	sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "version" {
			versionCalls++
			if versionCalls == 1 {
				return "old", nil
			}
		}
		if args[0] == "ls" {
			listCalls++
			if listCalls == 1 {
				return "run sbx login", errors.New("unauthorized")
			}
		}
		return base(ctx, args...)
	}
	ui.decisions = []bool{true, true, true, false, false, false, true} // retry, login, prepare, integrations, save
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	if versionCalls != 2 || listCalls != 2 || !strings.Contains(strings.Join(sys.calls, "\n"), "sbx login") {
		t.Fatal(sys.calls)
	}
	if !strings.Contains(ui.transcript.String(), "SBX login") {
		t.Fatal("login side effect not reported")
	}
}

func TestSandboxSetupPreservesCustomRecipeAndExistingStarter(t *testing.T) {
	w, ui, sys, home := sandboxFixture(t)
	path, _ := config.Path()
	cfg := config.Default()
	plan, err := planSandboxFiles(path, &cfg.SBX)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.apply(); err != nil {
		t.Fatal(err)
	}
	custom := []byte(strings.Replace(sandbox.KitSpec, "radar-sandbox:latest", "custom-sandbox:v1", 1))
	if err := os.WriteFile(filepath.Join(plan.root, "spec.yaml"), custom, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.SBX.ReadyCommand = []string{"custom-ready"}
	cfg.SBX.Kit.Path = filepath.Join(home, "custom-mixin")
	if err := config.Create(cfg); err != nil {
		t.Fatal(err)
	}
	ui.decisions = []bool{true, false, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.SBX.Kit, cfg.SBX.Kit) || !reflect.DeepEqual(saved.SBX.ReadyCommand, cfg.SBX.ReadyCommand) || saved.SBX.EnvFile != cfg.SBX.EnvFile {
		t.Fatal("custom recipe changed")
	}
	got, _ := os.ReadFile(filepath.Join(plan.root, "spec.yaml"))
	if string(got) != string(custom) {
		t.Fatal("custom kit overwritten")
	}
	if !strings.Contains(strings.Join(sys.calls, "\n"), "--kit "+cfg.SBX.Kit.Path) {
		t.Fatal("mixin not used")
	}
	if w.sandboxPlan != nil {
		t.Fatal("custom config must not generate another starter")
	}
}

func TestSandboxStarterDetectsConcurrentEditsAndNeverOverwrites(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			_, _, _, _ = fixture(t)
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
	_, _, _, _ = fixture(t)
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
		if strings.Contains(file.details, "do-not-display") || file.action != "UNCHANGED" {
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

func TestSandboxProbeCleanupFailureBlocksSetupAndRetainsMount(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	base := sys.sandboxFn
	name, anchor := "", ""
	sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "create" {
			name = args[2]
			anchor = args[len(args)-2]
		}
		if args[0] == "rm" {
			return "private", errors.New("failed")
		}
		if args[0] == "ls" && name != "" {
			return fmt.Sprintf(`{"sandboxes":[{"name":%q}]}`, name), nil
		}
		return base(ctx, args...)
	}
	ui.decisions = []bool{true}
	err := w.run()
	var cleanupErr *sandboxCleanupError
	if !errors.As(err, &cleanupErr) {
		t.Fatal(err)
	}
	defer os.RemoveAll(cleanupErr.temp)
	if _, err := os.Stat(anchor); err != nil {
		t.Fatal("removed a still-mounted probe directory")
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("cleanup failure saved config")
	}
}

func TestSandboxProbeCleanupAfterCancellation(t *testing.T) {
	w, _, sys, _ := sandboxFixture(t)
	base := sys.sandboxFn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleaned := false
	sys.sandboxFn = func(callCtx context.Context, args ...string) (string, error) {
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

func TestSandboxSetupUnsupportedPlatformDoesNotProbeOrEnable(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	sys.goos = "linux"
	ui.decisions = []bool{false, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	for _, call := range sys.calls {
		if strings.HasPrefix(call, "sbx ") {
			t.Fatal(call)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SBX.Enabled != nil {
		t.Fatal("unsupported platform altered enablement")
	}
}

func TestSandboxExplicitPublishedRecipeIsPreserved(t *testing.T) {
	_, _, _, _ = fixture(t)
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
	_, _, _, _ = fixture(t)
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

func TestSandboxPreparationEscapeDoesNotOfferHostFallback(t *testing.T) {
	w, ui, _, _ := sandboxFixture(t)
	ui.cancelAt = "Prepare and test the sandbox environment now?"
	if err := w.run(); !errors.Is(err, ErrAborted) {
		t.Fatal(err)
	}
	for _, title := range ui.titles {
		if strings.Contains(title, "Continue setup") {
			t.Fatal("cancel offered fallback")
		}
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("cancel saved config")
	}
}

func TestSandboxSetupConcurrentStarterEditLeavesConfigurationUnsaved(t *testing.T) {
	w, ui, _, home := sandboxFixture(t)
	file := filepath.Join(home, "config", "radar", "sandbox", "spec.yaml")
	ui.decisions = []bool{true, false, false, false, true}
	ui.beforeConfirm = func(title string) {
		if title == "Save this configuration?" {
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("user-kit"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.run(); err == nil {
		t.Fatal("concurrent starter edit saved")
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("conflict saved config")
	}
	if _, err := os.Stat(filepath.Join(home, "notes")); !os.IsNotExist(err) {
		t.Fatal("conflict created directories")
	}
}

func TestSandboxCommandOutputIsBounded(t *testing.T) {
	var b sandboxCommandOutput
	input := []byte(strings.Repeat("x", (1<<20)+20))
	if n, err := b.Write(input); err != nil || n != len(input) {
		t.Fatal(n, err)
	}
	if b.Len() != 1<<20 || !b.truncated {
		t.Fatal("output not bounded")
	}
	if n, err := b.Write(input); err != nil || n != len(input) || b.Len() != 1<<20 {
		t.Fatal("repeated overflow")
	}
}

// Opt-in host test: creates/removes one disposable SBX sandbox, pulls the public
// image if needed and mounts only private fixture directories. No Radar user
// config, credentials or network policy are modified.
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
	ui := &fakeUI{}
	w := newWizard(ui, realSystem{})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	err = w.probeSandbox(ctx, cfg, plan)
	t.Log(ui.transcript.String())
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
