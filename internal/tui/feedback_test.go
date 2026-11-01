package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"radar/internal/pi"
	"radar/internal/protocol"
)

func TestFeedbackDashboardEntryIsDisabledWithoutLinearRuntime(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	m := model{}
	if m.mainHintAvailable("F", protocol.Task{}, false) {
		t.Fatal("unavailable feedback shown as enabled")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	got := updated.(model)
	if cmd != nil || got.editor.active || !strings.Contains(got.message, "linear MCP") {
		t.Fatalf("unavailable feedback created a workspace: %+v", got)
	}
}

func TestFeedbackDashboardEntryStartsNormalWorkspaceEditor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", profile)
	dir := filepath.Join(profile, "radar/loaded")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	report := pi.LoadedRelease{PID: os.Getpid(), Version: "0.7.0", Profile: profile, Updated: time.Now().UnixMilli(), LinearAvailable: true}
	data, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(dir, "live.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	m := model{feedbackAvailable: true}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	got := updated.(model)
	if cmd != nil || got.mode != "workspace_name" || !got.editor.create.Feedback || !got.editor.active || got.editor.create.Name != "Radar: " {
		t.Fatalf("feedback did not reuse workspace editor: %+v", got.editor)
	}
	if len(got.editor.desired.Worktrees) != 0 || !strings.Contains(got.workspaceView(80), "Improve Radar") {
		t.Fatal("feedback requires a repository or lacks its heading")
	}
	if !got.editor.createRequest().Feedback {
		t.Fatal("editor lost feedback purpose")
	}
	// A cached enabled hint never bypasses the actual live report check.
	if err := os.Remove(filepath.Join(dir, "live.json")); err != nil {
		t.Fatal(err)
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	if cmd != nil || updated.(model).editor.active {
		t.Fatal("stale dashboard availability bypassed live gate")
	}
}
