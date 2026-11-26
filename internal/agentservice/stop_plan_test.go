package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

func stopPlanFixture(t *testing.T) (Registration, *Manager) {
	t.Helper()
	reg := probeFixture(t)
	reg.Desired = true
	dir, _ := instanceDir(reg.ID)
	if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
		t.Fatal(err)
	}
	prepared := 0
	return reg, nativeStatusManager(reg, &prepared)
}

func requireStopNoShutdown(t *testing.T, methods []string) {
	t.Helper()
	for _, method := range methods {
		if method != "hello" && method != "snapshot" {
			t.Errorf("refused/preview path dispatched %s", method)
		}
	}
}

func TestNativeStopPlanPreviewIsPrivateBoundedAndEffectFree(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(strconv.FormatBool(running), func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			for _, name := range []string{"snapshot.json", "ingress.token", "collection.json", "schedules.json", "config.json"} {
				if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			methods := func() []string { return nil }
			if running {
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			}
			before, _ := os.ReadFile(filepath.Join(dir, "registration.json"))
			m.retry[reg.ID] = retryState{attempts: 2}
			plan, err := m.PrepareStop(context.Background(), reg.ID, reg.Workspace)
			if err != nil || plan.Preview() != (StopPreview{ID: reg.ID, Desired: true, Running: running}) {
				t.Fatalf("unexpected preview %+v %v", plan.Preview(), err)
			}
			copy := plan.Preview()
			copy.ID, copy.Desired, copy.Running = "other", false, !running
			if plan.Preview().ID != reg.ID || !plan.Preview().Desired || plan.Preview().Running != running {
				t.Fatal("mutable preview changed retained native target")
			}
			encoded, err := json.Marshal(plan)
			if err != nil || string(encoded) != "{}" {
				t.Fatal("private executable capture serialized", string(encoded), err)
			}
			encoded, _ = json.Marshal(plan.Preview())
			if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "987654") || strings.Contains(string(encoded), reg.Workspace.Path) {
				t.Fatal("preview exposed private native evidence")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "registration.json"))
			if !bytes.Equal(before, after) || m.retry[reg.ID].attempts != 2 {
				t.Fatal("preview changed desired state/retry evidence")
			}
			requireStopNoShutdown(t, methods())
			if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
				t.Fatal("preview released registered ownership")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(os.Getenv("RADAR_SOCKET")), "leases")); !os.IsNotExist(err) {
				t.Fatal("preview acquired a workspace lease", err)
			}
		})
	}
}

func TestNativeStopPlanUsesExistingStopAndRetainsWorkspace(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(strconv.FormatBool(running), func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			methods := func() []string { return nil }
			if running {
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			}
			m.retry[reg.ID] = retryState{attempts: 2}
			plan, err := m.PrepareStop(context.Background(), reg.ID, reg.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			copy := plan
			if err := plan.Execute(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := copy.Execute(context.Background()); err == nil {
				t.Fatal("copied admission replayed")
			}
			got, err := load(reg.ID)
			if err != nil || got.Desired || got.Workspace != reg.Workspace || got.Entrypoint != reg.Entrypoint {
				t.Fatal("stop removed/rebound registration or retained desired state", got, err)
			}
			if _, ok := m.retry[reg.ID]; ok {
				t.Fatal("stop retained retry intent")
			}
			if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
				t.Fatal("stop released workspace ownership")
			}
			shutdowns := 0
			for _, method := range methods() {
				if method == "shutdown" {
					shutdowns++
				}
			}
			if shutdowns != map[bool]int{false: 0, true: 1}[running] {
				t.Fatal("unexpected native shutdown count", shutdowns)
			}
		})
	}
}

