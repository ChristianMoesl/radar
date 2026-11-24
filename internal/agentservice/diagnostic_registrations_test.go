package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

func TestDiagnosticRegistrationInventoriesBoundedAndComplete(t *testing.T) {
	for _, kind := range []string{"missing", "valid", "unregistered", "exact_bound", "over_bound", "symlink", "dangling_symlink", "root_symlink", "relative", "malformed", "directory", "aggregate", "entries"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "agents")
			t.Setenv("RADAR_AGENT_ROOT", root)
			workspace := filepath.Join(t.TempDir(), "workspace")
			file := ""
			available, want := true, 0
			if kind != "missing" {
				diagnosticRegistration(t, root, "owner-a", workspace)
				reg := diagnosticRegistration(t, root, "owner-z", filepath.Join(workspace, "nested"))
				file = filepath.Join(root, reg.ID, "registration.json")
				want = 2
				if err := syscall.Mkfifo(filepath.Join(root, reg.ID, "ingress.token"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "unregistered":
				if err := os.Mkdir(filepath.Join(root, "removed"), 0700); err != nil {
					t.Fatal(err)
				}
			case "exact_bound", "over_bound":
				data, _ := os.ReadFile(file)
				size := 64 * 1024
				if kind == "over_bound" {
					size++
					available = false
				}
				if err := os.WriteFile(file, append(data, bytes.Repeat([]byte(" "), size-len(data))...), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling_symlink":
				available = false
				target := filepath.Join(t.TempDir(), "private-registration")
				if err := os.Rename(file, target); err != nil {
					t.Fatal(err)
				}
				if kind == "dangling_symlink" {
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, file); err != nil {
					t.Fatal(err)
				}
			case "root_symlink":
				available = false
				if err := os.Rename(root, root+"-target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root+"-target", root); err != nil {
					t.Fatal(err)
				}
			case "relative":
				available = false
				reg := Registration{Version: 1, ID: "owner-z", Workspace: protocol.AgentWorkspaceBinding{ID: "registered", Path: "private-relative"}}
				if err := atomicJSON(file, reg); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				available = false
				if err := os.WriteFile(file, []byte("private-invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				available = false
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			case "aggregate":
				available = false
				for i := range 20 {
					reg := diagnosticRegistration(t, root, fmt.Sprintf("other-%02d", i), workspace+"-other")
					f := filepath.Join(root, reg.ID, "registration.json")
					data, _ := os.ReadFile(f)
					if err := os.WriteFile(f, append(data, bytes.Repeat([]byte(" "), 60*1024-len(data))...), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "entries":
				available = false
				for i := range 1024 {
					if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("removed-%04d", i)), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			var before []byte
			if info, err := os.Lstat(file); err == nil && info.Mode().IsRegular() {
				before, _ = os.ReadFile(file)
			}
			agents, agentErr := DiagnosticAgents("", "")
			schedules, scheduleErr := DiagnosticAgentSchedules("", "", "")
			if available {
				if agentErr != nil || scheduleErr != nil || len(agents) != want || len(schedules) != 0 {
					t.Fatal("valid native inventory/defaults lost", agentErr, scheduleErr)
				}
				if want > 0 && (agents[0].ID != "owner-a" || agents[1].ID != "owner-z" || agents[0].Desired || agents[0].OwnershipHold != "registered_owner_even_when_stopped") {
					t.Error("native ordering or stopped ownership changed")
				}
			} else {
				if agentErr == nil || agents != nil {
					t.Error("unsafe/incomplete registry became usable or partial agent inventory")
				}
				if scheduleErr == nil || schedules != nil {
					t.Error("unsafe/incomplete registry became complete empty schedule inventory")
				}
				for _, err := range []error{agentErr, scheduleErr} {
					if err != nil && strings.Contains(err.Error(), "private-") {
						t.Error("private registration failure exposed")
					}
				}
			}
			raw, _ := json.Marshal(agents)
			if strings.Contains(string(raw), "private-") {
				t.Error("private registration fields exposed")
			}
			if before != nil {
				after, _ := os.ReadFile(file)
				if !bytes.Equal(before, after) {
					t.Fatal("inventory changed registration")
				}
			}
			if kind == "missing" {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatal("inventory created absent registry")
				}
			}
		})
	}
}

func TestDiagnosticRegistrationScanSharesNativeIdentityAndOwnershipFingerprint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RADAR_AGENT_ROOT", root)
	workspace := filepath.Join(t.TempDir(), "workspace")
	diagnosticRegistration(t, root, "owner", workspace)
	diagnosticRegistration(t, root, "nested", filepath.Join(workspace, "nested"))
	native, err := registrations()
	if err != nil {
		t.Fatal(err)
	}
	observed, err := diagnosticRegistrations()
	if err != nil || !reflect.DeepEqual(native, observed) {
		t.Fatal("bounded discovery changed native registrations", err)
	}
	_, fingerprint, err := DiagnosticWorkspaceRemovalOwners(context.Background(), []string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	scanFingerprint, err := diagnosticRegistrationScan(context.Background(), nil)
	if err != nil || scanFingerprint != fingerprint {
		t.Fatal("shared scan changed ownership fingerprint", err)
	}
	observed[0].Entrypoint = "changed-returned-command"
	fresh, err := diagnosticRegistrations()
	if err != nil || !reflect.DeepEqual(fresh, native) {
		t.Fatal("returned registration changed later discovery", err)
	}
	for _, ctx := range []context.Context{func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(), delayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}} {
		visited := false
		fingerprint, err := diagnosticRegistrationScan(ctx, func(Registration) { visited = true })
		if err == nil || visited || fingerprint != [32]byte{} {
			t.Fatal("cancelled/expired scan produced usable evidence")
		}
	}
}

func TestDiagnosticRegistrationNativeDiscoveryPolicyUnchanged(t *testing.T) {
	for _, kind := range []string{"over_bound", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RADAR_AGENT_ROOT", root)
			reg := diagnosticRegistration(t, root, "owner", "/registered/workspace")
			path := filepath.Join(root, reg.ID, "registration.json")
			if kind == "over_bound" {
				data, _ := os.ReadFile(path)
				if err := os.WriteFile(path, append(data, bytes.Repeat([]byte(" "), 64*1024)...), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "registration")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if native, err := registrations(); err != nil || len(native) != 1 || native[0].ID != reg.ID {
				t.Fatal("diagnostic bounds changed native discovery policy", err)
			}
			if observed, err := diagnosticRegistrations(); err == nil || observed != nil {
				t.Fatal("diagnostic discovery accepted native-only acquisition policy")
			}
		})
	}
}

func TestDiagnosticRegistrationInventoriesDoNotWaitForFIFO(t *testing.T) {
	if kind := os.Getenv("RADAR_TEST_REGISTRATION_FIFO"); kind != "" {
		root := t.TempDir()
		t.Setenv("RADAR_AGENT_ROOT", root)
		reg := diagnosticRegistration(t, root, "worker", "/registered/workspace")
		file := filepath.Join(root, reg.ID, "registration.json")
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(file, 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		switch kind {
		case "agents":
			_, err = DiagnosticAgents("", "")
		case "schedules":
			_, err = DiagnosticAgentSchedules("", "", "")
		case "unselected_agents":
			_, err = DiagnosticAgents("/unrelated", "other")
		case "unselected_schedules":
			_, err = DiagnosticAgentSchedules("/unrelated", "other", "")
		}
		if err == nil {
			t.Fatal("unsafe registry became usable evidence")
		}
		return
	}
	for _, kind := range []string{"agents", "schedules", "unselected_agents", "unselected_schedules"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiagnosticRegistrationInventoriesDoNotWaitForFIFO$", "-test.count=1")
			cmd.Env = append(os.Environ(), "RADAR_TEST_REGISTRATION_FIFO="+kind)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("registration inventory blocked on FIFO; isolated child killed and collected")
			}
			if err != nil {
				t.Fatalf("child registration inventory: %s", output)
			}
		})
	}
}
