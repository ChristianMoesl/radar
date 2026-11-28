package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

func TestNativeStopObservationIndependentOfHandlerAndImmutable(t *testing.T) {
	for _, kind := range []string{"stopped", "still_listening", "shutdown_error", "guard_error", "cancelled", "expired", "definition_changed", "owner_live", "malformed_listener"} {
		t.Run(kind, func(t *testing.T) {
			reg, manager := stopPlanFixture(t)
			dir, _ := instanceDir(reg.ID)
			for _, name := range []string{"snapshot.json", "ingress.token", "collection.json", "schedules.json", "config.json", "ownership.json", "store.sqlite"} {
				if err := syscall.Mkfifo(filepath.Join(dir, name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var malformed atomic.Bool
			methods := func() []string { return nil }
			if kind == "still_listening" || kind == "shutdown_error" || kind == "malformed_listener" {
				methods = rawDiagnosticAgentPeer(t, reg, func(method string) string {
					if malformed.Load() || method == "shutdown" && kind == "shutdown_error" {
						return ""
					}
					if method == "hello" {
						return `{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"instanceId":"worker"}}`
					}
					if method == "snapshot" {
						return `{"jsonrpc":"2.0","id":1,"result":` + nativeStatusSnapshot(reg) + `}`
					}
					return `{"jsonrpc":"2.0","id":1,"result":{"stopping":true}}`
				})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var plan StopPlan
			guard := func(_ context.Context, execute func() error) error {
				err := execute()
				switch kind {
				case "guard_error":
					return errors.New("private-lost-response")
				case "cancelled":
					cancel()
				case "expired":
					plan.capture.expires = time.Now().Add(-time.Second)
				case "definition_changed":
					changed := reg
					changed.Desired, changed.Entrypoint = false, "private-other-definition"
					if err := atomicJSON(filepath.Join(dir, "registration.json"), changed); err != nil {
						t.Fatal(err)
					}
				case "owner_live":
					if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
						t.Fatal(err)
					}
				case "malformed_listener":
					malformed.Store(true)
				}
				return err
			}
			var err error
			plan, err = manager.PrepareStop(ctx, reg.ID, reg.Workspace, guard)
			if err != nil || plan.Observation() != (integration.AgentStopObservation{}) {
				t.Fatal("capture fabricated postconditions", err, plan.Observation())
			}
			copy := plan
			start := time.Now()
			err = plan.Execute(ctx)
			failed := kind == "guard_error" || kind == "shutdown_error" || kind == "cancelled" || kind == "expired"
			if (err != nil) != failed {
				t.Fatal("handler result changed or conflated with observation", err)
			}
			got := plan.Observation()
			if kind == "cancelled" || kind == "expired" || kind == "definition_changed" {
				if got != (integration.AgentStopObservation{}) {
					t.Fatal("cancelled/expired/replaced target acquired evidence", got)
				}
			} else {
				runner := integration.AgentStopRunnerAbsent
				if kind == "still_listening" || kind == "shutdown_error" {
					runner = integration.AgentStopRunnerPresent
				} else if kind == "owner_live" || kind == "malformed_listener" {
					runner = integration.AgentStopRunnerUnknown
				}
				if got.Registration != integration.AgentStopRegistrationStopped || got.Runner != runner || got.ObservedAt.Before(start) || got.ObservedAt.After(time.Now()) {
					t.Fatal("missing/false independent stop postcondition", got)
				}
			}
			if copy.Observation() != got {
				t.Fatal("copies lost immutable shared evidence")
			}
			frozen := got
			got.Registration = integration.AgentStopRegistrationOriginal
			calls := len(methods())
			if err := copy.Execute(context.Background()); err == nil {
				t.Fatal("observation restored one-use admission")
			}
			cancel()
			if err := os.Rename(dir, dir+".retained"); err != nil {
				t.Fatal(err)
			}
			if plan.Observation() != frozen || len(methods()) != calls {
				t.Fatal("inspection refreshed evidence/contacted a peer/mutated frozen result")
			}
			data, _ := json.Marshal(plan)
			if string(data) != "{}" {
				t.Fatal("private plan serialized")
			}
			data, _ = json.Marshal(frozen)
			if strings.Contains(string(data), "private") || strings.Contains(string(data), reg.ID) || strings.Contains(string(data), dir) || strings.Contains(string(data), "987654") {
				t.Fatal("private native observation leaked", string(data))
			}
		})
	}
}

func TestNativeStopObservationPreEffectRefusalAndConcurrentCopies(t *testing.T) {
	if (StopPlan{}).Observation() != (integration.AgentStopObservation{}) {
		t.Fatal("zero plan fabricated evidence")
	}
	for _, kind := range []string{"cancelled", "expired", "busy", "denied", "skipped", "drift", "concurrent"} {
		t.Run(kind, func(t *testing.T) {
			reg, manager := stopPlanFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			plan, err := manager.PrepareStop(context.Background(), reg.ID, reg.Workspace, func(_ context.Context, execute func() error) error {
				if kind == "denied" {
					return errors.New("private-denied")
				}
				if kind == "skipped" {
					return nil
				}
				if kind == "concurrent" {
					close(entered)
					<-release
				}
				return execute()
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "cancelled":
				cancel()
			case "expired":
				plan.capture.expires = time.Now().Add(-time.Second)
			case "busy":
				manager.mu.Lock()
				defer manager.mu.Unlock()
			case "drift":
				dir, _ := instanceDir(reg.ID)
				if err := atomicJSON(filepath.Join(dir, "registration.json"), reg); err != nil {
					t.Fatal(err)
				}
			case "concurrent":
				done := make(chan error, 1)
				go func() { done <- plan.Execute(ctx) }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("guard never entered")
				}
				var wg sync.WaitGroup
				for range 8 {
					wg.Go(func() {
						copy := plan
						if copy.Observation() != (integration.AgentStopObservation{}) || copy.Execute(ctx) == nil {
							t.Error("in-flight copy fabricated evidence/replayed")
						}
					})
				}
				wg.Wait()
				unblock()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if got := plan.Observation(); got.Registration != integration.AgentStopRegistrationStopped || got.Runner != integration.AgentStopRunnerAbsent || got.ObservedAt.IsZero() {
					t.Fatal("first executor did not publish observed result", got)
				}
				return
			}
			if err := plan.Execute(ctx); err == nil || plan.Observation() != (integration.AgentStopObservation{}) {
				t.Fatal("pre-effect refusal fabricated evidence", err, plan.Observation())
			}
		})
	}
}

func TestNativeStopObservationRejectsUnsafeAndIntervalDrift(t *testing.T) {
	for _, kind := range []string{"original", "stopped", "private_extension", "live_original_pid", "registration_missing", "registration_fifo", "registration_symlink", "registration_oversized", "registration_ambiguous", "changed_definition", "owner_invalid", "owner_fifo", "socket_symlink", "probe_error", "registration_replaced_during_probe", "owner_replaced_during_probe", "listener_appears_during_probe", "root_replaced_during_probe", "instance_replaced_during_probe", "cancel_during_probe", "absolute_deadline", "expire_during_capture"} {
		t.Run(kind, func(t *testing.T) {
			reg, manager := stopPlanFixture(t)
			plan, err := manager.PrepareStop(context.Background(), reg.ID, reg.Workspace, allowStopGuard)
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "registration.json")
			stopped := reg
			stopped.Desired = false
			if kind != "original" {
				if err := atomicJSON(path, stopped); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var observationCtx context.Context = ctx
			if kind == "absolute_deadline" {
				observationCtx = delayedDeadlineContext{Context: ctx, deadline: time.Now().Add(-time.Second)}
			}
			switch kind {
			case "private_extension":
				raw, _ := json.Marshal(stopped)
				raw = append(raw[:len(raw)-1], []byte(`,"private_extension":{"body":"private-value"}}`)...)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "live_original_pid":
				plan.capture.running, plan.capture.pid = true, os.Getpid()
			case "registration_missing", "registration_fifo", "registration_symlink":
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if kind == "registration_fifo" {
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				} else if kind == "registration_symlink" {
					if err := os.Symlink(path+".retained", path); err != nil {
						t.Fatal(err)
					}
				}
			case "registration_oversized":
				if err := os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "registration_ambiguous":
				if err := os.WriteFile(path, ambiguousRegistrationMembers(stopped)["registration_desired_duplicate"], 0600); err != nil {
					t.Fatal(err)
				}
			case "changed_definition":
				stopped.Entrypoint = "private-replacement"
				if err := atomicJSON(path, stopped); err != nil {
					t.Fatal(err)
				}
			case "owner_invalid":
				if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("private-invalid-owner"), 0600); err != nil {
					t.Fatal(err)
				}
			case "owner_fifo":
				if err := syscall.Mkfifo(filepath.Join(dir, "pid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "socket_symlink":
				socket, _ := socketPath(reg.ID)
				if err := os.Symlink(path, socket); err != nil {
					t.Fatal(err)
				}
			}
			captures, probes := 0, 0
			capture := func(id string) (stopFiles, Registration, error) {
				captures++
				files, reg, err := captureStopFiles(id)
				if kind == "expire_during_capture" {
					plan.capture.expires = time.Now().Add(-time.Second)
				}
				return files, reg, err
			}
			probe := func(ctx context.Context, r Registration) (protocol.AgentStatus, error) {
				probes++
				s, err := observeRunner(ctx, r)
				switch kind {
				case "probe_error":
					return s, errors.New("private-peer-error")
				case "registration_replaced_during_probe":
					if err := atomicJSON(path, stopped); err != nil {
						t.Fatal(err)
					}
				case "owner_replaced_during_probe":
					if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("987654"), 0600); err != nil {
						t.Fatal(err)
					}
				case "listener_appears_during_probe":
					nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
				case "root_replaced_during_probe", "instance_replaced_during_probe":
					target := dir
					if kind == "root_replaced_during_probe" {
						target = filepath.Dir(dir)
					}
					if err := os.Rename(target, target+".retained"); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					old := filepath.Join(target+".retained", "registration.json")
					if kind == "root_replaced_during_probe" {
						old = filepath.Join(target+".retained", reg.ID, "registration.json")
					}
					if err := os.Link(old, path); err != nil {
						t.Fatal(err)
					}
				case "cancel_during_probe":
					cancel()
				}
				return s, err
			}
			got := plan.capture.observeStop(observationCtx, capture, probe)
			switch kind {
			case "original", "stopped", "private_extension", "live_original_pid", "owner_invalid", "probe_error":
				registration, runner := integration.AgentStopRegistrationStopped, integration.AgentStopRunnerAbsent
				if kind == "original" {
					registration = integration.AgentStopRegistrationOriginal
				}
				if kind == "live_original_pid" || kind == "owner_invalid" || kind == "probe_error" {
					runner = integration.AgentStopRunnerUnknown
				}
				if got.Registration != registration || got.Runner != runner || got.ObservedAt.IsZero() {
					t.Fatal("unsafe/unknown owner evidence became absence or valid fact lost", got)
				}
			default:
				if got != (integration.AgentStopObservation{}) {
					t.Fatal("unsafe/changed/cancelled evidence remained usable", got)
				}
			}
			if kind == "absolute_deadline" && (captures != 0 || probes != 0) || kind == "expire_during_capture" && (captures != 1 || probes != 0) {
				t.Fatal("observation crossed absolute expiry", captures, probes)
			}
		})
	}
}
