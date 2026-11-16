package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"radar/internal/protocol"
)

func probeFixture(t *testing.T) Registration {
	t.Helper()
	root := agentRuntimeTestDir(t)
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(root, "agents"))
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "radar.sock"))
	return diagnosticRegistration(t, os.Getenv("RADAR_AGENT_ROOT"), "worker", filepath.Join(root, "workspace"))
}

func probeServer(t *testing.T, reg Registration, result func(string) any) func() []string {
	t.Helper()
	path, _ := socketPath(reg.ID)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	methods := []string{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req struct {
				Method string `json:"method"`
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			if json.NewDecoder(conn).Decode(&req) == nil {
				mu.Lock()
				methods = append(methods, req.Method)
				mu.Unlock()
				value := result(req.Method)
				_ = json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": value})
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, methods...) }
}

func probeSnapshot(reg Registration) map[string]any {
	return map[string]any{"protocol": protocol.AgentProtocolVersion, "instanceId": reg.ID, "definitionVersion": 4, "pending": 7, "pid": 987654, "problem": "private-error", "presentation": map[string]any{"attention": true, "revision": 9, "title": "private-title", "summary": "private-summary", "actions": []string{"private-action"}, "conversations": []string{"private-conversation"}}, "tasks": []string{"private-task"}, "recent": []string{"private-event"}}
}

func TestDiagnosticAgentProbeOnlyNativeMethodsAndPublicCounters(t *testing.T) {
	reg := probeFixture(t)
	dir, _ := instanceDir(reg.ID)
	for _, name := range []string{"ingress.token", "snapshot.json", "collection.json", "schedules.json"} {
		// Unsafe private files must not be opened as a fallback or precondition.
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(dir, "registration.json"))
	methods := probeServer(t, reg, func(method string) any {
		if method == "hello" {
			return map[string]any{"protocol": 1, "instanceId": reg.ID, "private": "private-hello"}
		}
		return probeSnapshot(reg)
	})
	view, err := ProbeDiagnosticAgent(context.Background(), reg.Workspace.Path, reg.ID)
	if err != nil || view.Desired || !view.Registered || view.RunnerEvidence != "reachable_matching_protocol_identity" || view.SnapshotEvidence != "fresh_runner_snapshot" || view.Problem != "none" || view.Snapshot == nil || view.Snapshot.Pending != 7 || view.Snapshot.Revision != 9 || view.Snapshot.DefinitionVersion != 4 || !view.Snapshot.Attention || !view.Snapshot.Problem || view.LeaseEvidence != "unknown_not_inspected" || view.OwnershipHold != "registered_owner_even_when_stopped" {
		t.Fatalf("%+v %v", view, err)
	}
	got, _ := json.Marshal(view)
	if strings.Contains(string(got), "private") || strings.Contains(string(got), "987654") {
		t.Fatalf("private data exposed: %s", got)
	}
	called := methods()
	if len(called) != 2 || called[0] != "hello" || called[1] != "snapshot" {
		t.Fatal(called)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "registration.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("probe changed registration")
	}
	entries, _ := os.ReadDir(filepath.Dir(os.Getenv("RADAR_SOCKET")))
	for _, entry := range entries {
		if entry.Name() == "leases" {
			t.Fatal("probe acquired a lease")
		}
	}
}

func TestDiagnosticAgentProbeUnreachableNeverUsesCheckpointOrInfersStopped(t *testing.T) {
	reg := probeFixture(t)
	dir, _ := instanceDir(reg.ID)
	if err := atomicJSON(filepath.Join(dir, "snapshot.json"), probeSnapshot(reg)); err != nil {
		t.Fatal(err)
	}
	view, err := ProbeDiagnosticAgent(context.Background(), "", reg.ID)
	if err != nil || view.RunnerEvidence != "unknown_not_reached" || view.Snapshot != nil || view.SnapshotEvidence != "unavailable" || view.Problem != "unavailable" {
		t.Fatalf("%+v %v", view, err)
	}
}

func TestDiagnosticAgentProbeScopeAndCancellationBeforeSocket(t *testing.T) {
	reg := probeFixture(t)
	methods := probeServer(t, reg, func(string) any { return nil })
	for _, tc := range []struct{ scope, id string }{{reg.Workspace.Path + "-other", reg.ID}, {"", "../worker"}, {"", "absent"}} {
		if _, err := ProbeDiagnosticAgent(context.Background(), tc.scope, tc.id); err == nil {
			t.Fatal("invalid scope/parent accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeDiagnosticAgent(ctx, "", reg.ID); err == nil {
		t.Fatal("pre-cancel accepted")
	}
	if len(methods()) != 0 {
		t.Fatal("out-of-scope/cancelled socket contact")
	}
}

func TestDiagnosticAgentProbeRejectsIdentityAndMalformedLiveSnapshot(t *testing.T) {
	for _, kind := range []string{"hello_identity", "hello_protocol", "snapshot_identity", "snapshot_protocol", "negative_pending", "missing_pending", "missing_attention", "missing_revision", "negative_revision", "missing_definition", "zero_definition", "oversized", "null"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			methods := probeServer(t, reg, func(method string) any {
				if method == "hello" {
					v := map[string]any{"protocol": 1, "instanceId": reg.ID}
					if kind == "hello_identity" {
						v["instanceId"] = "sibling"
					}
					if kind == "hello_protocol" {
						v["protocol"] = 99
					}
					return v
				}
				v := probeSnapshot(reg)
				switch kind {
				case "snapshot_identity":
					v["instanceId"] = "sibling"
				case "snapshot_protocol":
					v["protocol"] = 99
				case "negative_pending":
					v["pending"] = -1
				case "missing_pending":
					delete(v, "pending")
				case "missing_attention":
					delete(v["presentation"].(map[string]any), "attention")
				case "missing_revision":
					delete(v["presentation"].(map[string]any), "revision")
				case "negative_revision":
					v["presentation"].(map[string]any)["revision"] = -1
				case "missing_definition":
					delete(v, "definitionVersion")
				case "zero_definition":
					v["definitionVersion"] = 0
				case "oversized":
					v["private"] = strings.Repeat("x", protocol.AgentMaxFrameBytes)
				case "null":
					return nil
				}
				return v
			})
			view, err := ProbeDiagnosticAgent(context.Background(), "", reg.ID)
			if err != nil || view.Snapshot != nil || view.SnapshotEvidence != "unavailable" || view.Problem == "none" {
				t.Fatalf("%+v %v", view, err)
			}
			if strings.HasPrefix(kind, "hello_") && (len(methods()) != 1 || view.Problem != "identity_or_protocol_mismatch" || view.RunnerEvidence != "unknown_not_reached") {
				t.Fatalf("invalid hello followed: %+v %v", view, methods())
			}
		})
	}
}

func TestDiagnosticAgentProbeDeadlineAndRegistrationDrift(t *testing.T) {
	for _, kind := range []string{"deadline", "changed", "replaced", "removed"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "registration.json")
			probeServer(t, reg, func(method string) any {
				if method == "hello" {
					return map[string]any{"protocol": 1, "instanceId": reg.ID}
				}
				switch kind {
				case "deadline":
					time.Sleep(60 * time.Millisecond)
				case "changed":
					reg.Desired = true
					_ = atomicJSON(path, reg)
				case "replaced":
					_ = atomicJSON(path, reg)
				case "removed":
					_ = os.Remove(path)
				}
				return probeSnapshot(reg)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if kind != "deadline" {
				ctx = context.Background()
			}
			start := time.Now()
			view, err := ProbeDiagnosticAgent(ctx, "", reg.ID)
			if kind == "deadline" {
				if err != nil || view.Problem != "cancelled_or_timed_out" || view.Snapshot != nil || view.RunnerEvidence != "unknown_not_reached" || time.Since(start) > time.Second {
					t.Fatalf("%+v %v", view, err)
				}
			} else if err == nil {
				t.Fatalf("registration drift accepted: %+v", view)
			}
		})
	}
}

