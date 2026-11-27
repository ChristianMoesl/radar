package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type agentNotePlanningRunner struct {
	Runner
	beforePlan func()
}

func (r *agentNotePlanningRunner) LookPath(name string) error {
	if name == "tmux" && r.beforePlan != nil {
		change := r.beforePlan
		r.beforePlan = nil
		change()
	}
	return r.Runner.LookPath(name)
}

type agentUnattachedNoteAuthor struct {
	integration.WorkspaceNoteAuthor
	err error
}

func (a agentUnattachedNoteAuthor) EnsureWorkspaceNote(context.Context, DesiredWorkspaceNote) error {
	return a.err
}

func TestAgentPreparationRetainsOriginalUnattachedNoteRecovery(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	author := options.NoteAuthor
	nativeErr := errors.New("fixture canonical note creation interrupted")
	options.NoteAuthor = agentUnattachedNoteAuthor{WorkspaceNoteAuthor: author, err: nativeErr}
	if _, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true); !errors.Is(err, nativeErr) {
		t.Fatal("partial note failure lost its native cause", err)
	}
	intentPath := filepath.Join(request.DataDir, "workspace.json")
	before, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workspacegroup.Load(options.WorkspaceRoot)
	if err != nil || len(registry.Workspaces) != 1 || registry.Workspaces[0].NotePath != "" {
		t.Fatal("fixture did not retain the initial unattached registration", registry, err)
	}
	options.NoteAuthor = author
	binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
	if err != nil {
		t.Fatal("original unattached note could not recover", err)
	}
	after, err := os.ReadFile(intentPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("recovery rewrote its original intent", err)
	}
	if _, err := AgentWorkspaceNote(request.ID, request.DataDir, binding); err != nil {
		t.Fatal("recovered note does not match original ownership", err)
	}
}

func TestAgentPreparationRejectsRetainedNoteIdentityDrift(t *testing.T) {
	for _, timing := range []string{"before", "native-plan"} {
		for _, drift := range []string{"changed-key", "removed-key", "changed-path"} {
			if timing == "before" && drift == "changed-path" {
				continue // The existing pre-plan ownership check already rejects this.
			}
			for _, mode := range []string{"preview", "stopped", "missing", "retained-add"} {
				t.Run(timing+"/"+drift+"/"+mode, func(t *testing.T) {
					options, request, base := agentWorkspaceFixture(t)
					binding, _, err := prepareAgentWorkspace(context.Background(), base, options, request, true)
					if err != nil {
						t.Fatal(err)
					}
					request.Path, request.Expected, request.Preview = binding.Path, &binding, mode == "preview"
					if mode == "retained-add" {
						request.Path, request.Expected = "", nil
					}
					intentPath := filepath.Join(request.DataDir, "workspace.json")
					intentBefore, err := os.ReadFile(intentPath)
					if err != nil {
						t.Fatal(err)
					}
					_, original, found, err := RegisteredWorkspace(binding.Path, options.WorkspaceRoot)
					if err != nil || !found {
						t.Fatal(found, err)
					}
					noteBefore, err := os.ReadFile(original.NotePath)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "missing" {
						delete(base.live, binding.Sandbox)
					} else {
						live := base.live[binding.Sandbox]
						live.Status = "stopped"
						base.live[binding.Sandbox] = live
					}
					var changedRegistry []byte
					change := func() {
						t.Helper()
						if timing == "native-plan" && !request.Preview {
							(&agentPlanningNoteAuthor{t: t, root: options.WorkspaceRoot, checkLock: true}).assertNoteLock()
						}
						if err := workspacegroup.Update(options.WorkspaceRoot, func(registry *workspacegroup.Registry) error {
							registry.Workspaces[0].NoteLinkingKey = "obsidian:task:00000000-0000-4000-8000-000000000001"
							switch drift {
							case "removed-key":
								registry.Workspaces[0].NoteLinkingKey = ""
							case "changed-path":
								registry.Workspaces[0].NotePath = filepath.Join(filepath.Dir(original.NotePath), "other.md")
								registry.Workspaces[0].NoteLinkingKey = original.NoteLinkingKey
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
						changedRegistry, err = os.ReadFile(workspacegroup.Path(options.WorkspaceRoot))
						if err != nil {
							t.Fatal(err)
						}
					}
					runner := &agentNotePlanningRunner{Runner: base}
					if timing == "before" {
						change()
					} else {
						runner.beforePlan = change
					}
					calls := len(base.calls)
					actual, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
					if err == nil || !strings.Contains(err.Error(), "canonical note identity changed") || actual != (protocol.AgentWorkspaceBinding{}) {
						t.Fatal("retained ownership trusted a changed note identity", actual, err)
					}
					if len(changedRegistry) == 0 {
						t.Fatal("fixture never changed the registered identity")
					}
					for _, call := range base.calls[calls:] {
						if call.name == "sbx" && len(call.args) > 0 && (call.args[0] == "create" || call.args[0] == "rm" || call.args[0] == "exec") {
							t.Fatal("changed note identity entered runtime effects", call)
						}
					}
					for path, want := range map[string][]byte{intentPath: intentBefore, original.NotePath: noteBefore, workspacegroup.Path(options.WorkspaceRoot): changedRegistry} {
						got, err := os.ReadFile(path)
						if err != nil || string(got) != string(want) {
							t.Fatal("rejected recovery rewrote retained evidence", path, err)
						}
					}
					// Restore only this test's injected drift. A fresh healthy call
					// must retain normal recovery and reacquire native admission.
					if err := workspacegroup.Update(options.WorkspaceRoot, func(registry *workspacegroup.Registry) error {
						registry.Workspaces[0] = original
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					request.Preview = false
					if actual, _, err := prepareAgentWorkspace(context.Background(), base, options, request, true); err != nil || actual != binding {
						t.Fatal("note rejection leaked admission or broke healthy recovery", actual, err)
					}
				})
			}
		}
	}
}
