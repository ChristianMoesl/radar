package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/setup"
	"radar/internal/setup/setuptest"
)

func TestFreshHomebrewToolReport(t *testing.T) {
	w, ui, sys, _ := fixture(t)
	for _, name := range []string{"node", "npm", "pi"} {
		sys.Missing[name] = true
	}
	if err := w.Dependencies(context.Background()); !errors.Is(err, ErrToolsNotReady) {
		t.Fatalf("expected tool report: %v", err)
	}
	want := `  ✓ git      ready
  ✓ tmux     ready
  ✓ fd       ready
  ✓ gh       ready
  ! Node.js  missing — requires 24+
  ! npm      missing — included with Node.js
  ! Pi       missing — requires 0.85.1+

Install the missing tools:

  brew install node && curl -fsSL https://pi.dev/install.sh | sh

Then run radar again to continue setup.
`
	if got := ui.Transcript.String(); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
	if len(ui.Titles) != 0 || len(sys.Calls) != 4 {
		t.Fatalf("unexpected prompts or commands: %v %v", ui.Titles, sys.Calls)
	}

}

func TestToolChecksDistinguishFailures(t *testing.T) {
	for _, tc := range []struct {
		name, output, detail string
		err                  error
		state                toolState
	}{
		{"node", "v24.0.0", "ready", nil, toolReady},
		{"node", "v22.14.0", "22.14.0 — requires 24+", nil, toolOutdated},
		{"node", "unrecognized output", "could not read version — requires 24+", nil, toolBroken},
		{"node", "", "failed to run", errors.New("private error details"), toolBroken},
		{"pi", "0.85.1", "ready", nil, toolReady},
		{"pi", "0.85.0", "0.85.0 — requires 0.85.1+", nil, toolOutdated},
		{"pi", "<html>failure</html>", "could not read version — requires 0.85.1+", nil, toolBroken},
		{"tmux", "tmux 3.2", "ready", nil, toolReady},
		{"tmux", "tmux next-3.7", "ready", nil, toolReady},
		{"tmux", "tmux 3.1c", "3.1c — requires 3.2+", nil, toolOutdated},
		{"tmux", "broken", "could not read version — requires 3.2+", nil, toolBroken},
		{"git", "", "failed to run", errors.New("developer tools missing"), toolBroken},
		{"gh", "", "failed to run", errors.New("broken shim"), toolBroken},
	} {
		t.Run(tc.name+"/"+tc.detail, func(t *testing.T) {
			w, _, sys, _ := fixture(t)
			sys.Versions = map[string]string{tc.name: tc.output}
			sys.OutputErrors = map[string]error{tc.name: tc.err}
			got := w.checkTool(context.Background(), tc.name)
			if got.state != tc.state || got.detail != tc.detail {
				t.Fatalf("got %+v; want %v %q", got, tc.state, tc.detail)
			}
			if len(sys.Calls) != 1 {
				t.Fatalf("extra probes: %v", sys.Calls)
			}
		})
	}
}

func TestToolRepairRecommendations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		states   map[string]toolState
		missing  map[string]bool
		command  string
		guidance string
	}{
		{"only Pi", map[string]toolState{"pi": toolMissing}, nil, piInstallCommand, ""},
		{"only fd", map[string]toolState{"fd": toolMissing}, nil, "brew install fd", ""},
		{"missing tools grouped", map[string]toolState{"git": toolMissing, "tmux": toolMissing, "gh": toolMissing}, nil, "brew install git tmux gh", ""},
		{"Node includes npm", map[string]toolState{"node": toolMissing, "npm": toolMissing}, nil, "brew install node", ""},
		{"old Node is not replaced", map[string]toolState{"node": toolOutdated}, nil, "", "existing Node.js installation"},
		{"broken Node is not replaced", map[string]toolState{"node": toolBroken}, nil, "", "existing Node.js installation"},
		{"missing npm is not separate package", map[string]toolState{"npm": toolMissing}, nil, "", "Repair npm through your existing Node.js installation"},
		{"missing node with existing npm", map[string]toolState{"node": toolMissing}, nil, "", "existing Node.js installation"},
		{"old tmux", map[string]toolState{"tmux": toolOutdated}, nil, "", "Update tmux using its existing installation method"},
		{"broken git shim", map[string]toolState{"git": toolBroken}, nil, "", "Repair git using its existing installation method; check PATH"},
		{"existing Pi", map[string]toolState{"pi": toolOutdated}, nil, "", "Update Pi to 0.85.1+ using its existing installation method"},
		{"broken Pi", map[string]toolState{"pi": toolBroken}, nil, "", "Repair Pi using its existing installation method"},
		{"no brew", map[string]toolState{"fd": toolMissing}, map[string]bool{"brew": true}, "", "Install fd with your system package manager"},
		{"no brew or Node", map[string]toolState{"node": toolMissing, "npm": toolMissing, "pi": toolMissing}, map[string]bool{"brew": true}, piInstallCommand, "Install Node.js 24+ with npm: https://nodejs.org/en/download"},
		{"curl absent", map[string]toolState{"pi": toolMissing}, map[string]bool{"curl": true}, "brew install curl && " + piInstallCommand, ""},
		{"curl and brew absent", map[string]toolState{"pi": toolMissing}, map[string]bool{"curl": true, "brew": true}, piInstallCommand, "Install curl with your system package manager before running the command below"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _, sys, _ := fixture(t)
			sys.Missing = tc.missing
			checks := map[string]toolCheck{}
			for _, name := range requiredTools {
				checks[name] = toolCheck{name: name, state: tc.states[name]}
			}
			commands, guidance := w.toolRepairs(checks)
			if got := strings.Join(commands, " && "); got != tc.command {
				t.Fatalf("command %q; want %q", got, tc.command)
			}
			got := strings.Join(guidance, "\n")
			if tc.guidance == "" && got != "" || !strings.Contains(got, tc.guidance) {
				t.Fatalf("guidance %q; want %q", got, tc.guidance)
			}
			if len(sys.Calls) != 0 {
				t.Fatalf("guidance executed commands: %v", sys.Calls)
			}
		})
	}
}

