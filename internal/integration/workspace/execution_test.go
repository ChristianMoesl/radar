package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"reflect"
	"strings"
	"testing"
)

func TestAgentWorkspaceRequiresRegistrationAndConfiguredSandbox(t *testing.T) {
	root := configureWorkspaceRoot(t)
	anchor := filepath.Join(root, "agent")
	if err := os.MkdirAll(anchor, 0755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	if _, err := agentWorkspace(context.Background(), runner, anchor, false); err == nil {
		t.Fatal("unregistered workspace accepted")
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "agent", Path: anchor, Members: []workspacegroup.Member{}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentWorkspace(context.Background(), runner, anchor, true); err == nil {
		t.Fatal("SBX-enabled workspace fell back to host")
	}
	binding, err := agentWorkspace(context.Background(), runner, anchor, false)
	if err != nil || binding.Mode != "local" {
		t.Fatalf("%+v %v", binding, err)
	}
	shared := filepath.Join(t.TempDir(), workspacegroup.SharedDirectoryParent, workspacegroup.ID(anchor))
	if err := os.MkdirAll(shared, 0755); err != nil {
		t.Fatal(err)
	}
	group.Sandbox = &workspacegroup.Sandbox{Name: "fixture-sbx", Agent: "fixture", SharedDirectory: shared, Mounts: []string{anchor, shared}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentWorkspace(context.Background(), runner, anchor, true); err == nil {
		t.Fatal("missing sandbox accepted")
	}
	runner.sbxListOutput = listedReadySandbox(group.Sandbox, "running")
	binding, err = agentWorkspace(context.Background(), runner, anchor, true)
	if err != nil || binding.Sandbox != "fixture-sbx" {
		t.Fatalf("%+v %v", binding, err)
	}
	runner.sbxListOutput = listedReadySandbox(group.Sandbox, "stopped")
	if _, err := agentWorkspace(context.Background(), runner, anchor, true); err == nil {
		t.Fatal("stopped sandbox accepted")
	}
}

type executionRunner struct {
	calls []call
	fail  bool
}

func (*executionRunner) LookPath(string) error { return nil }
func (r *executionRunner) Run(_ context.Context, cwd, name string, args ...string) (string, error) {
	r.calls = append(r.calls, call{cwd: cwd, name: name, args: args})
	if r.fail {
		return "", errors.New("sandbox failed")
	}
	return "fixture", nil
}

func TestWorkspaceExecutionUsesSBXAndNeverFallsBack(t *testing.T) {
	t.Setenv("RADAR_SOCKET", filepath.Join(t.TempDir(), "radar.sock"))
	anchor := t.TempDir()
	group := workspacegroup.Workspace{ID: "fixture", Path: anchor}
	binding := protocol.AgentWorkspaceBinding{ID: "fixture", Path: anchor, Mode: "sbx", Sandbox: "fixture-sbx"}
	runner := &executionRunner{}
	argv := []string{"node", "-e", "process.stdout.write('fixture')"}
	if _, err := executeWorkspace(context.Background(), runner, binding, group, anchor, argv); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"exec", "--workdir", anchor, "fixture-sbx"}, argv...)
	if len(runner.calls) != 1 || runner.calls[0].name != "sbx" || !reflect.DeepEqual(runner.calls[0].args, want) {
		t.Fatalf("wrong execution target: %+v", runner.calls)
	}
	runner.fail = true
	if _, err := executeWorkspace(context.Background(), runner, binding, group, anchor, argv); err == nil {
		t.Fatal("sandbox error hidden")
	}
	for _, c := range runner.calls {
		if c.name != "sbx" {
			t.Fatal("host fallback executed")
		}
	}
	outside := t.TempDir()
	link := filepath.Join(anchor, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := executeWorkspace(context.Background(), runner, binding, group, link, argv); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("escaped cwd: %v", err)
	}
}
