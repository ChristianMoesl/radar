package pi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeRadarExtension(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	path, err := MaterializeRadarExtension()
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(dataHome, "radar", "pi", "radar.ts")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"radar_add_worktree", "promptSnippet", "promptGuidelines", "ctx.ui.confirm", "pi.exec", "RADAR_BINARY", "Type.Union",
		"@radar_busy", "publishBusy", "TMUX_PANE", "session_start", "agent_start", "agent_settled", "session_shutdown",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("extension is missing %q", required)
		}
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeRadarExtension(); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != text {
		t.Fatal("stale extension was not atomically replaced")
	}
}