func TestReadyToolsContinueWithoutRepairParagraph(t *testing.T) {
	w, ui, _, _ := fixture(t)
	if err := w.requiredTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := ui.Transcript.String()
	if strings.Count(text, "✓") != len(requiredTools) || strings.Contains(text, "!") || strings.Contains(text, "Install") || strings.Contains(text, "Then run") {
		t.Fatalf("healthy report: %s", text)
	}
}

func TestLinuxFdfindIsReportedAsReady(t *testing.T) {
	w, _, sys, _ := fixture(t)
	sys.Goos = "linux"
	sys.Missing["fd"] = true
	if check := w.checkTool(context.Background(), "fd"); check.state != toolReady {
		t.Fatal(check)
	}
	if got := strings.Join(sys.Calls, "\n"); got != "fdfind --version" {
		t.Fatal(got)
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
		t.Fatalf("custom Missing: %v %v", ready, err)
	}
	setuptest.InstallRadar(t, custom)
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

func TestOnlyPiExtensionsHaveOnboardingInstallCommands(t *testing.T) {
	w, _, _, _ := fixture(t)
	for _, dep := range []string{"node", "fd", "pi", "git", "gh", "tmux", "npm"} {
		if _, err := w.installCommand(dep); err == nil {
			t.Fatalf("onboarding can install baseline tool %s", dep)
		}
	}
	for _, dep := range []string{"pi-radar", "pi-sbx"} {
		argv, err := w.installCommand(dep)
		if err != nil || strings.Join(argv, " ") != "pi install npm:@christianmoesl/"+dep {
			t.Fatalf("%s: %v %v", dep, argv, err)
		}
	}
}

func TestInstalledSBXRequiresVersionedPiSBXWithoutChangingItsSource(t *testing.T) {
	w, ui, sys, home := fixture(t)
	delete(sys.Missing, "sbx")
	ui.Decisions = []bool{false}
	if err := w.Extensions(context.Background(), Dependency{"pi-sbx", "pi-sbx 0.7.0+"}); !errors.Is(err, setup.ErrExtensionDeclined) {
		t.Fatalf("missing sandbox extension: %v", err)
	}
	if !strings.Contains(ui.Transcript.String(), "pi-sbx 0.7.0+") {
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
	for version, want := range map[string]bool{"0.5.9": false, "0.7.0": true, "1.0.0": true, "0.7.0+local.1": true, "0.7.0-rc.1": false, "v0.7.0": false, "0.06.0": false, "0.7.00": false, "0.7.0+": false, "0.7.0\n": false} {
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

func TestBaselineDependenciesDoNotCheckSBX(t *testing.T) {
	w, ui, sys, _ := fixture(t)
	delete(sys.Missing, "sbx")
	if err := w.Dependencies(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ui.Transcript.String(), "pi-sbx") {
		t.Fatal("SBX must be checked after the explicit choice")
	}
}

func TestMissingToolsIsReadOnlyAndSharesPrerequisiteList(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		missing        map[string]bool
		want           []string
	}{
		{name: "ready"},
		{name: "missing Pi and Node", missing: map[string]bool{"node": true, "npm": true, "pi": true}, want: []string{"node", "npm", "pi"}},
		{name: "Git still required", missing: map[string]bool{"git": true}, want: []string{"git"}},
		{name: "missing tmux", missing: map[string]bool{"tmux": true}, want: []string{"tmux"}},
		{name: "Debian fdfind accepted", platform: "linux", missing: map[string]bool{"fd": true}},
		{name: "both fd names missing", platform: "linux", missing: map[string]bool{"fd": true, "fdfind": true}, want: []string{"fd"}},
		{name: "fdfind not accepted on macOS", platform: "darwin", missing: map[string]bool{"fd": true}, want: []string{"fd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys := &setuptest.System{Goos: tc.platform, Missing: tc.missing, NodeVersion: "v22.0.0"}
			if got := missingTools(sys); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("missing=%v want=%v", got, tc.want)
			}
			if len(sys.Calls) != 0 {
				t.Fatalf("PATH inspection executed commands: %v", sys.Calls)
			}
		})
	}
}

func TestVersionValidation(t *testing.T) {
	for _, value := range []string{"v22.1.0", "24.0", "v24.0.0-rc.1", "nonsense"} {
		if versionAtLeast(value, [3]int{24, 0, 0}) {
			t.Errorf("accepted version %q", value)
		}
	}
	if !versionAtLeast("v24.1.0\n", [3]int{24, 0, 0}) {
		t.Fatal("rejected Node 24")
	}
}

func fixture(t *testing.T) (Service, *setuptest.UI, *setuptest.System, string) {
	t.Helper()
	home := setuptest.Home(t)
	ui := &setuptest.UI{}
	sys := &setuptest.System{Missing: map[string]bool{"sbx": true}}
	return Service{UI: ui, System: sys, Completed: func(effect string) { ui.Completed = append(ui.Completed, effect) }}, ui, sys, home
}
