package workspace

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/workspacelease"
)

func assertAgentPreparationAdmissionHeld(t *testing.T, root string, binding protocol.AgentWorkspaceBinding) {
	t.Helper()
	(&agentPlanningNoteAuthor{t: t, root: root, checkLock: true}).assertNoteLock()
	for label, acquire := range map[string]func() (func(), error){
		"anchor cleanup":   func() (func(), error) { return workspacelease.Acquire(binding.Path, true) },
		"anchor mutation":  func() (func(), error) { return workspacelease.AcquireMutation(binding.Path) },
		"sandbox cleanup":  func() (func(), error) { return workspacelease.AcquireSandbox(binding.Sandbox, true) },
		"sandbox mutation": func() (func(), error) { return workspacelease.AcquireSandboxMutation(binding.Sandbox) },
	} {
		if release, err := acquire(); err == nil {
			release()
			t.Error("agent preparation completion dropped native admission:", label)
		}
	}
	if release, err := workspacelease.Acquire(t.TempDir(), true); err != nil {
		t.Error("completion blocked unrelated cleanup", err)
	} else {
		release()
	}
}

func TestAgentPreparationCompletionRetainsNativeAdmission(t *testing.T) {
	for _, phase := range []string{"new", "stopped"} {
		for _, outcome := range []string{"healthy", "resume-error", "cancelled", "verification-error", "resume-registration-drift", "verification-registration-drift"} {
			t.Run(phase+"/"+outcome, func(t *testing.T) {
				options, request, base := agentWorkspaceFixture(t)
				request.Preview = true
				binding, _, err := prepareAgentWorkspace(context.Background(), base, options, request, true)
				if err != nil {
					t.Fatal(err)
				}
				request.Preview = false
				if phase == "stopped" {
					binding, _, err = prepareAgentWorkspace(context.Background(), base, options, request, true)
					if err != nil {
						t.Fatal(err)
					}
					request.Path, request.Expected = binding.Path, &binding
					actual := base.live[binding.Sandbox]
					actual.Status = "stopped"
					base.live[binding.Sandbox] = actual
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				nativeErr := errors.New("fixture native completion failure")
				resumed, verified := false, false
				drift := func() {
					t.Helper()
					if err := workspacegroup.Update(options.WorkspaceRoot, func(registry *workspacegroup.Registry) error {
						registry.Workspaces[0].SessionName = "concurrent-replacement"
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				runner := activationRunner{Runner: base, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
					if name == "sbx" && len(args) == 5 && args[0] == "exec" && args[4] == "true" {
						resumed = true
						assertAgentPreparationAdmissionHeld(t, options.WorkspaceRoot, binding)
						switch outcome {
						case "resume-error":
							return "", nativeErr
						case "cancelled":
							cancel()
							return "", ctx.Err()
						case "resume-registration-drift":
							drift()
						}
					}
					if resumed && name == "sbx" && len(args) > 0 && args[0] == "ls" {
						verified = true
						assertAgentPreparationAdmissionHeld(t, options.WorkspaceRoot, binding)
						if outcome == "verification-error" {
							return "", nativeErr
						}
						if outcome == "verification-registration-drift" {
							drift()
						}
					}
					return base.Run(ctx, cwd, name, args...)
				}}
				actual, _, err := prepareAgentWorkspace(ctx, runner, options, request, true)
				if !resumed {
					t.Fatal("fixture never reached native resume", err)
				}
				switch outcome {
				case "healthy":
					if err != nil || actual != binding || !verified {
						t.Fatal("healthy native preparation changed", actual, verified, err)
					}
				case "cancelled":
					if !errors.Is(err, context.Canceled) || verified {
						t.Fatal("cancelled resume continued verification or lost its cause", verified, err)
					}
				case "resume-error", "verification-error":
					if !errors.Is(err, nativeErr) || verified != (outcome == "verification-error") {
						t.Fatal("native completion error lost or later work continued", verified, err)
					}
				default:
					if err == nil || !strings.Contains(err.Error(), "workspace registration changed") {
						t.Fatal("completion trusted changed registration", actual, err)
					}
				}
				if outcome != "healthy" && actual != (protocol.AgentWorkspaceBinding{}) {
					t.Fatal("failed completion returned a usable binding", actual)
				}
				_, registered, found, lookupErr := RegisteredWorkspace(binding.Path, options.WorkspaceRoot)
				if lookupErr != nil || !found {
					t.Fatal("completion rolled back accepted workspace resources", lookupErr)
				}
				if strings.HasSuffix(outcome, "registration-drift") && registered.SessionName != "concurrent-replacement" {
					t.Fatal("completion overwrote concurrent registration")
				}
				if _, err := os.Stat(registered.NotePath); err != nil {
					t.Fatal("completion discarded the accepted canonical note", err)
				}
				for label, acquire := range map[string]func() (func(), error){
					"anchor cleanup":   func() (func(), error) { return workspacelease.Acquire(binding.Path, true) },
					"anchor mutation":  func() (func(), error) { return workspacelease.AcquireMutation(binding.Path) },
					"sandbox cleanup":  func() (func(), error) { return workspacelease.AcquireSandbox(binding.Sandbox, true) },
					"sandbox mutation": func() (func(), error) { return workspacelease.AcquireSandboxMutation(binding.Sandbox) },
				} {
					if release, err := acquire(); err != nil {
						t.Error("completion leaked native admission:", label, err)
					} else {
						release()
					}
				}
				// Completion must release the note transaction too, including on
				// errors after the resource executor has already succeeded.
				if err := workspacegroup.WithNoteLock(options.WorkspaceRoot, func() error { return nil }); err != nil {
					t.Fatal("completion leaked its note lock", err)
				}
			})
		}
	}
}
