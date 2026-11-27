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

func retainedPreanchorFixture(t *testing.T, resource, layout string) (CreateOptions, integration.AgentWorkspaceRequest, *recreationRunner, protocol.AgentWorkspaceBinding, integration.WorkspaceReconcilePlan) {
	t.Helper()
	options, request, runner := agentWorkspaceFixture(t)
	collision := filepath.Join(options.WorkspaceRoot, "agent-"+request.ID)
	if layout == "collision" {
		if err := os.MkdirAll(collision, 0755); err != nil {
			t.Fatal(err)
		}
	}
	request.Preview = true
	binding, preview, err := prepareAgentWorkspace(context.Background(), runner, options, request, false)
	if err != nil {
		t.Fatal(err)
	}
	request.Preview, request.PlanID = false, preview.PlanID
	request.Note, err = json.Marshal(preview.Note)
	if err != nil {
		t.Fatal(err)
	}
	var release func()
	if resource == "anchor" {
		release, err = workspacelease.Acquire(binding.Path, true)
	} else {
		release, err = workspacelease.AcquireSandbox(binding.Sandbox, true)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, false); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatal("fixture did not fail native bundle admission", err)
	}
	if _, err := os.Stat(filepath.Join(request.DataDir, "workspace.json")); err != nil {
		t.Fatal("fixture did not retain ownership intent", err)
	}
	for _, path := range []string{binding.Path, preview.Note.Path} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("blocked fixture already created resources", path, err)
		}
	}
	registry, err := workspacegroup.Load(options.WorkspaceRoot)
	if err != nil || len(registry.Workspaces) != 0 {
		t.Fatal("blocked fixture already registered resources", registry, err)
	}
	if layout == "collision" {
		// The original suffixed anchor remains authoritative even when the
		// default name becomes free. Recovery must not choose a fresh path.
		if binding.Path == collision {
			t.Fatal("fixture did not select a collision-qualified anchor")
		}
		if err := os.Remove(collision); err != nil {
			t.Fatal(err)
		}
	}
	return options, request, runner, binding, preview
}

func TestAgentPreanchorIntentRecoversExactOriginalCreation(t *testing.T) {
	for _, resource := range []string{"anchor", "sandbox"} {
		for _, layout := range []string{"default", "collision"} {
			for _, mode := range []string{"accepted", "automatic"} {
				t.Run(resource+"/"+layout+"/"+mode, func(t *testing.T) {
					options, request, runner, binding, original := retainedPreanchorFixture(t, resource, layout)
					intentPath := filepath.Join(request.DataDir, "workspace.json")
					before, err := os.ReadFile(intentPath)
					if err != nil {
						t.Fatal(err)
					}
					request.Preview, request.PlanID = true, ""
					actual, preview, err := prepareAgentWorkspace(context.Background(), runner, options, request, false)
					if err != nil || actual != binding || preview.Note.Path != original.Note.Path || preview.Note.LinkingKey != original.Note.LinkingKey {
						t.Fatal("retained pre-anchor intent cannot preview its exact original creation", actual, err)
					}
					request.Preview = false
					for _, rejection := range []string{"denied", "stale", "cancelled", "busy"} {
						request.PlanID = preview.PlanID
						ctx, cancel := context.WithCancel(context.Background())
						var release func()
						want := ""
						switch rejection {
						case "denied":
							request.PlanID, want = "", "confirmation required"
						case "stale":
							request.PlanID, want = "stale", "plan changed"
						case "cancelled":
							cancel()
						case "busy":
							want = "in use"
							if resource == "anchor" {
								release, err = workspacelease.Acquire(binding.Path, true)
							} else {
								release, err = workspacelease.AcquireSandbox(binding.Sandbox, true)
							}
							if err != nil {
								t.Fatal(err)
							}
						}
						actual, _, err := prepareAgentWorkspace(ctx, runner, options, request, false)
						cancel()
						if release != nil {
							release()
						}
						if err == nil || actual != (protocol.AgentWorkspaceBinding{}) || (want != "" && !strings.Contains(err.Error(), want)) || (rejection == "cancelled" && !errors.Is(err, context.Canceled)) {
							t.Fatal("retained creation bypassed fresh native safeguards", rejection, actual, err)
						}
					}
					for _, path := range []string{binding.Path, original.Note.Path} {
						if _, err := os.Lstat(path); !os.IsNotExist(err) {
							t.Fatal("rejected recovery created resources", path, err)
						}
					}
					if countAgentCalls(runner, "create") != 0 || countAgentCalls(runner, "exec") != 0 {
						t.Fatal("rejected recovery entered runtime effects")
					}
					request.PlanID = preview.PlanID
					if mode == "automatic" {
						request.PlanID = ""
					}
					resumed := false
					admitted := activationRunner{Runner: runner, run: func(ctx context.Context, cwd, name string, args ...string) (string, error) {
						if name == "sbx" && len(args) == 5 && args[0] == "exec" && args[4] == "true" {
							resumed = true
							assertAgentPreparationAdmissionHeld(t, options.WorkspaceRoot, binding)
						}
						return runner.Run(ctx, cwd, name, args...)
					}}
					actual, _, err = prepareAgentWorkspace(context.Background(), admitted, options, request, mode == "automatic")
					if err != nil || actual != binding || !resumed || countAgentCalls(runner, "create") != 1 {
						t.Fatal("fresh admitted recovery did not create the original bundle once", actual, err)
					}
					after, err := os.ReadFile(intentPath)
					if err != nil || string(after) != string(before) {
						t.Fatal("recovery rewrote its retained intent", err)
					}
					if note, err := AgentWorkspaceNote(request.ID, request.DataDir, actual); err != nil || note == nil || note.Path != original.Note.Path || note.LinkingKey != original.Note.LinkingKey {
						t.Fatal("recovery changed original ownership", note, err)
					}
				})
			}
		}
	}
}

