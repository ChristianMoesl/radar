package onboarding

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

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
	if !ui.defaults["Save this configuration?"] {
		t.Fatal("final save should default to Yes")
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
			style := ui.Style()
			text := style.Bold(true).Render("Radar setup") + style.Foreground(lipgloss.Color("2")).Render("✓") + style.Foreground(lipgloss.Color("3")).Render("!")
			if strings.Contains(text, "\x1b[") != tc.wantColor || ansi.Strip(text) != "Radar setup✓!" {
				t.Fatalf("unexpected style: %q", text)
			}
		})
	}
}
