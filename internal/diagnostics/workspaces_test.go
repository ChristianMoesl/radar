package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"radar/internal/agentservice"
	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
)

func TestWorkspaceResourcesCompletelyPageBoundedRegistrationAndOwnership(t *testing.T) {
	store, root, path := fixture(t)
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "retained", Path: path, Sandbox: &workspacegroup.Sandbox{Name: "registered-not-probed", Agent: "shell", EnvFile: filepath.Join(root, "private-env-file"), ReadyCommand: []string{"private-command"}}}
	input := []protocol.Task{}
	for i := 0; i < 18; i++ {
		name := fmt.Sprintf("member-%02d", i)
		group.Members = append(group.Members, workspacegroup.Member{Path: filepath.Join(path, name), Repository: filepath.Join(root, "source-"+name), Branch: "private-branch"})
		group.Sandbox.AdditionalMounts = append(group.Sandbox.AdditionalMounts, workspacegroup.SandboxMount{Path: filepath.Join(root, "mount-"+name), ReadOnly: i%2 == 0})
		group.Sandbox.Ports = append(group.Sandbox.Ports, workspacegroup.SandboxPort{HostPort: 8000 + i, SandboxPort: 9000 + i})
		retained := task(path, fmt.Sprintf("test:ABC-%d", i))
		// Distinct source identities share a lexical workspace relationship,
		// not one shared workspace ref that production correctly merges.
		retained.SourceRefs = retained.SourceRefs[:1]
		retained.SourceRefs[0].Path = filepath.Join(path, name)
		input = append(input, retained)
		id := fmt.Sprintf("stopped-%02d", i)
		dir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		// The exact production removal-owner relation includes backing workspaces
		// inside the group and intentionally stopped registrations.
		reg, _ := json.Marshal(agentservice.Registration{Version: 1, ID: id, Desired: false, Workspace: protocol.AgentWorkspaceBinding{ID: group.ID, Path: filepath.Join(path, id), Mode: "local"}})
		if err := os.WriteFile(filepath.Join(dir, "registration.json"), reg, 0600); err != nil {
			t.Fatal(err)
		}
	}
	other := workspacegroup.Workspace{ID: workspacegroup.ID(path + "-other"), Name: "other", Path: path + "-other", Members: []workspacegroup.Member{{Path: path + "-other/repo", Repository: filepath.Join(root, "other-source"), Branch: "main"}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group, other}}); err != nil {
		t.Fatal(err)
	}
	store.SetTasks(input)
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must include dashboard-hidden retained tasks")
	}
	beforeRecords, beforeRefs, beforeRevision := store.InspectionSnapshot()
	beforeRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	page, err := read(store, protocol.DiagnosticsRequest{Section: "workspaces", ID: group.ID}, false)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("summary: %+v %v", page, err)
	}
	view := page.Items[0].Workspace
	if view.MemberCount != 18 || view.MountCount != 18 || view.PortCount != 18 || view.TaskCount != 18 || view.AgentOwnerCount == nil || *view.AgentOwnerCount != 18 || !view.Truncated {
		t.Fatalf("full counts missing: %+v", view)
	}
	if len(view.Members) != 16 || len(view.Mounts) != 16 || len(view.Ports) != 16 || len(view.TaskIDs) != 16 || len(view.Retention) != 16 || len(view.AgentOwners) != 16 || view.ExecutionEligibility != "blocked_registered_agent" {
		t.Fatalf("summary not bounded or owner protection lost: %+v", view)
	}
	req := protocol.DiagnosticsRequest{Section: "workspace_resources", ParentID: group.ID, Workspace: path, Limit: 7}
	seen := map[string]bool{}
	counts := map[string]int{}
	var firstCursor string
	for {
		page, err = read(store, req, true)
		if err != nil || page.Total != 90 || !page.ResourceRefreshPending || len(page.Items) > 7 {
			t.Fatalf("page: %+v %v", page, err)
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "private-") || len(encoded) > maxPageBytes {
			t.Fatalf("unbounded/private payload: %s", encoded)
		}
		for _, item := range page.Items {
			resource := item.WorkspaceResource
			if seen[item.ID] || resource == nil || resource.ID != item.ID || resource.WorkspaceID != group.ID || resource.Occurrences != 1 {
				t.Fatalf("bad stable relationship: %+v", item)
			}
			seen[item.ID] = true
			counts[resource.Kind]++
			if resource.Task != nil && (resource.Task.State != "done" || resource.Task.DoneAt == "" || resource.Task.GCRetention.Decision != "retention_gate_passed" || resource.RecordID == "" || resource.Evidence != "cached_task_ownership_including_history") {
				t.Fatalf("lost retained task explanation: %+v", resource)
			}
			selected, err := read(store, protocol.DiagnosticsRequest{Section: "workspace_resources", ParentID: group.ID, ID: item.ID}, true)
			if err != nil || selected.Total != 1 || selected.Items[0].ID != item.ID {
				t.Fatalf("stable ID not selectable: %+v %v", selected, err)
			}
		}
		if page.Complete {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		req.Cursor = page.NextCursor
	}
	for _, kind := range []string{"member", "mount", "port", "task_owner", "agent_owner"} {
		if counts[kind] != 18 {
			t.Fatalf("nested evidence lost beyond summary: %+v", counts)
		}
	}
	all, err := read(store, protocol.DiagnosticsRequest{Section: "workspace_resources"}, false)
	if err != nil || all.Total != 91 {
		t.Fatalf("global resources: %+v %v", all, err)
	}
	for _, selector := range []protocol.DiagnosticsRequest{
		{Section: "workspace_resources", Workspace: other.Path, ParentID: group.ID},
		{Section: "workspace_resources", ParentID: "unknown"},
	} {
		page, err := read(store, selector, false)
		if err != nil || page.Total != 0 {
			t.Fatalf("scope/parent leaked: %+v %v", page, err)
		}
	}
	if _, err := read(store, protocol.DiagnosticsRequest{Section: "workspace_resources", State: "done"}, false); err == nil {
		t.Fatal("partially applicable state filter accepted")
	}
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "workspace_resources"}, false, integration.NewRegistry()); err == nil {
		t.Fatal("missing provider presented as no resources")
	}
	afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
	afterRegistry, _ := os.ReadFile(workspacegroup.Path(root))
	if beforeRevision != afterRevision || !reflect.DeepEqual(beforeRecords, afterRecords) || !reflect.DeepEqual(beforeRefs, afterRefs) || string(beforeRegistry) != string(afterRegistry) {
		t.Fatal("inspection mutated state or registration")
	}
	// Changing a hidden child beyond the summary boundary invalidates the full
	// resource snapshot; changed selectors/pending publication do too.
	for _, changed := range []protocol.DiagnosticsRequest{
		{Section: "workspace_resources", ParentID: other.ID, Workspace: path, Limit: 7, Cursor: firstCursor},
		{Section: "workspace_resources", ParentID: group.ID, Workspace: path, Limit: 7, Cursor: firstCursor},
	} {
		if _, err := read(store, changed, false); err == nil {
			t.Fatal("changed parent or pending state reused cursor")
		}
	}
	group.Sandbox.AdditionalMounts[17].ReadOnly = !group.Sandbox.AdditionalMounts[17].ReadOnly
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group, other}}); err != nil {
		t.Fatal(err)
	}
	req.Cursor = firstCursor
	if _, err := read(store, req, true); err == nil {
		t.Fatal("unshown changed resource reused cursor")
	}
}

