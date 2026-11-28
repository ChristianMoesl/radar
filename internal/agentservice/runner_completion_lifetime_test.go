package agentservice

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type completionLifetimeContext struct {
	context.Context
	err func() error
}

func (c completionLifetimeContext) Err() error { return c.err() }

func TestNativeRunnerCompletionClosesBeforeReturnOrPanic(t *testing.T) {
	for _, kind := range []string{"missing", "native_error", "cancelled", "provider_panic", "completion_panic", "healthy", "binding_error"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			path := filepath.Join(dir, "registration.json")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var retained func(context.Context, protocol.AgentWorkspaceBinding) error
			providerPanic, callbackPanic := &struct{ name string }{"provider"}, &struct{ name string }{"callback"}
			manager := New(runnerCompletionWorkspace{binding: reg.Workspace, prepare: func(gotCtx context.Context, req integration.AgentWorkspaceRequest) (binding protocol.AgentWorkspaceBinding, plan integration.WorkspaceReconcilePlan, err error) {
				retained = req.Complete
				if retained == nil {
					t.Fatal("missing native completion")
				}
				switch kind {
				case "native_error":
					return reg.Workspace, plan, errors.New("fixture native failure before completion")
				case "cancelled":
					cancel()
				case "provider_panic":
					panic(providerPanic)
				case "completion_panic":
					// A buggy provider may recover a continuation panic. This must not
					// turn an interrupted callback into accepted startup or a retry.
					binding = reg.Workspace
					defer func() {
						if got := recover(); got != callbackPanic {
							t.Errorf("callback panic changed: %v", got)
						}
					}()
					_ = retained(completionLifetimeContext{Context: gotCtx, err: func() error { panic(callbackPanic) }}, binding)
				case "healthy":
					nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
					return reg.Workspace, plan, retained(gotCtx, reg.Workspace)
				case "binding_error":
					changed := reg.Workspace
					changed.ID = "different"
					// Deliberately swallow the callback failure.
					_ = retained(gotCtx, changed)
				}
				return reg.Workspace, plan, nil
			}})
			manager.retry[reg.ID] = retryState{attempts: 3, next: time.Unix(100, 0), problem: "previous failure"}
			var handleErr error
			func() {
				defer func() {
					got := recover()
					if kind == "provider_panic" {
						if got != providerPanic {
							t.Errorf("provider panic changed: %v", got)
						}
					} else if got != nil {
						t.Errorf("unexpected panic: %v", got)
					}
				}()
				_, handleErr = manager.Handle(ctx, protocol.AgentRequest{ID: reg.ID, Operation: "start"})
			}()
			if kind == "healthy" {
				if handleErr != nil {
					t.Fatal(handleErr)
				}
			} else if kind != "provider_panic" && handleErr == nil {
				t.Error("incomplete startup returned success")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			retry, hadRetry := manager.retry[reg.ID]
			currentReg, loadErr := load(reg.ID)
			if loadErr != nil || currentReg.Desired != (kind == "healthy") || hadRetry != (kind != "healthy") {
				t.Error("refused/interrupted completion changed desired or retry intent", loadErr)
			}
			if kind != "healthy" {
				nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
			}
			// This is intentionally a fresh context: an escaped callback must
			// not revive even when its caller's context has already cancelled.
			for attempt := 0; attempt < 2; attempt++ {
				if err := retained(context.Background(), reg.Workspace); err == nil {
					t.Error("escaped completion accepted after Handle returned/unwound")
				}
			}
			after, err := os.ReadFile(path)
			current, statErr := os.Lstat(path)
			if err != nil || statErr != nil || !bytes.Equal(before, after) || !os.SameFile(info, current) {
				t.Error("escaped completion performed desired-state persistence", err, statErr)
			}
			if got, ok := manager.retry[reg.ID]; ok != hadRetry || got != retry {
				t.Error("escaped completion changed retry intent")
			}
			if !manager.mu.TryLock() {
				t.Fatal("manager serialization leaked after return/panic")
			}
			manager.mu.Unlock()
		})
	}
}

func TestNativeRunnerCompletionDrainsBeforeManagerUnlock(t *testing.T) {
	reg := probeFixture(t)
	entered, release, providerReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	callbackDone, returned := make(chan error, 1), make(chan error, 1)
	var retained func(context.Context, protocol.AgentWorkspaceBinding) error
	manager := New(runnerCompletionWorkspace{binding: reg.Workspace, prepare: func(ctx context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
		defer close(providerReturned)
		retained = req.Complete
		nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
		gated := completionLifetimeContext{Context: ctx, err: func() error {
			enterOnce.Do(func() { close(entered) })
			<-release
			return nil
		}}
		go func() { callbackDone <- retained(gated, reg.Workspace) }()
		<-entered
		return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
	}})
	go func() {
		_, err := manager.Handle(context.Background(), protocol.AgentRequest{ID: reg.ID, Operation: "start"})
		returned <- err
	}()
	select {
	case <-providerReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not return with an already-entered callback")
	}
	returnedEarly := false
	select {
	case <-returned:
		returnedEarly = true
		t.Error("Handle returned before its entered callback drained")
	case <-time.After(25 * time.Millisecond):
	}
	if manager.mu.TryLock() {
		manager.mu.Unlock()
		t.Error("manager unlocked while its callback could still persist desired state")
	}
	unblock()
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatal("accepted callback failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not drain")
	}
	if !returnedEarly {
		select {
		case err := <-returned:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Handle did not return after draining callback")
		}
	}
	if err := retained(context.Background(), reg.Workspace); err == nil {
		t.Fatal("drained callback remained open")
	}
	if !manager.mu.TryLock() {
		t.Fatal("drained callback leaked manager serialization")
	}
	manager.mu.Unlock()
}