func TestNativeStopPlanRefusesInvalidScopeAndUnknownEvidence(t *testing.T) {
	for _, kind := range []string{"id", "workspace_id", "workspace_path", "workspace_mode", "noncanonical_path", "busy", "cancelled", "missing_registration", "registration_fifo", "registration_symlink", "registration_oversized", "registration_duplicate", "root_symlink", "instance_symlink", "owner_fifo", "owner_invalid", "owner_live_unreachable", "owner_symlink", "owner_oversized", "socket_symlink", "hello", "snapshot", "snapshot_pid", "workspace_unavailable"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			id, expected := reg.ID, reg.Workspace
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			methods := func() []string { return nil }
			write := func(name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "id":
				id = "../worker"
			case "workspace_id":
				expected.ID += "-other"
			case "workspace_path":
				expected.Path += "-other"
			case "workspace_mode":
				expected.Mode = "sbx"
			case "noncanonical_path":
				expected.Path += "/../workspace"
			case "busy":
				m.mu.Lock()
				defer m.mu.Unlock()
			case "cancelled":
				cancel()
			case "missing_registration":
				if err := os.Remove(filepath.Join(dir, "registration.json")); err != nil {
					t.Fatal(err)
				}
			case "registration_fifo", "registration_symlink":
				path := filepath.Join(dir, "registration.json")
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if kind == "registration_fifo" {
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(path+".retained", path); err != nil {
					t.Fatal(err)
				}
			case "root_symlink", "instance_symlink":
				path := dir
				if kind == "root_symlink" {
					path = filepath.Dir(dir)
				}
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".retained", path); err != nil {
					t.Fatal(err)
				}
			case "registration_oversized":
				write("registration.json", strings.Repeat(" ", protocol.AgentMaxFrameBytes+1))
			case "registration_duplicate":
				write("registration.json", `{"id":"worker","id":"worker"}`)
			case "owner_fifo":
				if err := syscall.Mkfifo(filepath.Join(dir, "pid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "owner_invalid":
				write("pid", "private-invalid-owner")
			case "owner_live_unreachable":
				write("pid", strconv.Itoa(os.Getpid()))
			case "owner_symlink":
				write("owner.retained", "987654")
				if err := os.Symlink(filepath.Join(dir, "owner.retained"), filepath.Join(dir, "pid")); err != nil {
					t.Fatal(err)
				}
			case "owner_oversized":
				write("pid", strings.Repeat("1", 65))
			case "socket_symlink":
				path, _ := socketPath(reg.ID)
				if err := os.Symlink(path+".absent", path); err != nil {
					t.Fatal(err)
				}
			case "hello":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"other"}`, nativeStatusSnapshot(reg))
			case "snapshot":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, "null")
			case "snapshot_pid":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, strings.Replace(nativeStatusSnapshot(reg), "987654", "4295954950", 1))
			case "workspace_unavailable":
				m.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
					return reg.Workspace, errors.New("private-provider-error")
				}}
			}
			before, _ := os.ReadFile(filepath.Join(dir, "registration.json.retained"))
			// Never open deliberately unsafe registration leaves in the assertion.
			if kind != "registration_fifo" && kind != "registration_symlink" {
				before, _ = os.ReadFile(filepath.Join(dir, "registration.json"))
			}
			plan, err := m.PrepareStop(ctx, id, expected)
			if err == nil || plan.capture != nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), reg.Workspace.Path) {
				t.Fatal("invalid capture admitted or leaked", err)
			}
			requireStopNoShutdown(t, methods())
			path := filepath.Join(dir, "registration.json")
			if kind == "registration_fifo" || kind == "registration_symlink" {
				path += ".retained"
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("refused preview changed registration")
			}
		})
	}
}

func TestNativeStopPlanConsumesBeforeRefusalAndRejectsDrift(t *testing.T) {
	for _, kind := range []string{"cancelled", "expired", "busy", "registration_bytes", "same_id_replacement", "instance_replacement", "root_change", "socket_change", "owner_added", "workspace_changed", "runner_started", "runner_pid_changed", "runner_invalid"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			methods := func() []string { return nil }
			pid := 987654
			if kind == "runner_pid_changed" || kind == "runner_invalid" {
				methods = probeServer(t, reg, func(method string) any {
					if method == "hello" {
						return map[string]any{"protocol": 1, "instanceId": reg.ID}
					}
					if kind == "runner_invalid" && pid != 987654 {
						return nil
					}
					return protocol.AgentSnapshot{Protocol: 1, InstanceID: reg.ID, PID: pid}
				})
			}
			plan, err := m.PrepareStop(context.Background(), reg.ID, reg.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			unlock := func() {}
			switch kind {
			case "cancelled":
				cancel()
			case "expired":
				plan.capture.expires = time.Now().Add(-time.Second)
			case "busy":
				m.mu.Lock()
				unlock = m.mu.Unlock
			case "registration_bytes":
				data, _ := os.ReadFile(filepath.Join(dir, "registration.json"))
				if err := os.WriteFile(filepath.Join(dir, "registration.json"), append(data, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			case "same_id_replacement":
				if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
					t.Fatal(err)
				}
			case "instance_replacement":
				if err := os.Rename(dir, dir+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(dir+".retained", "registration.json"), filepath.Join(dir, "registration.json")); err != nil {
					t.Fatal(err)
				}
			case "root_change":
				t.Setenv("RADAR_AGENT_ROOT", filepath.Dir(dir)+"-other")
			case "socket_change":
				other := filepath.Join(filepath.Dir(os.Getenv("RADAR_SOCKET")), "other")
				if err := os.Mkdir(other, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("RADAR_SOCKET", filepath.Join(other, "radar.sock"))
			case "owner_added":
				if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("987654"), 0600); err != nil {
					t.Fatal(err)
				}
			case "workspace_changed":
				changed := reg.Workspace
				changed.ID += "-other"
				m.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return changed, nil }}
			case "runner_started":
				methods = nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			case "runner_pid_changed", "runner_invalid":
				pid++
			}
			if err := plan.Execute(ctx); err == nil {
				unlock()
				t.Fatal("drift/refusal accepted")
			}
			unlock()
			if err := plan.Execute(context.Background()); err == nil {
				t.Fatal("refused admission restored")
			}
			requireStopNoShutdown(t, methods())
			data, _ := os.ReadFile(filepath.Join(dir, "registration.json"))
			var after Registration
			if json.Unmarshal(data, &after) == nil && !after.Desired {
				t.Fatal("pre-dispatch refusal changed desired state")
			}
		})
	}
}

func TestNativeStopPlanRevalidatesAtShutdownBoundary(t *testing.T) {
	reg, m := stopPlanFixture(t)
	dir, _ := instanceDir(reg.ID)
	observations := 0
	methods := probeServer(t, reg, func(method string) any {
		if method == "hello" {
			return map[string]any{"protocol": 1, "instanceId": reg.ID}
		}
		observations++
		if observations == 2 {
			data, _ := os.ReadFile(filepath.Join(dir, "registration.json"))
			if err := os.WriteFile(filepath.Join(dir, "registration.json"), append(data, ' '), 0600); err != nil {
				t.Error(err)
			}
		}
		return protocol.AgentSnapshot{Protocol: 1, InstanceID: reg.ID, PID: 987654}
	})
	plan, err := m.PrepareStop(context.Background(), reg.ID, reg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Execute(context.Background()); err == nil {
		t.Fatal("drift during native final observation admitted")
	}
	requireStopNoShutdown(t, methods())
	got, err := load(reg.ID)
	if err != nil || !got.Desired {
		t.Fatal("final refusal changed desired state", err)
	}
}

func TestNativeStopPlanPartialFailureNeverRestoresOrReplays(t *testing.T) {
	for _, kind := range []string{"lost_reply", "cancelled_after_dispatch", "unknown_owner_after_ack", "live_owner_after_ack"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			methods := rawDiagnosticAgentPeer(t, reg, func(method string) string {
				if method == "hello" {
					return `{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"instanceId":"worker"}}`
				}
				if method == "snapshot" {
					return `{"jsonrpc":"2.0","id":1,"result":` + nativeStatusSnapshot(reg) + `}`
				}
				switch kind {
				case "lost_reply":
					return ""
				case "cancelled_after_dispatch":
					cancel()
				case "live_owner_after_ack":
					if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
						t.Error(err)
					}
				case "unknown_owner_after_ack":
					if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("private-invalid-owner"), 0600); err != nil {
						t.Error(err)
					}
				}
				return `{"jsonrpc":"2.0","id":1,"result":{"stopping":true}}`
			})
			plan, err := m.PrepareStop(context.Background(), reg.ID, reg.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "live_owner_after_ack" {
				var timeout context.CancelFunc
				ctx, timeout = context.WithTimeout(ctx, 150*time.Millisecond)
				defer timeout()
			}
			if err := plan.Execute(ctx); err == nil {
				t.Fatal("partial/unknown outcome certified")
			}
			if err := plan.Execute(context.Background()); err == nil {
				t.Fatal("uncertain stop replayed")
			}
			got, err := load(reg.ID)
			if err != nil || got.Desired {
				t.Fatal("accepted stop rolled back or removed registration", err)
			}
			shutdowns := 0
			for _, method := range methods() {
				if method == "shutdown" {
					shutdowns++
				}
			}
			if shutdowns != 1 {
				t.Fatal("uncertain stop retried", shutdowns)
			}
			if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
				t.Fatal("partial stop released ownership")
			}
		})
	}
}

func TestNativeStopPlanConcurrentCopiesExecuteOnlyOnce(t *testing.T) {
	reg, m := stopPlanFixture(t)
	plan, err := m.PrepareStop(context.Background(), reg.ID, reg.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func(p StopPlan) { defer wg.Done(); results <- p.Execute(context.Background()) }(plan)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatal("admission was not shared across copies", succeeded)
	}
	if err := (StopPlan{}).Execute(context.Background()); err == nil || (StopPlan{}).Preview() != (StopPreview{}) {
		t.Fatal("zero plan admitted")
	}
}

func TestNativeStopPlanCaptureRejectsInFlightReplacementAndDeadline(t *testing.T) {
	for _, kind := range []string{"registration_replaced", "owner_changed", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			reg, m := stopPlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			methods := func() []string { return nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "deadline" {
				methods = rawDiagnosticAgentPeer(t, reg, func(string) string {
					time.Sleep(40 * time.Millisecond)
					return `{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"instanceId":"worker"}}`
				})
				var timeout context.CancelFunc
				ctx, timeout = context.WithTimeout(ctx, 10*time.Millisecond)
				defer timeout()
			} else {
				m.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
					if kind == "registration_replaced" {
						if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
							t.Error(err)
						}
					} else if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("987654"), 0600); err != nil {
						t.Error(err)
					}
					return reg.Workspace, nil
				}}
			}
			plan, err := m.PrepareStop(ctx, reg.ID, reg.Workspace)
			if err == nil || plan.capture != nil {
				t.Fatal("in-flight drift/deadline admitted")
			}
			requireStopNoShutdown(t, methods())
			got, err := load(reg.ID)
			if err != nil || !got.Desired {
				t.Fatal("refused capture changed desired state", err)
			}
		})
	}
}
