package onboarding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	if err := w.dependencies(context.Background()); !errors.Is(err, ErrAborted) {
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
