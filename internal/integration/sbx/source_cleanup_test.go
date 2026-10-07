package sbx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestSandboxCleanupWaitsOnlyForManagedResources(t *testing.T) {
	for _, tc := range []struct {
		name                string
		managed             bool
		registrationChanged bool
		missing             bool
	}{
		{name: "managed", managed: true},
		{name: "managed registration changed while waiting", managed: true, registrationChanged: true},
		{name: "standalone"},
		{name: "missing standalone", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, "workspaces")
			anchor := filepath.Join(root, "feature")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			configPath := filepath.Join(home, "config", "radar", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, fmt.Appendf(nil, `{workspace: {root_dir: %q},linking_mark_prefixes: ["ABC"]}`, root), 0o600); err != nil {
				t.Fatal(err)
			}
			group := workspacegroup.Workspace{
				ID: workspacegroup.ID(anchor), Name: "feature", Path: anchor,
				Sandbox: &workspacegroup.Sandbox{Name: "managed-sandbox", Agent: "shell"},
			}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			callsPath := filepath.Join(home, "calls")
			body := fmt.Sprintf("printf '%%s\\n' \"$*\" >> %q\n", callsPath)
			if tc.missing {
				body += "printf 'sandbox not found\\n' >&2\nexit 1\n"
			}
			installFakeSBX(t, home, body)
			unlock := holdSandboxCleanupNoteLock(t, root)
			name := "standalone-sandbox"
			if tc.managed {
				name = group.Sandbox.Name
			}
			target := protocol.CleanupTarget{Source: "sbx", Kind: "sandbox", ResourceID: name, Path: anchor}
			done := make(chan error, 1)
			go func() {
				_, err := (Source{}).Cleanup(context.Background(), integration.CleanupRequest{Target: target})
				done <- err
			}()
			if tc.managed {
				select {
				case err := <-done:
					t.Fatalf("managed cleanup bypassed the note lock: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				if data, err := os.ReadFile(callsPath); !os.IsNotExist(err) {
					t.Fatalf("sandbox was touched while creator held the lock: %s, %v", data, err)
				}
				if tc.registrationChanged {
					if err := workspacegroup.Update(root, func(registry *workspacegroup.Registry) error {
						registry.Workspaces[0].Sandbox.Name = "replacement-sandbox"
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				unlock()
			}
			select {
			case err := <-done:
				if tc.registrationChanged {
					if err == nil || !strings.Contains(err.Error(), "registration changed") {
						t.Fatalf("cleanup did not re-read registration under the lock: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not finish (standalone must not wait; managed must not recursively lock)")
			}
			unlock()
			data, err := os.ReadFile(callsPath)
			if tc.registrationChanged {
				if !os.IsNotExist(err) {
					t.Fatalf("stale managed target was removed: %s, %v", data, err)
				}
			} else if err != nil || string(data) != "rm --force "+name+"\n" {
				t.Fatalf("sandbox cleanup calls = %q, error = %v", data, err)
			}
		})
	}
}

func holdSandboxCleanupNoteLock(t *testing.T, root string) func() {
	t.Helper()
	lock, err := os.OpenFile(filepath.Join(root, ".radar-notes.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) })
	return func() {
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
			t.Fatal(err)
		}
	}
}
