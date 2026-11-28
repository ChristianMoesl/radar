package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func TestAgentRunnerCompletionRetainsOriginalNativeAdmission(t *testing.T) {
	for _, state := range []string{"prepared", "stopped", "missing"} {
		for _, outcome := range []string{"healthy", "native_error", "cancelled", "registration_drift", "panic"} {
			t.Run(state+"/"+outcome, func(t *testing.T) {
				options, request, runner := agentWorkspaceFixture(t)
				binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
				if err != nil {
					t.Fatal(err)
				}
				request.Path, request.Expected = binding.Path, &binding
				if state == "stopped" {
					live := runner.live[binding.Sandbox]
					live.Status = "stopped"
					runner.live[binding.Sandbox] = live
				}
				if state == "missing" {
					delete(runner.live, binding.Sandbox)
				}
				type contextKey struct{}
				baseCtx, cancel := context.WithCancel(context.Background())
				ctx := context.WithValue(baseCtx, contextKey{}, binding.ID)
				defer cancel()
				nativeErr := errors.New("fixture runner continuation error")
				calls := 0
				request.Complete = func(gotCtx context.Context, got protocol.AgentWorkspaceBinding) error {
					calls++
					if got != binding || gotCtx.Value(contextKey{}) != binding.ID {
						t.Error("continuation lost exact binding or context")
					}
					assertAgentPreparationAdmissionHeld(t, options.WorkspaceRoot, binding)
					// Native agent tools use the shared cleanup lease, not writer admission;
					// retaining startup admission must not block ordinary read/execution tools.
					release, err := workspacelease.Acquire(binding.Path, false)
					if err != nil {
						t.Error("startup blocked shared agent work", err)
					} else {
						release()
					}
					switch outcome {
					case "native_error":
						return nativeErr
					case "cancelled":
						cancel()
					case "registration_drift":
						if err := workspacegroup.Update(options.WorkspaceRoot, func(registry *workspacegroup.Registry) error {
							registry.Workspaces[0].SessionName = "concurrent-replacement"
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					case "panic":
						panic(nativeErr)
					}
					return nil
				}
				var got protocol.AgentWorkspaceBinding
				panicked := false
				func() {
					defer func() {
						if v := recover(); v != nil {
							if v != nativeErr {
								panic(v)
							}
							panicked = true
						}
					}()
					got, _, err = prepareAgentWorkspace(ctx, runner, options, request, true)
				}()
				if calls != 1 {
					t.Fatalf("runner completion calls %d want 1", calls)
				}
				switch outcome {
				case "healthy":
					if err != nil || got != binding {
						t.Fatal("healthy continuation failed", got, err)
					}
				case "native_error":
					if !errors.Is(err, nativeErr) {
						t.Fatal("native continuation cause lost", err)
					}
				case "cancelled":
					if !errors.Is(err, context.Canceled) {
						t.Fatal("cancelled continuation became success", err)
					}
				case "registration_drift":
					if err == nil || !strings.Contains(err.Error(), "workspace registration changed") {
						t.Fatal("runner completion trusted replaced full registration", err)
					}
				case "panic":
					if !panicked {
						t.Fatal("runner continuation panic was swallowed")
					}
				}
				if outcome != "healthy" && got != (protocol.AgentWorkspaceBinding{}) {
					t.Fatal("failed continuation returned usable binding")
				}
				// Error/cancellation/panic does not erase accepted provisioning or intent.
				if _, err := os.Stat(filepath.Join(request.DataDir, "workspace.json")); err != nil {
					t.Fatal(err)
				}
				for _, acquire := range []func() (func(), error){func() (func(), error) { return workspacelease.Acquire(binding.Path, true) }, func() (func(), error) { return workspacelease.AcquireMutation(binding.Path) }, func() (func(), error) { return workspacelease.AcquireSandbox(binding.Sandbox, true) }, func() (func(), error) { return workspacelease.AcquireSandboxMutation(binding.Sandbox) }} {
					release, err := acquire()
					if err != nil {
						t.Error("runner completion leaked native admission", err)
					} else {
						release()
					}
				}
				if err := workspacegroup.WithNoteLock(options.WorkspaceRoot, func() error { return nil }); err != nil {
					t.Fatal("continuation leaked note lock", err)
				}
			})
		}
	}
}

func TestAgentRunnerCompletionNeverRunsOnPreviewOrRefusal(t *testing.T) {
	for _, kind := range []string{"preview", "stale", "busy", "cancelled", "missing_expected"} {
		t.Run(kind, func(t *testing.T) {
			options, request, runner := agentWorkspaceFixture(t)
			binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
			if err != nil {
				t.Fatal(err)
			}
			request.Path, request.Expected = binding.Path, &binding
			called := false
			request.Complete = func(context.Context, protocol.AgentWorkspaceBinding) error { called = true; return nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "preview":
				request.Preview = true
			case "stale":
				request.PlanID = "not-the-native-plan"
			case "busy":
				release, err := workspacelease.Acquire(binding.Path, true)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "cancelled":
				cancel()
			case "missing_expected":
				request.Expected = nil
			}
			_, _, err = prepareAgentWorkspace(ctx, runner, options, request, true)
			if kind == "preview" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe continuation request accepted")
			}
			if called {
				t.Fatal("preview/refusal invoked runner continuation")
			}
		})
	}
}

func TestAgentRunnerCompletionExplicitBindingDoesNotClaimOwnership(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
	if err != nil {
		t.Fatal(err)
	}
	explicit := integration.AgentWorkspaceRequest{ID: "explicit", DataDir: filepath.Join(t.TempDir(), "retained"), Path: binding.Path, Expected: &binding}
	called := false
	explicit.Complete = func(_ context.Context, got protocol.AgentWorkspaceBinding) error {
		called = true
		if got != binding {
			t.Fatal("explicit binding changed")
		}
		return nil
	}
	got, _, err := prepareAgentWorkspace(context.Background(), runner, options, explicit, true)
	if err != nil || !called || got != binding {
		t.Fatal("explicit validation-only continuation failed", called, err)
	}
	if _, err := os.Stat(filepath.Join(explicit.DataDir, "workspace.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("explicit continuation claimed ownership", err)
	}
	data, err := json.Marshal(explicit)
	if err != nil || strings.Contains(string(data), "Complete") {
		t.Fatal("in-process continuation became serializable authority", err)
	}
}
