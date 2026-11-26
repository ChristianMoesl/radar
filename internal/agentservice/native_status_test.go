package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func nativeStatusSnapshot(reg Registration) string {
	value, _ := json.Marshal(protocol.AgentSnapshot{Protocol: protocol.AgentProtocolVersion, InstanceID: reg.ID, PID: 987654, DefinitionVersion: 1})
	return string(value)
}

func nativeStatusPeer(t *testing.T, reg Registration, hello, snapshot string) func() []string {
	t.Helper()
	return rawDiagnosticAgentPeer(t, reg, func(method string) string {
		result := `{"stopping":true}`
		switch method {
		case "hello":
			result = hello
		case "snapshot":
			result = snapshot
		}
		return `{"jsonrpc":"2.0","id":1,"result":` + result + `}`
	})
}

func nativeStatusManager(reg Registration, prepared *int) *Manager {
	return New(workspaceFixture{
		lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) { return reg.Workspace, nil },
		prepare: func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
			(*prepared)++
			return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, errors.New("fixture must not prepare a workspace")
		},
	})
}

func TestNativeAgentControlsRejectUncertainRunnerEvidence(t *testing.T) {
	validHello := `{"protocol":1,"instanceId":"worker"}`
	validSnapshot := nativeStatusSnapshot(Registration{ID: "worker"})
	cases := map[string]struct{ hello, snapshot string }{}
	for name, hello := range map[string]string{
		"wrong_protocol":             `{"protocol":99,"instanceId":"worker"}`,
		"wrong_identity":             `{"protocol":1,"instanceId":"other"}`,
		"missing_protocol":           `{"instanceId":"worker"}`,
		"null_identity":              `{"protocol":1,"instanceId":null}`,
		"duplicate_protocol":         `{"protocol":99,"protocol":1,"instanceId":"worker"}`,
		"equal_identity_duplicate":   `{"protocol":1,"instanceId":"worker","instanceId":"worker"}`,
		"escaped_identity_duplicate": `{"protocol":1,"\u0069nstanceId":"other","instanceId":"worker"}`,
		"folded_identity":            `{"protocol":1,"INSTANCEID":"worker"}`,
		"null":                       `null`,
	} {
		cases["hello_"+name] = struct{ hello, snapshot string }{hello, validSnapshot}
	}
	for name, snapshot := range map[string]string{
		"wrong_protocol":             strings.Replace(validSnapshot, `"protocol":1`, `"protocol":99`, 1),
		"wrong_identity":             strings.Replace(validSnapshot, `"worker"`, `"other"`, 1),
		"missing_identity":           strings.Replace(validSnapshot, `"instanceId":"worker",`, ``, 1),
		"null_identity":              strings.Replace(validSnapshot, `"worker"`, `null`, 1),
		"duplicate_protocol":         strings.Replace(validSnapshot, `"protocol":1`, `"protocol":99,"protocol":1`, 1),
		"escaped_identity_duplicate": strings.Replace(validSnapshot, `"instanceId":`, `"\u0069nstanceId":"other","instanceId":`, 1),
		"folded_identity":            strings.Replace(validSnapshot, `"instanceId":`, `"INSTANCEID":`, 1),
		"duplicate_pid":              strings.Replace(validSnapshot, `"pid":987654`, `"pid":1,"pid":987654`, 1),
		"equal_pid_duplicate":        strings.Replace(validSnapshot, `"pid":987654`, `"pid":987654,"pid":987654`, 1),
		"folded_pid":                 strings.Replace(validSnapshot, `"pid":`, `"PID":`, 1),
		"missing_pid":                strings.Replace(validSnapshot, `"pid":987654,`, ``, 1),
		"null_pid":                   strings.Replace(validSnapshot, `"pid":987654`, `"pid":null`, 1),
		"zero_pid":                   strings.Replace(validSnapshot, `"pid":987654`, `"pid":0`, 1),
		"negative_pid":               strings.Replace(validSnapshot, `"pid":987654`, `"pid":-1`, 1),
		"overflow_pid":               strings.Replace(validSnapshot, `"pid":987654`, `"pid":2147483648`, 1),
		"wrapped_live_pid":           strings.Replace(validSnapshot, `"pid":987654`, `"pid":`+strconv.FormatInt(1<<32+int64(os.Getpid()), 10), 1),
		"wrapped_dead_pid":           strings.Replace(validSnapshot, `"pid":987654`, `"pid":4295954950`, 1),
		"js_safe_max_pid":            strings.Replace(validSnapshot, `"pid":987654`, `"pid":9007199254740991`, 1),
		"unsafe_pid":                 strings.Replace(validSnapshot, `"pid":987654`, `"pid":9007199254740992`, 1),
		"string_pid":                 strings.Replace(validSnapshot, `"pid":987654`, `"pid":"987654"`, 1),
		"fraction_pid":               strings.Replace(validSnapshot, `"pid":987654`, `"pid":1.5`, 1),
		"null":                       `null`,
	} {
		cases["snapshot_"+name] = struct{ hello, snapshot string }{validHello, snapshot}
	}
	for name, evidence := range cases {
		t.Run(name, func(t *testing.T) {
			for _, operation := range []string{"inspect", "start", "stop", "restart", "remove", "emit", "interact", "conversation"} {
				t.Run(operation, func(t *testing.T) {
					reg := probeFixture(t) // Deliberately stopped, without a PID file.
					dir, _ := instanceDir(reg.ID)
					if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(validSnapshot), 0600); err != nil {
						t.Fatal(err)
					}
					methods := nativeStatusPeer(t, reg, evidence.hello, evidence.snapshot)
					prepared := 0
					manager := nativeStatusManager(reg, &prepared)
					value, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: operation, Params: json.RawMessage(`{}`)})
					if operation == "inspect" {
						var view protocol.AgentStatus
						if err != nil || json.Unmarshal(value, &view) != nil || view.Running || view.Error == "" {
							t.Error("uncertain status became healthy running/stopped evidence")
						}
					} else if err == nil {
						t.Error("uncertain runner evidence authorized native control")
					}
					if prepared != 0 {
						t.Error("uncertain runner evidence prepared/recovered its workspace")
					}
					for _, method := range methods() {
						if method != "hello" && method != "snapshot" {
							t.Errorf("uncertain runner evidence invoked %s", method)
						}
					}
					if _, err := load(reg.ID); err != nil {
						t.Error("uncertain runner evidence removed its registration", err)
					}
					if err := CheckWorkspaceRemoval(reg.Workspace.Path); err == nil {
						t.Error("uncertain runner evidence released the workspace ownership hold")
					}
				})
			}
		})
	}
}

