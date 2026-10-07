package onboarding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"radar/internal/config"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPromptKeepsSecretsMaskedOnInputAndAcceptance(t *testing.T) {
	m := newPrompt(question{title: "API token", secret: true, validate: required})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("secret-fixture")})
	m = updated.(promptModel)
	if strings.Contains(m.View(), "secret-fixture") {
		t.Fatal("secret visible during entry")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(promptModel)
	if !m.accepted || strings.Contains(m.View(), "secret-fixture") || !strings.Contains(m.View(), "[hidden]") {
		t.Fatal("secret visible in transcript")
	}
}
func TestPromptValidationAndCancellation(t *testing.T) {
	m := newPrompt(question{title: "Required", validate: required})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(promptModel)
	if m.accepted || m.validation == "" {
		t.Fatal("empty input accepted")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(promptModel)
	if !m.aborted || m.accepted {
		t.Fatal("ctrl-c did not abort")
	}
}
func TestConfirmationDefaultsToNoAndSupportsKeyboard(t *testing.T) {
	m := newPrompt(question{title: "Install?"})
	m.confirm = true
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(promptModel)
	if !result.accepted || result.yes {
		t.Fatal("bare Enter should not consent")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	result = updated.(promptModel)
	updated, _ = result.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(promptModel)
	if !result.accepted || !result.yes {
		t.Fatal("arrow/Enter did not accept")
	}
}

func TestRadarPackageInspectionUsesUserProfileNotProject(t *testing.T) {
	_, _, _, home := fixture(t)
	if ready, err := packageInstalled("pi-radar"); !ready || err != nil {
		t.Fatalf("installed: %v %v", ready, err)
	}
	custom := filepath.Join(home, "custom profile")
	t.Setenv("PI_CODING_AGENT_DIR", custom)
	if ready, err := packageInstalled("pi-radar"); ready || err != nil {
		t.Fatalf("custom missing: %v %v", ready, err)
	}
	installRadarFixture(t, custom)
	if ready, err := packageInstalled("pi-radar"); !ready || err != nil {
		t.Fatalf("custom installed: %v %v", ready, err)
	}
	// A declaration without its package is not an installed dependency.
	if err := os.Remove(filepath.Join(custom, "npm", "node_modules", "@christianmoesl", "pi-radar", "package.json")); err != nil {
		t.Fatal(err)
	}
	if ready, err := packageInstalled("pi-radar"); ready || err != nil {
		t.Fatalf("stale declaration: %v %v", ready, err)
	}
}
func TestRadarPackageInspectionDoesNotOverrideDisabledResources(t *testing.T) {
	_, _, _, home := fixture(t)
	path := filepath.Join(home, ".pi", "agent", "settings.json")
	if err := os.WriteFile(path, []byte(`{"packages":[{"source":"npm:@christianmoesl/pi-radar","extensions":[]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if ready, err := packageInstalled("pi-radar"); ready || err == nil {
		t.Fatal("disabled extension should require an explicit user fix")
	}
}
func TestPackageManagerCommands(t *testing.T) {
	_, ui, sys, _ := fixture(t)
	sys.goos = "linux"
	sys.missing["brew"] = true
	w := newWizard(ui, sys)
	for dep, want := range map[string]string{"node": "apt-get install -y nodejs npm", "fd": "apt-get install -y fd-find", "pi": "npm install --global --ignore-scripts @earendil-works/pi-coding-agent", "pi-radar": "pi install npm:@christianmoesl/pi-radar"} {
		argv, err := w.installCommand(dep)
		if err != nil || !strings.HasSuffix(strings.Join(argv, " "), want) {
			t.Fatalf("%s: %v %v", dep, argv, err)
		}
	}
	sys.missing["apt-get"] = true
	if _, err := w.installCommand("tmux"); err == nil {
		t.Fatal("must not invent an installer")
	}
}

func TestInstalledSBXRequiresVersionedPiSBXWithoutChangingItsSource(t *testing.T) {
	w, ui, sys, home := fixture(t)
	delete(sys.missing, "sbx")
	ui.decisions = []bool{false}
	if err := w.dependencies(context.Background(), config.Default().SBX); !errors.Is(err, ErrAborted) {
		t.Fatalf("missing sandbox extension: %v", err)
	}
	if !strings.Contains(ui.transcript.String(), "pi-sbx 0.6.0+") {
		t.Fatal("missing sandbox prerequisite explanation")
	}
	dir := filepath.Join(home, ".pi", "agent")
	pkg := filepath.Join(dir, "npm", "node_modules", "@christianmoesl", "pi-sbx")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"packages":["npm:@christianmoesl/pi-radar","npm:@christianmoesl/pi-sbx"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string]bool{"0.5.9": false, "0.6.0": true, "1.0.0": true, "0.6.0+local.1": true, "0.6.0-rc.1": false, "v0.6.0": false, "0.06.0": false, "0.6.00": false, "0.6.0+": false, "0.6.0\n": false} {
		if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(fmt.Sprintf(`{"name":"@christianmoesl/pi-sbx","version":%q}`, version)), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := packageInstalled("pi-sbx"); err != nil || got != want {
			t.Fatalf("%s installed=%v err=%v", version, got, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"packages":["git:github.com/ChristianMoesl/pi-sbx"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := packageInstalled("pi-sbx"); err == nil {
		t.Fatal("missing Git install must not be replaced with duplicate npm source")
	}
}

func TestPiSBXIsOnlyCheckedForEffectiveSandboxUsage(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name, platform  string
		enabled         *bool
		installed, want bool
	}{
		{"disabled with CLI installed", "darwin", &no, true, false},
		{"disabled without CLI", "darwin", &no, false, false},
		{"automatic without CLI", "darwin", nil, false, false},
		{"automatic macOS sandbox", "darwin", nil, true, true},
		{"automatic Linux without sandboxing", "linux", nil, true, false},
		{"explicit sandbox enabled", "darwin", &yes, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, ui, sys, _ := fixture(t)
			sys.goos = tc.platform
			sys.missing["sbx"] = !tc.installed
			sandbox := config.Default().SBX
			sandbox.Enabled = tc.enabled
			ui.decisions = []bool{false} // A missing required sandbox dependency must need consent.
			err := w.dependencies(context.Background(), sandbox)
			if tc.want {
				if !errors.Is(err, ErrAborted) {
					t.Fatalf("missing sandbox dependency: %v", err)
				}
			} else if err != nil {
				t.Fatalf("non-sandbox setup blocked: %v", err)
			}
			output := ui.transcript.String()
			baseline, _, _ := strings.Cut(output, "\nSBX sandboxing")
			if strings.Contains(baseline, "pi-sbx") {
				t.Fatal("pi-sbx was listed as a required Radar tool")
			}
			if strings.Contains(output, "pi-sbx") != tc.want {
				t.Fatalf("sandbox dependency check does not match effective enablement: %s", output)
			}
		})
	}
}

func TestRepeatSetupWithSBXDisabledDoesNotRequirePiSBX(t *testing.T) {
	w, ui, sys, home := fixture(t)
	delete(sys.missing, "sbx")
	cfg := config.Default()
	no := false
	cfg.SBX.Enabled = &no
	cfg.RepositoryDirs = []string{filepath.Join(home, "repos")}
	cfg.Workspace.RootDir = filepath.Join(home, "workspaces")
	cfg.Obsidian.VaultPath = filepath.Join(home, "notes")
	if err := config.Create(cfg); err != nil {
		t.Fatal(err)
	}
	ui.decisions = []bool{false, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ui.transcript.String(), "pi-sbx") {
		t.Fatal("disabled sandboxing still required pi-sbx")
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.SBX.Enabled == nil || *saved.SBX.Enabled {
		t.Fatal("setup changed explicit sandbox disablement")
	}
}
