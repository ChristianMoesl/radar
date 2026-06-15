package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTmuxLongListLayoutE2E(t *testing.T) {
	if os.Getenv("RADAR_TMUX_E2E") != "1" {
		t.Skip("set RADAR_TMUX_E2E=1 to run tmux E2E tests")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found")
	}

	tmp := t.TempDir()
	stateHome := filepath.Join(tmp, "state")
	configHome := filepath.Join(tmp, "config")
	runtimeDir, err := os.MkdirTemp(os.TempDir(), "radar-e2e-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeDir)
	if err := os.MkdirAll(filepath.Join(stateHome, "radar"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureState(t, filepath.Join(stateHome, "radar", "tasks.json"))

	binary := filepath.Join(tmp, "radar")
	build := exec.Command("go", "build", "-o", binary, "./cmd/radar")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	session := fmt.Sprintf("radar-e2e-%d", time.Now().UnixNano())
	env := []string{
		"TERM=xterm-256color",
		"COLUMNS=100",
		"LINES=35",
		"RADAR_DISABLE_COLLECTION=1",
		"XDG_STATE_HOME=" + stateHome,
		"XDG_CONFIG_HOME=" + configHome,
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"RADAR_SOCKET=" + filepath.Join(runtimeDir, "radar.sock"),
		"RADAR_PID=" + filepath.Join(runtimeDir, "radar.pid"),
	}
	command := "env " + strings.Join(quoteArgs(env), " ") + " " + shellQuoteForTest(binary)
	if output, err := exec.Command("tmux", "new-session", "-d", "-x", "100", "-y", "35", "-s", session, command).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session failed: %v\n%s", err, output)
	}
	defer exec.Command("tmux", "kill-session", "-t", session).Run()
	defer func() {
		stop := exec.Command(binary, "stop")
		stop.Env = append(os.Environ(), env...)
		_ = stop.Run()
	}()

	initial := waitForCapture(t, session, stateHome, "Radar")
	initialLine := renderedLineIndex(initial, "Radar")
	initialColumn := renderedColumnIndex(initial, "Radar")
	if initialLine < 0 || initialColumn < 0 {
		t.Fatalf("initial capture missing counter header:\n%s", initial)
	}

	assertCaptureAfterKeys := func(name string, keys []string) {
		t.Helper()
		if len(keys) > 0 {
			args := append([]string{"send-keys", "-t", session}, keys...)
			if output, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
				t.Fatalf("tmux send-keys %s failed: %v\n%s", name, err, output)
			}
		}
		capture := waitForCapture(t, session, stateHome, "Radar")
		line := renderedLineIndex(capture, "Radar")
		column := renderedColumnIndex(capture, "Radar")
		if line != initialLine || column != initialColumn {
			t.Fatalf("%s: counter header moved from line %d/column %d to line %d/column %d:\n%s", name, initialLine, initialColumn, line, column, capture)
		}
		attentionLine := renderedLineIndex(capture, "Need attention")
		progressLine := renderedLineIndex(capture, "In progress")
		if attentionLine >= 0 && progressLine >= 0 && progressLine < attentionLine {
			t.Fatalf("%s: category headers out of order: attention=%d progress=%d\n%s", name, attentionLine, progressLine, capture)
		}
	}

	assertCaptureAfterKeys("part way down", repeatKeyStrings("j", 8))
	assertCaptureAfterKeys("bottom", repeatKeyStrings("j", 80))
	assertCaptureAfterKeys("back to top", repeatKeyStrings("k", 80))
}

func writeFixtureState(t *testing.T, path string) {
	t.Helper()
	data, err := json.MarshalIndent(longTaskListFixture(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForCapture(t *testing.T, session string, stateHome string, contains string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var capture string
	for time.Now().Before(deadline) {
		output, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-t", session).CombinedOutput()
		if err == nil {
			capture = string(output)
			if strings.Contains(capture, contains) {
				return capture
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	panes, _ := exec.Command("tmux", "list-panes", "-t", session, "-F", "#{pane_current_command} #{pane_dead} #{pane_dead_status} #{pane_width}x#{pane_height}").CombinedOutput()
	logData, _ := os.ReadFile(filepath.Join(stateHome, "radar", "radar.log"))
	t.Fatalf("timed out waiting for %q in tmux capture (panes: %s, log: %s):\n%s", contains, strings.TrimSpace(string(panes)), string(logData), capture)
	return ""
}

func repeatKeyStrings(key string, count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = key
	}
	return keys
}

func quoteArgs(args []string) []string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuoteForTest(arg)
	}
	return quoted
}

func shellQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