func TestDiagnosticAgentProbeRegistrationBoundsAndNativeDecoder(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory", "oversized", "invalid", "wrong_identity", "relative_workspace"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "registration.json")
			data, _ := os.ReadFile(path)
			_ = os.Remove(path)
			switch kind {
			case "symlink":
				_ = os.WriteFile(path+".private", data, 0600)
				_ = os.Symlink(path+".private", path)
			case "fifo":
				_ = syscall.Mkfifo(path, 0600)
			case "directory":
				_ = os.Mkdir(path, 0700)
			case "oversized":
				_ = os.WriteFile(path, append(data, bytes.Repeat([]byte(" "), 64*1024)...), 0600)
			case "invalid":
				_ = os.WriteFile(path, []byte("{"), 0600)
			case "wrong_identity":
				reg.ID = "sibling"
				_ = atomicJSON(path, reg)
			case "relative_workspace":
				reg.Workspace.Path = "relative"
				_ = atomicJSON(path, reg)
			}
			if _, err := ProbeDiagnosticAgent(context.Background(), "", "worker"); err == nil {
				t.Fatal("unsafe registration accepted")
			}
		})
	}
}

// Simulate an inherited context whose timer notification has not run yet even
// though its socket deadline has elapsed. Err alone cannot classify this case.
type delayedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestDiagnosticAgentProbeExpiredSocketDeadlineIsUnknownBeforeContextTimer(t *testing.T) {
	reg := probeFixture(t)
	probeServer(t, reg, func(method string) any {
		if method == "hello" {
			return map[string]any{"protocol": 1, "instanceId": reg.ID}
		}
		time.Sleep(250 * time.Millisecond)
		return probeSnapshot(reg)
	})
	ctx := delayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(150 * time.Millisecond)}
	view, err := ProbeDiagnosticAgent(ctx, "", reg.ID)
	if err != nil || ctx.Err() != nil || view.Problem != "cancelled_or_timed_out" || view.RunnerEvidence != "unknown_not_reached" || view.Snapshot != nil {
		t.Fatalf("expired transport was usable live evidence: %+v %v", view, err)
	}
}