func TestNativeAgentControlsRejectFailedHandshakeAndCancellation(t *testing.T) {
	for _, phase := range []string{"hello", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			for _, failure := range []string{"eof", "malformed", "rpc_error", "cancelled", "deadline"} {
				t.Run(failure, func(t *testing.T) {
					for _, operation := range []string{"start", "stop", "restart", "remove"} {
						t.Run(operation, func(t *testing.T) {
							reg := probeFixture(t)
							methods := rawDiagnosticAgentPeer(t, reg, func(method string) string {
								if phase == "snapshot" && method == "hello" {
									return `{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"instanceId":"worker"}}`
								}
								switch failure {
								case "eof":
									return ""
								case "rpc_error":
									return `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"private-error"}}`
								case "deadline":
									time.Sleep(40 * time.Millisecond)
								}
								return "{"
							})
							ctx, cancel := context.WithCancel(context.Background())
							defer cancel()
							if failure == "cancelled" {
								cancel()
							}
							if failure == "deadline" {
								var timeout context.CancelFunc
								ctx, timeout = context.WithTimeout(ctx, 10*time.Millisecond)
								defer timeout()
							}
							prepared := 0
							_, err := nativeStatusManager(reg, &prepared).Handle(ctx, protocol.AgentRequest{ID: reg.ID, Operation: operation})
							if err == nil || prepared != 0 {
								t.Error("failed handshake/cancellation became absence or recovery permission")
							}
							if _, err := load(reg.ID); err != nil {
								t.Error("failed handshake removed registration")
							}
							if failure == "cancelled" && len(methods()) != 0 {
								t.Error("pre-cancelled control contacted runner")
							}
							for _, method := range methods() {
								if method != "hello" && method != "snapshot" {
									t.Errorf("failed handshake invoked %s", method)
								}
							}
						})
					}
				})
			}
		})
	}
}