func TestWorkspaceResourceIdentityUsesFullValuesAndPreservesOccurrences(t *testing.T) {
	long := "/" + strings.Repeat("x", 600)
	group := protocol.DiagnosticWorkspace{ID: "workspace", Members: []protocol.DiagnosticMember{{Path: long + "a", Repository: "/source"}, {Path: long + "b", Repository: "/source"}, {Path: long + "a", Repository: "/source"}}}
	tasks := []workspaceTask{{recordID: "record", view: protocol.DiagnosticRetention{TaskID: 1, State: "done"}}}
	before, _ := json.Marshal(group)
	items := workspaceResources("", group, tasks, nil)
	if len(items) != 3 || items[0].ID == items[1].ID || items[0].WorkspaceResource.Occurrences != 2 || len(items[0].WorkspaceResource.Member.Path) > 516 {
		t.Fatalf("full identity/bounded payload/duplicates lost: %+v", items)
	}
	tasks[0].view.State = "active"
	changed := workspaceResources("", group, tasks, nil)
	if changed[2].ID != items[2].ID || changed[2].WorkspaceResource.Task.State != "active" {
		t.Fatal("volatile task state changed relationship identity")
	}
	after, _ := json.Marshal(group)
	if string(before) != string(after) {
		t.Fatal("projection mutated provider values")
	}
}

func TestWorkspaceResourceOwnershipFailureIsUnknownNotNoOwners(t *testing.T) {
	store, root, path := fixture(t)
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{{ID: workspacegroup.ID(path), Name: "one", Path: path}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("RADAR_AGENT_ROOT"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"workspaces", "workspace_resources"} {
		page, err := read(store, protocol.DiagnosticsRequest{Section: section}, false)
		if err != nil || !strings.HasSuffix(page.Evidence, "agent_ownership_unavailable") {
			t.Fatalf("unknown ownership not surfaced: %+v %v", page, err)
		}
		if section == "workspaces" && (page.Items[0].Workspace.AgentOwnerCount != nil || page.Items[0].Workspace.AgentOwnershipEvidence != "unavailable") {
			t.Fatal("unavailable owner count reported as zero")
		}
	}
	// Summary task counts honor the existing retained-task state filter.
	view := workspaceSummary(protocol.DiagnosticWorkspace{}, nil, nil, true)
	if view.AgentOwnerCount == nil || *view.AgentOwnerCount != 0 || view.TaskCount != 0 {
		t.Fatal("verified empty ownership must remain distinguishable")
	}
}

func TestWorkspaceResourcePrimaryMountModesAndEvidenceFreshness(t *testing.T) {
	store, root, path := fixture(t)
	ro := filepath.Join(root, "read-only")
	rw := filepath.Join(root, "writable")
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "one", Path: path, Sandbox: &workspacegroup.Sandbox{Name: "registered", Agent: "shell", Mounts: []string{ro + ":ro", rw}}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	req := protocol.DiagnosticsRequest{Section: "workspace_resources", ParentID: group.ID, Limit: 1}
	page, err := read(store, req, false)
	if err != nil || page.Total != 2 || page.NextCursor == "" {
		t.Fatalf("mount registrations: %+v %v", page, err)
	}
	all, err := read(store, protocol.DiagnosticsRequest{Section: "workspace_resources", ParentID: group.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range all.Items {
		mount := item.WorkspaceResource.Mount
		if mount == nil || (mount.Path != ro && mount.Path != rw) || mount.ReadOnly != (mount.Path == ro) {
			t.Fatalf("incorrect registered mount mode: %+v", item)
		}
	}
	// No agent-owner rows existed before or after this failure. The changed
	// evidence itself must invalidate pagination even with identical items.
	if err := os.WriteFile(os.Getenv("RADAR_AGENT_ROOT"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Cursor = page.NextCursor
	if _, err := read(store, req, false); err == nil {
		t.Fatal("unknown ownership reused previously verified evidence cursor")
	}
}