func TestAgentPreanchorRecoveryRefusesReplacementAndOccupiedAnchors(t *testing.T) {
	for _, scenario := range []string{"known-registration", "directory", "file", "symlink", "binding-drift", "host-fallback", "prepared-note-drift"} {
		t.Run(scenario, func(t *testing.T) {
			options, request, runner, binding, preview := retainedPreanchorFixture(t, "anchor", "default")
			intentPath := filepath.Join(request.DataDir, "workspace.json")
			switch scenario {
			case "known-registration":
				request.Path, request.Expected = binding.Path, &binding
			case "directory":
				if err := os.Mkdir(binding.Path, 0755); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(binding.Path, []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), binding.Path); err != nil {
					t.Fatal(err)
				}
			case "binding-drift":
				data, err := os.ReadFile(intentPath)
				if err != nil {
					t.Fatal(err)
				}
				var intent agentWorkspaceIntent
				if err := json.Unmarshal(data, &intent); err != nil {
					t.Fatal(err)
				}
				intent.Binding.Sandbox = "unrelated"
				data, err = json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(intentPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "host-fallback":
				options.Sandbox = false
			case "prepared-note-drift":
				note := *preview.Note
				note.LinkingKey = "obsidian:task:00000000-0000-4000-8000-000000000001"
				request.Note, _ = json.Marshal(note)
			}
			before, err := os.ReadFile(intentPath)
			if err != nil {
				t.Fatal(err)
			}
			request.PlanID = ""
			if actual, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true); err == nil || actual != (protocol.AgentWorkspaceBinding{}) {
				t.Fatal("retained intent authorized replacement or unrelated content", actual, err)
			}
			after, err := os.ReadFile(intentPath)
			if err != nil || string(after) != string(before) {
				t.Fatal("refusal rewrote retained intent", err)
			}
			if countAgentCalls(runner, "create") != 0 || countAgentCalls(runner, "exec") != 0 || countAgentCalls(runner, "rm") != 0 {
				t.Fatal("refusal entered runtime effects")
			}
			registry, err := workspacegroup.Load(options.WorkspaceRoot)
			if err != nil || len(registry.Workspaces) != 0 {
				t.Fatal("refusal registered replacement resources", registry, err)
			}
			if _, err := os.Lstat(preview.Note.Path); !os.IsNotExist(err) {
				t.Fatal("refusal created the note", err)
			}
		})
	}
}

func TestPrepareOnlyUnregisteredPathStillRequiresRegistration(t *testing.T) {
	options, _, runner := agentWorkspaceFixture(t)
	options.Path, options.Name = filepath.Join(options.WorkspaceRoot, "explicit"), "explicit"
	if _, err := Create(context.Background(), runner, options); err == nil || !strings.Contains(err.Error(), "must already be registered") {
		t.Fatal("generic explicit preparation gained creation authority", err)
	}
	if _, err := os.Lstat(options.Path); !os.IsNotExist(err) {
		t.Fatal("generic preparation created an unregistered anchor", err)
	}
}
