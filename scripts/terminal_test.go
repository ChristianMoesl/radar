package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Initialize both speeds before taking the baseline. A fresh Linux PTY can
// encode the input speed as "same as output"; stty makes it explicit on its first
// write. Start from that normal form so the assertion still compares every bit.
func initializeTerminal(t *testing.T, terminal *os.File) {
	t.Helper()
	cmd := exec.Command("stty", "38400")
	cmd.Stdin = terminal
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize PTY: %v: %s", err, output)
	}
}

// Preserve the caller's selected terminal utility inside the otherwise isolated
// fixture PATH, just as the fixtures preserve their selected Node executable.
// Never stub stty: the PTY assertions must exercise real capture and restoration.
func linkTerminalTool(t *testing.T, bin string) {
	t.Helper()
	stty, err := exec.LookPath("stty")
	if err != nil {
		t.Fatal("terminal tests require stty on PATH")
	}
	if err := os.Symlink(stty, filepath.Join(bin, "stty")); err != nil {
		t.Fatal(err)
	}
}
