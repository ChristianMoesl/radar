package agentservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"radar/internal/protocol"
)

func TestDiagnosticWorkspaceOwnersNativeParityAndNoPrivateReads(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agents")
	t.Setenv("RADAR_AGENT_ROOT", root)
	workspace := filepath.Join(t.TempDir(), "workspace")
	for _, id := range []string{"stopped", "nested", "sibling"} {
		path := workspace
		if id == "nested" {
			path = filepath.Join(workspace, "nested")
		}
		if id == "sibling" {
			path = workspace + "-sibling"
		}
		reg := diagnosticRegistration(t, root, id, path)
		reg.Desired = id != "stopped"
		if err := atomicJSON(filepath.Join(root, id, "registration.json"), reg); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"snapshot.json", "token", "collection.json", "schedules.json"} {
			if err := unix.Mkfifo(filepath.Join(root, id, name), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(root, "removed"), 0700); err != nil {
		t.Fatal(err)
	}
	native, err := WorkspaceRemovalOwners([]string{workspace})
	if err != nil || len(native[workspace]) != 2 || CheckWorkspaceRemoval(workspace) == nil {
		t.Fatalf("native ownership guard=%v %v", native, err)
	}
	count, snapshot, err := DiagnosticWorkspaceRemovalOwners(context.Background(), workspace)
	if err != nil || count != len(native[workspace]) || snapshot == [32]byte{} {
		t.Fatalf("count=%d err=%v", count, err)
	}
	_, same, err := DiagnosticWorkspaceRemovalOwners(context.Background(), workspace)
	if err != nil || snapshot != same {
		t.Fatal("unchanged registration snapshot drifted")
	}
	reg, err := load("stopped")
	if err != nil {
		t.Fatal(err)
	}
	reg.Entrypoint = "changed-private-command"
	if err := atomicJSON(filepath.Join(root, reg.ID, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	changedCount, changed, err := DiagnosticWorkspaceRemovalOwners(context.Background(), workspace)
	if err != nil || changedCount != count || snapshot == changed {
		t.Fatal("same-count private registration drift was not detected")
	}
	if _, err := os.Stat(filepath.Join(root, "leases")); !os.IsNotExist(err) {
		t.Fatal("ownership observation created runtime state")
	}
}

func TestDiagnosticWorkspaceOwnersUnavailableIsNeverPartial(t *testing.T) {
	for _, scenario := range []string{"invalid", "relative", "symlink", "fifo", "oversized", "aggregate", "entries", "root symlink", "root file", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "agents")
			t.Setenv("RADAR_AGENT_ROOT", root)
			workspace := filepath.Join(t.TempDir(), "workspace")
			reg := diagnosticRegistration(t, root, "owner", workspace)
			path := filepath.Join(root, reg.ID, "registration.json")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "invalid":
				if err := os.WriteFile(path, []byte("private-invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "relative":
				reg.Workspace.Path = "private-relative"
				if err := atomicJSON(path, reg); err != nil {
					t.Fatal(err)
				}
			case "symlink", "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if scenario == "symlink" {
					other := filepath.Join(t.TempDir(), "private-registration")
					if err := atomicJSON(other, reg); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, path); err != nil {
						t.Fatal(err)
					}
				} else if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				reg.Entrypoint = strings.Repeat("x", 64*1024)
				if err := atomicJSON(path, reg); err != nil {
					t.Fatal(err)
				}
			case "aggregate":
				for i := range 20 {
					reg := diagnosticRegistration(t, root, fmt.Sprintf("agent-%02d", i), workspace+"-unrelated")
					reg.Entrypoint = strings.Repeat("x", 60*1024)
					if err := atomicJSON(filepath.Join(root, reg.ID, "registration.json"), reg); err != nil {
						t.Fatal(err)
					}
				}
			case "entries":
				for i := range 1024 {
					if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("removed-%04d", i)), 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "root symlink":
				other := root + "-target"
				if err := os.Rename(root, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, root); err != nil {
					t.Fatal(err)
				}
			case "root file":
				root = filepath.Join(t.TempDir(), "private-file")
				t.Setenv("RADAR_AGENT_ROOT", root)
				if err := os.WriteFile(root, []byte("private"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			count, snapshot, err := DiagnosticWorkspaceRemovalOwners(ctx, workspace)
			if err == nil || count != 0 || snapshot != [32]byte{} || strings.Contains(err.Error(), "private") {
				t.Fatalf("partial or private evidence returned: count=%d err=%v", count, err)
			}
		})
	}
}

func TestDiagnosticWorkspaceOwnersMissingRegistryIsObservedWithoutCreatingIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	t.Setenv("RADAR_AGENT_ROOT", root)
	workspace := filepath.Join(t.TempDir(), "workspace")
	count, missing, err := DiagnosticWorkspaceRemovalOwners(context.Background(), workspace)
	if err != nil || count != 0 {
		t.Fatalf("missing root: count=%d err=%v", count, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("diagnostics created a registry")
	}
	reg := diagnosticRegistration(t, root, "owner", workspace)
	reg.Workspace = protocol.AgentWorkspaceBinding{ID: "other", Path: workspace + "-other", Mode: "local"}
	data, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(root, reg.ID, "registration.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	count, present, err := DiagnosticWorkspaceRemovalOwners(context.Background(), workspace)
	if err != nil || count != 0 || present == missing {
		t.Fatal("registry creation was not detected despite unchanged owner count")
	}
}

func TestDiagnosticWorkspaceOwnersAbsoluteDeadlineIsNotUsableEvidence(t *testing.T) {
	ctx := delayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Millisecond)}
	count, fingerprint, err := DiagnosticWorkspaceRemovalOwners(ctx, "/registered/workspace")
	if err == nil || count != 0 || fingerprint != [32]byte{} {
		t.Fatal("expired inherited deadline produced usable ownership evidence")
	}
}
