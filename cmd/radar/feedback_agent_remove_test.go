package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedbackAgentRemovalTransportDoesNotEnableCLIControl(t *testing.T) {
	_, anchor, _ := feedbackManualFixture(t)
	socket := filepath.Join(t.TempDir(), "missing")
	env := []string{"HOME=" + os.Getenv("HOME"), "XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME"), "RADAR_SOCKET=" + socket}
	for _, mode := range []string{"preview", "apply"} {
		args := []string{"feedback-diagnostics", "--workspace", anchor, "--section", "agent_remove", "--parent-id", "record", "--agent-id", "worker", "--action", mode, "--json"}
		stdout, _, code := outputCLI(t, args, "", env...)
		if code == 0 || strings.Contains(stdout, `"diagnostic_agent_remove":`) {
			t.Fatal("removal CLI activated without consent flow")
		}
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("unsupported control started daemon")
	}
}
