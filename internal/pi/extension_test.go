package pi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The extension is distributed as a Pi package, not embedded in the Go binary.
func extensionSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "extensions", "pi-radar", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRadarExtensionContract(t *testing.T) {
	text := extensionSource(t)
	for _, required := range []string{
		"radar_reconcile_workspace", "reconcile-workspace", "additional_mounts", "read_only", "promptSnippet", "promptGuidelines", "ctx.ui.confirm", "pi.exec", "RADAR_BINARY", "Type.Union",
		"retryableResultText", "Re-inspect and retry", "details: { plans, result, partial }", "effective_sandbox_mount_count", "workspace.auto_confirm", "const autoConfirm = plan.auto_confirm === true",
		"activity", "activityTracker", "ui_prompt_start", "ui_prompt_end", "\"waiting\"", "\"busy\"", "\"idle\"", "session_start", "agent_start", "agent_settled", "session_shutdown",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("extension is missing %q", required)
		}
	}
}