func TestNativeAgentMissingAndRefusedListenerRetainNormalStoppedLifecycle(t *testing.T) {
	for _, listener := range []string{"absent", "refused"} {
		t.Run(listener, func(t *testing.T) {
			reg := probeFixture(t)
			if listener == "refused" {
				path, _ := socketPath(reg.ID)
				peer, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
				if err != nil {
					t.Fatal(err)
				}
				peer.SetUnlinkOnClose(false)
				if err := peer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			prepared := 0
			manager := nativeStatusManager(reg, &prepared)
			if view := status(context.Background(), reg); view.Running || view.Error != "" {
				t.Fatal("known absent stopped runner became an error")
			}
			if err := stop(context.Background(), reg); err != nil {
				t.Fatal(err)
			}
			if err := manager.start(context.Background(), reg, protocol.AgentRequest{}); err == nil || prepared != 1 {
				t.Fatal("known absent runner lost its native recovery path")
			}
			if _, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: "remove"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeAgentValidStatusKeepsPrivateNativePayloadAndNoDiagnosticPIDRequirement(t *testing.T) {
	reg := probeFixture(t)
	snapshot := strings.Replace(nativeStatusSnapshot(reg), `"tasks":null`, `"tasks":[{"body":"private-task"}]`, 1)
	methods := nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker","extension":{"anything":true}}`, snapshot)
	view := status(context.Background(), reg)
	if !view.Running || view.Error != "" || view.Snapshot == nil || !strings.Contains(string(view.Snapshot.Tasks), "private-task") {
		t.Fatal("valid native status lost its native payload")
	}
	if !reflect.DeepEqual(methods(), []string{"hello", "snapshot"}) {
		t.Fatal(methods())
	}
	// Diagnostic projections deliberately do not depend on private process data.
	var projected diagnosticRunnerSnapshot
	if err := json.Unmarshal([]byte(strings.Replace(snapshot, `"pid":987654,`, ``, 1)), &projected); err != nil || projected.projection(reg.ID) == nil {
		t.Fatal("native PID guard changed the diagnostic contract")
	}
}

func TestNativeAgentSnapshotPIDFitsNativeProcessSelectors(t *testing.T) {
	for _, value := range []struct {
		pid   string
		valid bool
	}{
		{"1", true}, {"2147483647", true},
		{"2147483648", false}, {"4294967296", false},
		{strconv.FormatInt(1<<32+int64(os.Getpid()), 10), false},
		{"4295954950", false}, {"9007199254740991", false}, {"9223372036854775807", false},
	} {
		t.Run(value.pid, func(t *testing.T) {
			reg := probeFixture(t)
			raw := strings.Replace(nativeStatusSnapshot(reg), `"pid":987654`, `"pid":`+value.pid, 1)
			snapshot, err := decodeRunnerSnapshot([]byte(raw), reg.ID)
			if (err == nil) != value.valid || (snapshot != nil) != value.valid {
				t.Error("native snapshot PID escaped the positive signed-32-bit boundary", err)
			}
			// The same native decoder guards cached presentation. Rejected
			// process evidence must not return through the checkpoint fallback.
			dir, _ := instanceDir(reg.ID)
			if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			view := status(context.Background(), reg)
			if view.Running || (view.Snapshot != nil) != value.valid {
				t.Error("native cached snapshot bypassed process-selector validation")
			}
			// Diagnostics deliberately ignore PID: the public counters must not
			// inherit native private-process requirements or perform PID probes.
			var projected diagnosticRunnerSnapshot
			if err := json.Unmarshal([]byte(raw), &projected); err != nil || projected.projection(reg.ID) == nil {
				t.Error("native process-selector validation changed diagnostic projection", err)
			}
		})
	}
}

func TestNativeAgentOutOfRangeSnapshotCannotDispatchShutdown(t *testing.T) {
	for _, pid := range []string{"2147483648", "4295954950", strconv.FormatInt(1<<32+int64(os.Getpid()), 10), "9007199254740991"} {
		t.Run(pid, func(t *testing.T) {
			reg := probeFixture(t) // No independent PID record to mask the live-path gap.
			snapshot := strings.Replace(nativeStatusSnapshot(reg), `"pid":987654`, `"pid":`+pid, 1)
			methods := nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, snapshot)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
			defer cancel()
			if err := stop(ctx, reg); err == nil {
				t.Error("shutdown acknowledgement certified an out-of-range process selector as stopped")
			}
			if !reflect.DeepEqual(methods(), []string{"hello", "snapshot"}) {
				t.Error("invalid process evidence dispatched shutdown", methods())
			}
		})
	}
}

func TestNativeAgentShutdownCannotIgnoreIndependentLiveOwner(t *testing.T) {
	reg := probeFixture(t)
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	dir, _ := instanceDir(reg.ID)
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	// A dead PID claimed by the peer is not independent proof its owner stopped.
	methods := nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := stop(ctx, reg); err == nil {
		t.Fatal("shutdown acknowledgement and unrelated dead PID became stopped-owner evidence")
	}
	if !runnerProcessAlive(child.Process.Pid) || !reflect.DeepEqual(methods(), []string{"hello", "snapshot", "shutdown"}) {
		t.Fatal("stop killed owner or retried uncertain shutdown", methods())
	}
}

func TestNativeAgentKnownMissingListenerCannotReleaseLiveOwner(t *testing.T) {
	reg := probeFixture(t)
	dir, _ := instanceDir(reg.ID)
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	prepared := 0
	manager := nativeStatusManager(reg, &prepared)
	if err := manager.start(context.Background(), reg, protocol.AgentRequest{}); err == nil || prepared != 0 {
		t.Fatal("missing socket authorized workspace recovery under a live owner")
	}
	if err := stop(context.Background(), reg); err == nil {
		t.Fatal("missing socket ignored its independent live owner")
	}
	if _, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: "remove"}); err == nil {
		t.Fatal("missing socket released its live ownership hold")
	}
	if _, err := load(reg.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAgentCachedSnapshotCannotReplaceLiveOwnershipEvidence(t *testing.T) {
	for _, identity := range []string{"worker", "other"} {
		t.Run(identity, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(nativeStatusSnapshot(Registration{ID: identity})), 0600); err != nil {
				t.Fatal(err)
			}
			view := status(context.Background(), reg)
			if view.Running || view.Error != "" || (view.Snapshot != nil) != (identity == reg.ID) {
				t.Fatal("cache became live evidence or crossed registration identity")
			}
			if err := stop(context.Background(), reg); err != nil {
				t.Fatal("cached presentation overrode known native absence", err)
			}
		})
	}
}
