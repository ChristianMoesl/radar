package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPopupOpensRadarExecutable(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	tmuxPath := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RADAR_TMUX_ARGS\"\n"
	if err := os.WriteFile(tmuxPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RADAR_TMUX_ARGS", argsPath)

	if err := Popup("/tmp/radar binary"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(args)), "display-popup\n-E\n\"/tmp/radar binary\""; got != want {
		t.Fatalf("tmux args = %q, want %q", got, want)
	}
}
