package sbx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"radar/internal/integration/workspace"
	"radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

type retirementRunner struct {
	listing    string
	inspectErr error
	commands   []string
}

func (*retirementRunner) LookPath(string) error { return nil }
func (r *retirementRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.commands = append(r.commands, name+" "+strings.Join(args, " "))
	if len(args) > 0 && args[0] == "ls" {
		return r.listing, r.inspectErr
	}
	return "", nil
}

func TestSandboxRetirementFailsClosedAndNeverRemovesSandbox(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "missing", "replaced identity", "missing identity", "foreign mount", "unknown status", "duplicate name", "malformed listing", "inspection failure"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, "workspaces")
			anchor := filepath.Join(root, "completed")
			config := filepath.Join(home, "config", "radar", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(config), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(anchor, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, fmt.Appendf(nil, "workspace: {root_dir: %q}\n", root), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			group := workspacegroup.Workspace{ID: workspacegroup.ID(anchor), Name: "completed", Path: anchor, Sandbox: &workspacegroup.Sandbox{Name: "fixture", Agent: "shell", Mounts: []string{anchor}}}
			if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
				t.Fatal(err)
			}
			task := protocol.Task{SourceRefs: []protocol.SourceRef{{ID: "workspace:" + group.ID, Source: "workspace", Kind: "workspace", ProvidesWorkspace: true, WorkspaceID: group.ID, Path: anchor}, {ID: "sbx:sandbox:fixture", Source: "sbx", Kind: "sandbox", Path: anchor, Metadata: map[string]string{"name": "fixture", "id": "container-1"}}}}
			targets, err := workspace.RuntimeCleanupTargets(task, "sbx")
			if err != nil || len(targets) != 1 {
				t.Fatalf("targets=%+v err=%v", targets, err)
			}
			id, status, mount := "container-1", "running", anchor
			switch scenario {
			case "replaced identity":
				id = "container-2"
			case "missing identity":
				id = ""
			case "foreign mount":
				mount = t.TempDir()
			case "unknown status":
				status = "provisioning"
			case "stopped":
				status = "stopped"
			}
			row := fmt.Sprintf(`{"id":%q,"name":"fixture","status":%q,"workspaces":[%q]}`, id, status, mount)
			runner := &retirementRunner{listing: "[" + row + "]"}
			switch scenario {
			case "missing":
				runner.listing = "[]"
			case "duplicate name":
				runner.listing = "[" + row + "," + row + "]"
			case "malformed listing":
				runner.listing = "invalid"
			case "inspection failure":
				runner.inspectErr = errors.New("unavailable")
			}
			_, err = retireSandbox(context.Background(), runner, targets[0])
			wantError := scenario != "running" && scenario != "stopped" && scenario != "missing"
			if (err != nil) != wantError {
				t.Fatalf("err=%v wantError=%v", err, wantError)
			}
			log := strings.Join(runner.commands, "\n")
			if strings.Contains(log, "sbx rm") || strings.Contains(log, "sbx stop") != (scenario == "running") {
				t.Fatalf("unsafe commands:\n%s", log)
			}
		})
	}
}
