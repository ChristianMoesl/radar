package onboarding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"radar/internal/config"
	"radar/internal/setup"
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
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@christianmoesl/pi-sbx","version":"0.7.0"}`), 0600); err != nil {
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
				if ui.reviewed == nil || !strings.Contains(ui.reviewed.summaryText, "verified") {
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
	for _, failure := range []string{"missing", "old version", "auth", "null list", "old list wrapper", "malformed list", "extension", "create", "ready", "sharing", "decline preparation"} {
		t.Run(failure, func(t *testing.T) {
			w, ui, sys, home := sandboxFixture(t)
			base := sys.sandboxFn
			sys.sandboxFn = func(ctx context.Context, args ...string) (string, error) {
				if failure == "old version" && args[0] == "version" {
					return `{"client":{"version":"v0.42.0"}}`, nil
				}
				if failure == "null list" && args[0] == "ls" {
					return "null", nil
				}
				if failure == "old list wrapper" && args[0] == "ls" {
					return `{"sandboxes":[]}`, nil
				}
				if failure == "malformed list" && args[0] == "ls" {
					return `[`, nil
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
	if versionCalls != 3 || listCalls != 2 || !strings.Contains(strings.Join(sys.calls, "\n"), "sbx login") {
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
	root := filepath.Join(filepath.Dir(path), "sandbox")
	cfg.SBX.Kit.Name = root
	cfg.SBX.EnvFile = filepath.Join(root, "sandbox.env")
	cfg.SBX.AdditionalMounts = []string{filepath.Join(root, "startup.d") + ":ro"}
	if err := os.MkdirAll(filepath.Join(root, "startup.d"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.SBX.EnvFile, []byte("SBX_STARTUP_DIR="+filepath.Join(root, "startup.d")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	custom := []byte(strings.Replace(sandbox.KitSpec, "radar-sandbox:latest", "custom-sandbox:v1", 1))
	if err := os.WriteFile(filepath.Join(root, "spec.yaml"), custom, 0600); err != nil {
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
	got, _ := os.ReadFile(filepath.Join(root, "spec.yaml"))
	if string(got) != string(custom) {
		t.Fatal("custom kit overwritten")
	}
	if !strings.Contains(strings.Join(sys.calls, "\n"), "--kit "+cfg.SBX.Kit.Path) {
		t.Fatal("mixin not used")
	}
	if hasStarterFiles(w.plans) {
		t.Fatal("custom config must not generate another starter")
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
			return fmt.Sprintf(`[{"name":%q}]`, name), nil
		}
		return base(ctx, args...)
	}
	ui.decisions = []bool{true}
	err := w.run()
	if err == nil || !strings.Contains(err.Error(), "temporary sandbox cleanup could not be confirmed") {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(anchor))
	if _, err := os.Stat(anchor); err != nil {
		t.Fatal("removed a still-mounted probe directory")
	}
	if needed, _ := Needed(); !needed {
		t.Fatal("cleanup failure saved config")
	}
}

func TestSandboxSetupUnsupportedPlatformDoesNotProbeOrEnable(t *testing.T) {
	w, ui, sys, _ := sandboxFixture(t)
	sys.goos = "freebsd"
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

func hasStarterFiles(plans []setup.Plan) bool {
	for _, plan := range plans {
		for _, file := range plan.Files {
			if strings.HasSuffix(file.Path, "/spec.yaml") {
				return true
			}
		}
	}
	return false
}
