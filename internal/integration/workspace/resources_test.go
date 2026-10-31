package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacegroup "radar/internal/integration/workspace/group"
)

func TestLifecycleResourcesAreFreshWithoutFullContextProbes(t *testing.T) {
	root, group, runner := inspectionFixture(t)
	instructions := filepath.Join(group.Members[0].Path, "AGENTS.md")
	skills := filepath.Join(group.Members[0].Path, ".pi", "skills")
	if err := os.WriteFile(instructions, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skills, 0700); err != nil {
		t.Fatal(err)
	}
	first, err := InspectWorkspaceResources(context.Background(), runner, group.Members[0].Path, root)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Registered || len(first.Members) != 4 || len(first.Members[0].InstructionFiles) != 1 || len(first.Members[0].SkillPaths) != 1 {
		t.Fatalf("resources: %+v", first)
	}
	if len(runner.calls) != 1 || runner.calls["sbx ls"] != 1 {
		t.Fatalf("lifecycle did full inspection: %v", runner.calls)
	}
	data, _ := json.Marshal(first)
	for _, field := range []string{"revision", "desired", "dirty", "repositories", "ports", "openable_resources"} {
		if strings.Contains(string(data), `"`+field+`":`) {
			t.Fatalf("lifecycle exposed reconciliation field: %s", field)
		}
	}
	if err := os.Remove(instructions); err != nil {
		t.Fatal(err)
	}
	group.Members = group.Members[1:]
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	next, err := InspectWorkspaceResources(context.Background(), runner, group.Path, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Members) != 3 || next.Members[0].Path != group.Members[0].Path || runner.calls["sbx ls"] != 2 {
		t.Fatal("lifecycle reused stale registration/resources")
	}
}
func TestLifecycleResourcesRetainUnavailableSandboxRouting(t *testing.T) {
	for _, kind := range []string{"absent", "unowned", "list-error"} {
		t.Run(kind, func(t *testing.T) {
			root, group, runner := inspectionFixture(t)
			contextContractRunner(runner, kind)
			result, err := InspectWorkspaceResources(context.Background(), runner, group.Path, root)
			if err != nil || result.Sandbox == nil || result.Sandbox.Name != group.Sandbox.Name || result.Sandbox.RuntimeError == "" || result.Sandbox.SharedDirectoryReady {
				t.Fatalf("unverified runtime lost its routing boundary: %+v %v", result, err)
			}
			if runner.calls["sbx ports"] != 0 {
				t.Fatal("lifecycle read unrelated ports")
			}
		})
	}
}
func TestLifecycleResourcesOutsideWorkspaceDoNotDiscoverOrEnroll(t *testing.T) {
	root, _, runner := inspectionFixture(t)
	result, err := InspectWorkspaceResources(context.Background(), runner, t.TempDir(), root)
	if err != nil || result.Registered || len(result.Members) != 0 || len(runner.calls) != 0 {
		t.Fatalf("outside-workspace projection: %+v %v %v", result, err, runner.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectWorkspaceResources(ctx, runner, t.TempDir(), root); err == nil {
		t.Fatal("lifecycle ignored cancellation")
	}
}
