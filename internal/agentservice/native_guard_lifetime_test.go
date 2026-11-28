package agentservice

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"radar/internal/protocol"
)

func guardLifetimePlan(t *testing.T, operation string, guard func(context.Context, func() error) error) (Registration, *Manager, func(context.Context) error) {
	t.Helper()
	if operation == "stop" {
		reg, manager := stopPlanFixture(t)
		plan, err := manager.PrepareStop(context.Background(), reg.ID, reg.Workspace, guard)
		if err != nil {
			t.Fatal(err)
		}
		return reg, manager, plan.Execute
	}
	reg, manager := removePlanFixture(t)
	writeRemovalSchedule(t, reg)
	plan, err := manager.PrepareRemove(context.Background(), reg.ID, reg.Workspace, guard)
	if err != nil {
		t.Fatal(err)
	}
	return reg, manager, plan.Execute
}

func TestNativeGuardLifetimeClosesWithoutRelyingOnCallerCancellation(t *testing.T) {
	for _, kind := range []string{"guard_panic", "recovered_effect_panic", "allowed", "effect_error"} {
		t.Run(kind, func(t *testing.T) {
			var retained func() error
			calls := 0
			marker := &struct{ name string }{"native guard"}
			var result error
			func() {
				defer func() {
					got := recover()
					if kind == "guard_panic" && got != marker || kind != "guard_panic" && got != nil {
						t.Errorf("guard panic changed: %v", got)
					}
				}()
				result = runNativeGuard(context.Background(), func(_ context.Context, execute func() error) error {
					retained = execute
					if kind == "guard_panic" {
						panic(marker)
					}
					if kind == "recovered_effect_panic" {
						defer func() {
							if got := recover(); got != marker {
								t.Errorf("effect panic changed: %v", got)
							}
						}()
					}
					return execute()
				}, func() error {
					calls++
					if kind == "recovered_effect_panic" {
						panic(marker)
					}
					if kind == "effect_error" {
						return stopPlanUnavailable()
					}
					return nil
				})
			}()
			if kind != "guard_panic" && (result == nil) != (kind == "allowed") {
				t.Error("interrupted native callback became successful completion", result)
			}
			before := calls
			if err := retained(); err == nil || calls != before {
				t.Error("late callback executed without manager admission")
			}
		})
	}
}

func TestNativeGuardLifetimeClosesOnPanicAndRejectsRecoveredEffectPanic(t *testing.T) {
	for _, operation := range []string{"stop", "remove"} {
		t.Run(operation, func(t *testing.T) {
			for _, kind := range []string{"guard_panic", "recovered_effect_panic", "allowed"} {
				t.Run(kind, func(t *testing.T) {
					var retained func() error
					var manager *Manager
					marker := &struct{ name string }{"private fixture panic"}
					guard := func(_ context.Context, execute func() error) error {
						retained = execute
						if kind == "guard_panic" {
							panic(marker)
						}
						if kind == "recovered_effect_panic" {
							// Panic in the original native callback's final identity
							// check, before effects. Recovering it is not completion.
							original := manager.workspace
							manager.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
								panic(marker)
							}}
							defer func() {
								manager.workspace = original
								if got := recover(); got != marker {
									t.Errorf("effect panic changed: %v", got)
								}
							}()
						}
						return execute()
					}
					reg, m, execute := guardLifetimePlan(t, operation, guard)
					manager = m
					dir, _ := instanceDir(reg.ID)
					manager.retry[reg.ID] = retryState{attempts: 3, next: time.Unix(100, 0), problem: "prior private failure"}
					var executeErr error
					func() {
						defer func() {
							got := recover()
							if kind == "guard_panic" {
								if got != marker {
									t.Errorf("guard panic changed: %v", got)
								}
							} else if got != nil {
								t.Errorf("unexpected panic: %v", got)
							}
						}()
						executeErr = execute(context.Background())
					}()
					if kind == "allowed" && executeErr != nil {
						t.Fatal(executeErr)
					}
					if kind == "recovered_effect_panic" && (executeErr == nil || (executeErr.Error() != stopPlanUnavailable().Error() && executeErr.Error() != removePlanUnavailable().Error())) {
						t.Error("recovered effect panic became success or disclosed private evidence", executeErr)
					}
					// Snapshot AFTER return/unwinding: accepted effects are never
					// rolled back, but no escaped callback may add later effects.
					paths := []string{filepath.Join(dir, "registration.json"), schedulePath(reg)}
					before := make([][]byte, len(paths))
					infos := make([]os.FileInfo, len(paths))
					for i, path := range paths {
						before[i], _ = os.ReadFile(path)
						infos[i], _ = os.Lstat(path)
					}
					retry, hadRetry := manager.retry[reg.ID]
					if !manager.mu.TryLock() {
						t.Fatal("manager serialization leaked after return/panic")
					}
					for attempt := 0; attempt < 2; attempt++ {
						if err := retained(); err == nil {
							t.Error("callback accepted after native plan returned/unwound")
						}
					}
					manager.mu.Unlock()
					for i, path := range paths {
						after, _ := os.ReadFile(path)
						info, _ := os.Lstat(path)
						if !bytes.Equal(before[i], after) || !sameStopFile(infos[i], info) {
							t.Error("escaped callback persisted desired/schedule intent or removed registration")
						}
					}
					if got, ok := manager.retry[reg.ID]; ok != hadRetry || got != retry {
						t.Error("escaped callback changed retry evidence")
					}
					if err := execute(context.Background()); err == nil {
						t.Error("interrupted/completed one-use plan replayed")
					}
				})
			}
		})
	}
}

