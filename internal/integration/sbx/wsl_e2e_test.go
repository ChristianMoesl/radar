package sbx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sbxclient "radar/internal/integration/sbx/client"
	"radar/internal/integration/workspace"
	"radar/internal/linking"
	"radar/internal/protocol"
)

// This test creates and removes only a uniquely named disposable sandbox.
// Managed workspace provisioning remains blocked: Windows SBX v0.43.0 cannot
// read the WSL symlink Radar needs for notes.md.
func TestWindowsSBXLifecycleE2E(t *testing.T) {
	if os.Getenv("RADAR_SBX_WSL_E2E") != "1" {
		t.Skip("set RADAR_SBX_WSL_E2E=1 to exercise Windows SBX on WSL2")
	}
	if !sbxclient.IsWSL() {
		t.Fatal("requires WSL2")
	}
	raw := sbxclient.ExecRunner{}
	client := sbxclient.New(raw)
	executable, err := client.Executable()
	if err != nil || executable != "sbx.exe" {
		t.Fatalf("requires Windows SBX: %q, %v", executable, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	mount := filepath.Join(root, "workspace with spaces")
	if err := os.Mkdir(mount, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "from-host.txt"), []byte("host fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	hostPath, err := raw.Run(ctx, "", "wslpath", "-w", mount)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("radar-wsl-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if _, err := cleanupSandbox(cleanupCtx, workspace.ExecRunner{}, protocol.CleanupTarget{Source: "sbx", Kind: "sandbox", ResourceID: name}); err != nil {
			t.Errorf("remove test sandbox: %v", err)
		}
	})
	// Raw creation is deliberate: this is a simple SBX mount, not a managed Radar
	// workspace. No workaround for unsupported note links is installed.
	if _, err := raw.Run(ctx, "/", executable, "create", "--name", name, "shell", strings.TrimSpace(hostPath)); err != nil {
		t.Fatal(err)
	}
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "radar"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "radar", "config.json"), []byte(fmt.Sprintf(`{"workspace":{"root_dir":%q},"linking_mark_prefixes":["ABC"]}`, filepath.Join(root, "registered"))), 0600); err != nil {
		t.Fatal(err)
	}
	refs, status := FetchSandboxes(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), linking.NewMarkMatcher([]string{"ABC"}))
	if status.Status != "ok" {
		t.Fatalf("collection: %+v", status)
	}
	found := false
	for _, ref := range refs {
		if SandboxName(ref) != name {
			continue
		}
		found = true
		if ref.Path != mount || ref.CanonicalKey != "workspace:"+mount {
			t.Fatalf("untranslated sandbox: %+v", ref)
		}
		multiplexer := &fakeMultiplexer{}
		if _, err := openShell(ctx, workspace.ExecRunner{}, multiplexer, ref, OpenShellOptions{}); err != nil {
			t.Fatal(err)
		}
		if multiplexer.created.FirstCommand != "cd / && exec sbx.exe run --name '"+name+"'" || multiplexer.created.Path != mount {
			t.Fatalf("shell action: %+v", multiplexer.created)
		}
	}
	if !found {
		t.Fatal("test sandbox was not collected")
	}
	output, err := client.Run(ctx, "", "exec", name, "cat", "from-host.txt")
	if err != nil || strings.TrimSpace(output) != "host fixture" {
		t.Fatalf("read: %q, %v", output, err)
	}
	if _, err := client.Run(ctx, "", "exec", name, "sh", "-c", `printf 'sandbox fixture' > from-sandbox.txt`); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(mount, "from-sandbox.txt")); err != nil || string(data) != "sandbox fixture" {
		t.Fatalf("write: %q, %v", data, err)
	}
}
