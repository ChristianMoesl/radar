package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func agentWorkspaceFixture(t *testing.T) (CreateOptions, integration.AgentWorkspaceRequest, *recreationRunner) {
	t.Helper()
	// This fake-runner lifecycle fixture must not depend on the host's KVM
	// device. Native Linux preflight remains covered by the SBX client tests.
	withWorkspaceGOOS(t, "darwin")
	root := configureWorkspaceRoot(t)
	options := CreateOptions{WorkspaceRoot: root, PrepareOnly: true, NoteAuthor: testNoteAuthor(t), Sandbox: true, SandboxKitName: "shell"}
	request := integration.AgentWorkspaceRequest{ID: "fixture", DataDir: filepath.Join(t.TempDir(), "agents", "fixture")}
	runner := &recreationRunner{live: map[string]listedSandbox{}, ports: map[string][]workspacegroup.SandboxPort{}}
	return options, request, runner
}

func TestAgentOwnsDedicatedWorkspaceAndRecoversItsSandbox(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	ctx := context.Background()
	// An unrelated interactive workspace must never be borrowed.
	other := workspacegroup.Workspace{ID: workspacegroup.ID(filepath.Join(options.WorkspaceRoot, "interactive")), Name: "interactive", Path: filepath.Join(options.WorkspaceRoot, "interactive"), Members: []workspacegroup.Member{}}
	if err := os.MkdirAll(other.Path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := registerWorkspace(options.WorkspaceRoot, other); err != nil {
		t.Fatal(err)
	}
	binding, _, err := prepareAgentWorkspace(ctx, runner, options, request, true)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Mode != "sbx" || binding.Sandbox == "" || binding.Path == other.Path {
		t.Fatalf("not dedicated: %+v", binding)
	}
	before, err := os.ReadFile(filepath.Join(request.DataDir, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(request.DataDir, "workspace.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("ownership intent is not private")
	}
	request.Expected, request.Path = &binding, binding.Path
	// Daemon restart/uncertain add retains ownership and canonical note identity.
	again, _, err := prepareAgentWorkspace(ctx, runner, options, request, true)
	if err != nil || again != binding {
		t.Fatalf("binding changed: %+v %v", again, err)
	}
	// A stopped sandbox is started in place, not removed/recreated.
	live := runner.live[binding.Sandbox]
	live.Status = "stopped"
	runner.live[binding.Sandbox] = live
	creates := countAgentCalls(runner, "create")
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, false); err != nil {
		t.Fatal(err)
	}
	if countAgentCalls(runner, "create") != creates || runner.live[binding.Sandbox].Status != "running" {
		t.Fatal("stopped sandbox was replaced or not resumed")
	}
	// Missing runtime is recreated with the same managed identity and mounts.
	delete(runner.live, binding.Sandbox)
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, true); err != nil {
		t.Fatal(err)
	}
	if countAgentCalls(runner, "create") != creates+1 {
		t.Fatal("missing sandbox not recovered")
	}
	after, _ := os.ReadFile(filepath.Join(request.DataDir, "workspace.json"))
	if string(after) != string(before) {
		t.Fatal("recovery rewrote ownership intent")
	}
	registry, err := workspacegroup.Load(options.WorkspaceRoot)
	if err != nil || len(registry.Workspaces) != 2 {
		t.Fatalf("duplicate workspace: %+v %v", registry, err)
	}
	for _, call := range runner.calls {
		if call.name == "tmux" || call.name == "pi" {
			t.Fatalf("started interactive session: %+v", call)
		}
	}
}

func countAgentCalls(runner *recreationRunner, operation string) int {
	count := 0
	for _, call := range runner.calls {
		if call.name == "sbx" && len(call.args) > 0 && call.args[0] == operation {
			count++
		}
	}
	return count
}

func TestAgentWorkspacePreviewConfirmationAndDrift(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	ctx := context.Background()
	request.Preview = true
	binding, plan, err := prepareAgentWorkspace(ctx, runner, options, request, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(request.DataDir); !os.IsNotExist(err) {
		t.Fatal("preview persisted intent")
	}
	if _, err := os.Stat(binding.Path); !os.IsNotExist(err) {
		t.Fatal("preview provisioned workspace")
	}
	if _, err := os.Stat(plan.Note.Path); !os.IsNotExist(err) {
		t.Fatal("preview created canonical note")
	}
	request.Preview = false
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, false); err == nil || !strings.Contains(err.Error(), "confirmation required") {
		t.Fatalf("unconfirmed provisioning: %v", err)
	}
	request.Note, _ = json.Marshal(plan.Note)
	request.PlanID = "stale"
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, false); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("stale confirmation: %v", err)
	}
	request.PlanID = plan.PlanID
	created, _, err := prepareAgentWorkspace(ctx, runner, options, request, false)
	if err != nil || created != binding {
		t.Fatalf("accepted plan failed: %+v %v", created, err)
	}
	request.Expected, request.Path = &created, created.Path
	delete(runner.live, created.Sandbox)
	request.PlanID, request.Note = "", nil
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, false); err == nil || !strings.Contains(err.Error(), "confirmation required") {
		t.Fatalf("unconfirmed recreation: %v", err)
	}
	request.Preview = true
	_, recovery, err := prepareAgentWorkspace(ctx, runner, options, request, false)
	if err != nil {
		t.Fatal(err)
	}
	request.Preview, request.PlanID = false, recovery.PlanID
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, false); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedAgentAddReusesItsOriginalPreparedResources(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	ctx := context.Background()
	request.Preview = true
	_, plan, err := prepareAgentWorkspace(ctx, runner, options, request, true)
	if err != nil {
		t.Fatal(err)
	}
	request.Preview = false
	request.Note, _ = json.Marshal(plan.Note)
	runner.failCreate = SandboxName("workspace", "agent-"+request.ID)
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, true); err == nil {
		t.Fatal("injected failure ignored")
	}
	before, err := os.ReadFile(filepath.Join(request.DataDir, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	runner.failCreate = ""
	if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(request.DataDir, "workspace.json"))
	if string(before) != string(after) {
		t.Fatal("retry changed canonical note or workspace")
	}
	registry, _ := workspacegroup.Load(options.WorkspaceRoot)
	if len(registry.Workspaces) != 1 || registry.Workspaces[0].NotePath != plan.Note.Path {
		t.Fatal("partial provisioning duplicated its resources")
	}
}

