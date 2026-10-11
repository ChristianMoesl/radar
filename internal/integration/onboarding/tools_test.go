package onboarding

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

func TestFreshHomebrewToolReport(t *testing.T) {
	w, ui, sys, _ := fixture(t)
	for _, name := range []string{"node", "npm", "pi"} {
		sys.missing[name] = true
	}
	if err := w.run(); !errors.Is(err, ErrToolsNotReady) {
		t.Fatalf("expected tool report: %v", err)
	}
	want := `
Radar setup

  ✓ git      ready
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
	if got := ui.transcript.String(); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
	if len(ui.titles) != 0 || len(sys.calls) != 4 {
		t.Fatalf("unexpected prompts or commands: %v %v", ui.titles, sys.calls)
	}
	if needed, err := Needed(); err != nil || !needed {
		t.Fatalf("tool check wrote config: %v", err)
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
			sys.versions = map[string]string{tc.name: tc.output}
			sys.outputErrors = map[string]error{tc.name: tc.err}
			got := w.checkTool(context.Background(), tc.name)
			if got.state != tc.state || got.detail != tc.detail {
				t.Fatalf("got %+v; want %v %q", got, tc.state, tc.detail)
			}
			if len(sys.calls) != 1 {
				t.Fatalf("extra probes: %v", sys.calls)
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
			sys.missing = tc.missing
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
			if len(sys.calls) != 0 {
				t.Fatalf("guidance executed commands: %v", sys.calls)
			}
		})
	}
}

func TestReadyToolsContinueWithoutRepairParagraph(t *testing.T) {
	w, ui, _, _ := fixture(t)
	if err := w.requiredTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := ui.transcript.String()
	if strings.Count(text, "✓") != len(requiredTools) || strings.Contains(text, "!") || strings.Contains(text, "Install") || strings.Contains(text, "Then run") {
		t.Fatalf("healthy report: %s", text)
	}
}

func TestLinuxFdfindIsReportedAsReady(t *testing.T) {
	w, _, sys, _ := fixture(t)
	sys.goos = "linux"
	sys.missing["fd"] = true
	if check := w.checkTool(context.Background(), "fd"); check.state != toolReady {
		t.Fatal(check)
	}
	if got := strings.Join(sys.calls, "\n"); got != "fdfind --version" {
		t.Fatal(got)
	}
}

func TestToolReportStyling(t *testing.T) {
	for _, tc := range []struct {
		name, term, noColor string
		tty, wantColor      bool
	}{
		{"terminal", "xterm-256color", "", true, true},
		{"NO_COLOR", "xterm-256color", "1", true, false},
		{"dumb terminal", "dumb", "", true, false},
		{"redirected", "xterm-256color", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", tc.term)
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("CLICOLOR_FORCE", "1") // Must not override redirection/NO_COLOR.
			var output strings.Builder
			ui := terminalUI{out: &output}
			if tc.tty {
				master, slave, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				defer slave.Close()
				ui.out = slave
			}
			style := ui.style()
			text := style.Bold(true).Render("Radar setup") + style.Foreground(lipgloss.Color("2")).Render("✓") + style.Foreground(lipgloss.Color("3")).Render("!")
			if strings.Contains(text, "\x1b[") != tc.wantColor || ansi.Strip(text) != "Radar setup✓!" {
				t.Fatalf("unexpected style: %q", text)
			}
		})
	}
}

func TestFreshIntegrationPromptsDefaultToYes(t *testing.T) {
	w, ui, _, _ := fixture(t)
	ui.decisions = []bool{false, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GitHub", "Jira", "Datadog"} {
		if !ui.defaults["Connect Radar to "+name+"?"] {
			t.Fatalf("%s did not default to Yes", name)
		}
	}
	if ui.defaults["Save this configuration?"] {
		t.Fatal("final save default changed")
	}
	for _, unwanted := range []string{"RADAR_JIRA_", "RADAR_DATADOG_", "set -g prefix", "bind-key C-b send-prefix"} {
		if strings.Contains(ui.transcript.String(), unwanted) {
			t.Fatalf("setup includes %q", unwanted)
		}
	}
}

func TestSetupCompletionIsCompact(t *testing.T) {
	w, ui, _, _ := fixture(t)
	ui.decisions = []bool{false, false, false, true}
	if err := w.run(); err != nil {
		t.Fatal(err)
	}
	_, completion, ok := strings.Cut(ui.transcript.String(), "✓ Radar configured\n")
	if !ok || strings.Count(completion, "\n") > 3 {
		t.Fatalf("completion is not concise: %s", completion)
	}
	for _, want := range []string{"Ctrl+B, release, then R", "Run radar", "/login in Pi"} {
		if !strings.Contains(completion, want) {
			t.Fatalf("completion lost %q: %s", want, completion)
		}
	}
	for _, unwanted := range []string{"config.yaml", "secrets.yaml", "tmux.conf"} {
		if strings.Contains(completion, unwanted) {
			t.Fatalf("completion repeats reviewed path %q: %s", unwanted, completion)
		}
	}
}