func TestNativeGuardLifetimeDrainsBeforeManagerUnlockIncludingPanic(t *testing.T) {
	for _, operation := range []string{"stop", "remove"} {
		t.Run(operation, func(t *testing.T) {
			for _, kind := range []string{"return", "panic"} {
				t.Run(kind, func(t *testing.T) {
					entered, release, guardReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var enterOnce, releaseOnce sync.Once
					unblock := func() { releaseOnce.Do(func() { close(release) }) }
					defer unblock()
					callbackDone := make(chan error, 1)
					type outcome struct {
						err   error
						panic any
					}
					returned := make(chan outcome, 1)
					marker := &struct{ name string }{"guard"}
					var retained func() error
					guard := func(_ context.Context, execute func() error) error {
						defer close(guardReturned)
						retained = execute
						go func() { callbackDone <- execute() }()
						<-entered
						if kind == "panic" {
							panic(marker)
						}
						return nil
					}
					reg, manager, execute := guardLifetimePlan(t, operation, guard)
					manager.workspace = workspaceFixture{lookup: func(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
						// Plan preflight precedes guard entry; only gate once its
						// execution callback has actually entered the native body.
						if retained != nil {
							enterOnce.Do(func() { close(entered) })
							<-release
						}
						return reg.Workspace, nil
					}}
					go func() {
						var result outcome
						defer func() { result.panic = recover(); returned <- result }()
						result.err = execute(context.Background())
					}()
					select {
					case <-guardReturned:
					case <-time.After(3 * time.Second):
						t.Fatal("guard did not return with an entered callback")
					}
					var result outcome
					returnedEarly := false
					select {
					case result = <-returned:
						returnedEarly = true
						t.Error("plan returned/unwound before its entered callback drained")
					case <-time.After(25 * time.Millisecond):
					}
					if manager.mu.TryLock() {
						manager.mu.Unlock()
						t.Error("manager unlocked while its callback could still execute")
					}
					unblock()
					select {
					case err := <-callbackDone:
						if err != nil {
							t.Fatal("already entered callback did not finish", err)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("callback did not drain")
					}
					if !returnedEarly {
						select {
						case result = <-returned:
						case <-time.After(3 * time.Second):
							t.Fatal("plan did not finish after callback drain")
						}
					}
					if kind == "panic" && result.panic != marker || kind == "return" && (result.err != nil || result.panic != nil) {
						t.Error("native result/panic changed", result)
					}
					if err := retained(); err == nil {
						t.Error("drained callback remained open")
					}
					if !manager.mu.TryLock() {
						t.Fatal("drained plan leaked manager serialization")
					}
					manager.mu.Unlock()
				})
			}
		})
	}
}