func TestAgentWorkspaceRefusesRebindingCorruptionAndFallback(t *testing.T) {
	for _, scenario := range []string{"rebind", "workspace-identity", "sandbox-identity", "ownership", "corrupt-intent", "inside-data", "missing-registration"} {
		t.Run(scenario, func(t *testing.T) {
			options, request, runner := agentWorkspaceFixture(t)
			ctx := context.Background()
			binding, _, err := prepareAgentWorkspace(ctx, runner, options, request, true)
			if err != nil {
				t.Fatal(err)
			}
			request.Path, request.Expected = binding.Path, &binding
			registry, _ := workspacegroup.Load(options.WorkspaceRoot)
			switch scenario {
			case "rebind":
				request.Path = t.TempDir()
			case "workspace-identity":
				binding.ID = "unrelated"
			case "sandbox-identity":
				registry.Workspaces[0].Sandbox.Name = "unrelated"
			case "ownership":
				registry.Workspaces[0].TaskLinkingKey = "task:unrelated"
			case "corrupt-intent":
				if err := os.WriteFile(filepath.Join(request.DataDir, "workspace.json"), []byte(`{"version":999}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "inside-data":
				request = integration.AgentWorkspaceRequest{ID: "inside", DataDir: filepath.Join(options.WorkspaceRoot, "agent-inside", "state")}
			case "missing-registration":
				registry.Workspaces = nil
			}
			if err := workspacegroup.Save(options.WorkspaceRoot, registry); err != nil {
				t.Fatal(err)
			}
			calls := countAgentCalls(runner, "create") + countAgentCalls(runner, "rm")
			if _, _, err := prepareAgentWorkspace(ctx, runner, options, request, true); err == nil {
				t.Fatal("unsafe provisioning/recovery accepted")
			}
			if countAgentCalls(runner, "create")+countAgentCalls(runner, "rm") != calls {
				t.Fatal("runtime mutated before ownership validation")
			}
		})
	}
}

func TestExplicitAgentWorkspaceIsNotSilentlyManaged(t *testing.T) {
	options, request, runner := agentWorkspaceFixture(t)
	binding, _, err := prepareAgentWorkspace(context.Background(), runner, options, request, true)
	if err != nil {
		t.Fatal(err)
	}
	explicit := integration.AgentWorkspaceRequest{ID: "explicit", DataDir: filepath.Join(t.TempDir(), "explicit"), Path: binding.Path, Expected: &binding}
	runner.live[binding.Sandbox] = listedSandbox{Name: binding.Sandbox, Status: "stopped", Mounts: runner.live[binding.Sandbox].Workspaces}
	if _, _, err := prepareAgentWorkspace(context.Background(), runner, options, explicit, true); err == nil {
		t.Fatal("explicit stopped workspace was repaired without ownership")
	}
	if _, err := os.Stat(explicit.DataDir); !os.IsNotExist(err) {
		t.Fatal("explicit workspace was claimed")
	}
	// A global sandbox requirement still rejects a host-only explicit workspace.
	host := filepath.Join(options.WorkspaceRoot, "host-only")
	if err := os.MkdirAll(host, 0755); err != nil {
		t.Fatal(err)
	}
	if err := registerWorkspace(options.WorkspaceRoot, workspacegroup.Workspace{ID: workspacegroup.ID(host), Name: "host", Path: host}); err != nil {
		t.Fatal(err)
	}
	explicit.Path, explicit.Expected = host, &protocol.AgentWorkspaceBinding{ID: workspacegroup.ID(host), Path: host, Mode: "local"}
	if _, _, err := prepareAgentWorkspace(context.Background(), runner, options, explicit, true); err == nil {
		t.Fatal("SBX policy fell back to host")
	}
}
