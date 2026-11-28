package agentservice

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// This fixture deliberately does not implement the default test double's
// automatic completion. A provider must never cause a fallback launch after
// omitting, repeating or swallowing a failed native continuation.
type runnerCompletionWorkspace struct {
	binding protocol.AgentWorkspaceBinding
	prepare func(context.Context, integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error)
}

func (w runnerCompletionWorkspace) AgentWorkspace(context.Context, string) (protocol.AgentWorkspaceBinding, error) {
	return w.binding, nil
}
func (w runnerCompletionWorkspace) PrepareAgentWorkspace(ctx context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
	return w.prepare(ctx, req)
}

func TestNativeRunnerCompletionHasNoPostPreparationFallback(t *testing.T) {
	for _, kind := range []string{"healthy", "native_error_after_completion", "missing_completion", "repeated_completion", "binding_drift", "cancelled_completion", "cancelled_after_completion"} {
		t.Run(kind, func(t *testing.T) {
			reg := probeFixture(t)
			dir, _ := instanceDir(reg.ID)
			before, err := os.ReadFile(filepath.Join(dir, "registration.json"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			nativeErr := errors.New("fixture later native preparation failure")
			called := 0
			manager := New(runnerCompletionWorkspace{binding: reg.Workspace, prepare: func(gotCtx context.Context, req integration.AgentWorkspaceRequest) (protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan, error) {
				if req.Complete == nil {
					t.Error("native start discarded in-admission completion")
					return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, nativeErr
				}
				if req.Expected == nil || *req.Expected != reg.Workspace || req.ID != reg.ID {
					t.Fatal("completion lost expected binding")
				}
				if kind == "missing_completion" {
					return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
				}
				// Become ready only inside the provider's admission, after start's initial
				// stopped observation. No process is launched by this wire-level fixture.
				nativeStatusPeer(t, reg, `{"protocol":1,"instanceId":"worker"}`, nativeStatusSnapshot(reg))
				binding := reg.Workspace
				if kind == "binding_drift" {
					binding.ID = "replaced"
				}
				if kind == "cancelled_completion" {
					cancel()
				}
				completionErr := req.Complete(gotCtx, binding)
				called++
				if kind == "healthy" || kind == "native_error_after_completion" || kind == "repeated_completion" {
					current, err := load(reg.ID)
					if err != nil || !current.Desired || completionErr != nil {
						t.Fatal("native continuation failed inside admission", completionErr, err)
					}
				}
				if kind == "repeated_completion" {
					info, err := os.Stat(filepath.Join(dir, "registration.json"))
					if err != nil {
						t.Fatal(err)
					}
					if err := req.Complete(gotCtx, binding); err == nil {
						t.Error("repeated completion accepted")
					}
					called++
					after, err := os.Stat(filepath.Join(dir, "registration.json"))
					if err != nil || !os.SameFile(info, after) {
						t.Error("repeated completion performed another persistence", err)
					}
				}
				if kind == "native_error_after_completion" {
					return protocol.AgentWorkspaceBinding{}, integration.WorkspaceReconcilePlan{}, nativeErr
				}
				if kind == "cancelled_after_completion" {
					cancel()
				}
				// Deliberately swallow errors: the manager must independently refuse.
				return reg.Workspace, integration.WorkspaceReconcilePlan{}, nil
			}})
			_, err = manager.Handle(ctx, protocol.AgentRequest{ID: reg.ID, Operation: "start"})
			if kind == "healthy" {
				if err != nil || called != 1 {
					t.Fatal("healthy start failed", called, err)
				}
			} else if err == nil {
				t.Fatal("invalid continuation became successful start")
			}
			if kind == "native_error_after_completion" && !errors.Is(err, nativeErr) {
				t.Fatal("native failure replaced", err)
			}
			after, readErr := os.ReadFile(filepath.Join(dir, "registration.json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if kind == "missing_completion" || kind == "binding_drift" || kind == "cancelled_completion" {
				if !bytes.Equal(before, after) {
					t.Fatal("refused continuation persisted desired state")
				}
			} else {
				current, err := load(reg.ID)
				if err != nil || !current.Desired {
					t.Fatal("accepted continuation rolled back desired state", err)
				}
			}
		})
	}
}
