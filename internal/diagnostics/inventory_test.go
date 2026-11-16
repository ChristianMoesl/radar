package diagnostics

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/agentservice"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

func fixture(t *testing.T) (*state.Store, string, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	path := filepath.Join(root, "completed")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(home, "agents"))
	t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
	if err := os.MkdirAll(filepath.Join(home, "config", "radar"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "radar", "config.yaml"), []byte("workspace: {root_dir: "+root+"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return store, root, path
}

func task(path, id string) protocol.Task {
	return protocol.Task{Attention: "done", Title: "private-title", Reason: "private-provider-body", Metadata: map[string]string{"token": "private-token"}, SourceRefs: []protocol.SourceRef{
		{ID: id, EntityID: id, Source: "test", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", CanonicalKey: id, Metadata: map[string]string{"completed_at": "2026-01-02T10:20:30Z", "token": "private-token"}},
		{ID: "workspace:" + workspacegroup.ID(path), Source: "workspace", Kind: "workspace", Path: path, ProvidesWorkspace: true, WorkspaceID: workspacegroup.ID(path), Metadata: map[string]string{"environment": "private-env"}},
	}}
}

func TestInventoryIncludesHiddenRetainedRecordsWithoutPrivatePayloads(t *testing.T) {
	store, root, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must be hidden from dashboard")
	}
	page, err := read(store, protocol.DiagnosticsRequest{Section: "tasks"}, false)
	if err != nil || page.Total != 1 || !page.Complete || len(page.Items) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if view := page.Items[0].Task; view.State != "done" || view.DoneAt != "2026-01-02T10:20:30Z" || len(view.Sources) != 2 {
		t.Fatalf("view=%+v", view)
	}
	encoded, _ := json.Marshal(page)
	for _, private := range []string{"private-title", "private-provider-body", "private-token", "private-env", "completed_at"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private payload exposed: %s", encoded)
		}
	}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{{ID: workspacegroup.ID(path), Name: "completed", Path: path, NotePath: filepath.Join(root, "canonical.md"), Sandbox: &workspacegroup.Sandbox{Name: "fixture", Agent: "shell", EnvFile: filepath.Join(root, "private-env-file"), ReadyCommand: []string{"private-command"}}}}}); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(os.Getenv("RADAR_AGENT_ROOT"), "stopped-owner")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	registration, _ := json.Marshal(agentservice.Registration{Version: 1, ID: "stopped-owner", Desired: false, Workspace: protocol.AgentWorkspaceBinding{ID: workspacegroup.ID(path), Path: path, Mode: "local"}})
	if err := os.WriteFile(filepath.Join(agentDir, "registration.json"), registration, 0600); err != nil {
		t.Fatal(err)
	}
	page, err = read(store, protocol.DiagnosticsRequest{Section: "workspaces"}, true)
	if err != nil || page.Total != 1 || !page.ResourceRefreshPending {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	view := page.Items[0].Workspace
	done, _ := time.Parse(time.RFC3339, "2026-01-02T10:20:30Z")
	if len(view.Retention) != 1 || view.Retention[0].ExpiryAt != done.Add(workspacegc.ExpiryRetention).Format(time.RFC3339) || len(view.AgentOwners) != 1 || view.AgentOwners[0] != "stopped-owner" || view.RuntimeEvidence != "unknown_not_probed" || view.ExecutionEligibility != "blocked_registered_agent" {
		t.Fatalf("view=%+v", view)
	}
	encoded, _ = json.Marshal(page)
	if strings.Contains(string(encoded), "private-") {
		t.Fatalf("registration payload exposed: %s", encoded)
	}
}

func TestPaginationScopeFiltersAndSnapshotChanges(t *testing.T) {
	store, _, path := fixture(t)
	tasks := []protocol.Task{task(path, "test:ABC-1"), task(path+"-other", "test:ABC-2"), task(path+"-third", "test:ABC-3")}
	store.SetTasks(tasks)
	req := protocol.DiagnosticsRequest{Section: "tasks", Limit: 1}
	seen := map[string]bool{}
	for {
		page, err := read(store, req, false)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 3 || len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatalf("bad page: %+v", page)
		}
		seen[page.Items[0].ID] = true
		if page.Complete {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatal("pagination incomplete")
	}
	page, err := read(store, protocol.DiagnosticsRequest{Section: "tasks", Workspace: path}, false)
	if err != nil || page.Total != 1 {
		t.Fatalf("scope escaped prefix boundary: %+v %v", page, err)
	}
	first, err := read(store, protocol.DiagnosticsRequest{Section: "tasks", Limit: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	req = protocol.DiagnosticsRequest{Section: "tasks", Limit: 1, Cursor: first.NextCursor, State: "done"}
	if _, err := read(store, req, false); err == nil {
		t.Fatal("cursor accepted changed selector")
	}
	req.State = ""
	store.SetSources([]protocol.SourceStatus{{Name: "test", Status: "ok"}})
	if _, err := read(store, req, false); err == nil {
		t.Fatal("cursor accepted changed revision")
	}
	for _, bad := range []protocol.DiagnosticsRequest{{Section: "shell"}, {Section: "tasks", Limit: 51}, {Section: "tasks", Limit: -1}, {Section: "tasks", Workspace: "relative"}, {Section: "tasks", Cursor: "not-a-cursor"}, {Section: "tasks", ID: strings.Repeat("x", 513)}} {
		if _, err := read(store, bad, false); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestUnavailableOwnershipIsNotNoOwnersAndRegistryChangesInvalidateCursor(t *testing.T) {
	store, root, path := fixture(t)
	registry := workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{{ID: workspacegroup.ID(path), Name: "one", Path: path}, {ID: workspacegroup.ID(path + "-two"), Name: "two", Path: path + "-two"}}}
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("RADAR_AGENT_ROOT"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := read(store, protocol.DiagnosticsRequest{Section: "workspaces", Limit: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Workspace.AgentOwnershipEvidence != "unavailable" {
		t.Fatal("unavailable owner inventory reported as complete")
	}
	registry.Workspaces[0].NotePath = filepath.Join(root, "changed.md")
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	if _, err := read(store, protocol.DiagnosticsRequest{Section: "workspaces", Limit: 1, Cursor: page.NextCursor}, false); err == nil {
		t.Fatal("cursor accepted changed registration")
	}
}

func TestInternalInspectionSnapshotDoesNotAliasState(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-123")})
	records, refs, _ := store.InspectionSnapshot()
	records[0].Snapshot.Title = "changed"
	records[0].SourceRefIDs[0] = "changed"
	refs[0].Snapshot.Metadata["token"] = "changed"
	records2, refs2, _ := store.InspectionSnapshot()
	if records2[0].Snapshot.Title == "changed" || records2[0].SourceRefIDs[0] == "changed" || refs2[0].Snapshot.Metadata["token"] == "changed" {
		t.Fatal("inspection snapshot aliases mutable state")
	}
}

func TestAllRetainedWorkspacesAreAccountedForAcrossPages(t *testing.T) {
	store, root, _ := fixture(t)
	registry := workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{}}
	tasks := []protocol.Task{}
	for i := 0; i < 24; i++ {
		path := filepath.Join(root, strconv.Itoa(i))
		registry.Workspaces = append(registry.Workspaces, workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: strconv.Itoa(i), Path: path})
		tasks = append(tasks, task(path, "test:ABC-"+strconv.Itoa(i)))
	}
	store.SetTasks(tasks)
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture should retain hidden completed tasks")
	}
	if err := workspacegroup.Save(root, registry); err != nil {
		t.Fatal(err)
	}
	req := protocol.DiagnosticsRequest{Section: "workspaces", Limit: 20}
	seen := map[string]bool{}
	for {
		page, err := read(store, req, false)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 24 {
			t.Fatalf("retained inventory lost records: %+v", page)
		}
		for _, item := range page.Items {
			if seen[item.ID] || len(item.Workspace.TaskIDs) != 1 || len(item.Workspace.Retention) != 1 {
				t.Fatalf("bad ownership accounting: %+v", item)
			}
			seen[item.ID] = true
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 24 {
		t.Fatal("retained inventory incomplete")
	}
}

func TestPageByteLimitAndNestedTruncationAreExplicit(t *testing.T) {
	store, _, path := fixture(t)
	input := task(path, "test:ABC-123")
	for i := 0; i < 30; i++ {
		input.SourceRefs = append(input.SourceRefs, protocol.SourceRef{ID: "test:ref-" + strconv.Itoa(i), Source: "test", Title: "private-title", Metadata: map[string]string{"token": "private-token"}})
	}
	store.SetTasks([]protocol.Task{input})
	page, err := read(store, protocol.DiagnosticsRequest{Section: "tasks"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Items[0].Task.Truncated || len(page.Items[0].Task.Sources) != maxChildren || page.Items[0].Task.SourceCount != 32 {
		t.Fatalf("truncation not accounted for: %+v", page)
	}
	items := []protocol.DiagnosticItem{}
	for i := 0; i < 50; i++ {
		refs := []protocol.DiagnosticRef{}
		for j := 0; j < 16; j++ {
			refs = append(refs, protocol.DiagnosticRef{ID: strings.Repeat("x", 512), Path: strings.Repeat("x", 512), WorkspaceID: strings.Repeat("x", 512)})
		}
		items = append(items, protocol.DiagnosticItem{ID: strconv.Itoa(i), Task: &protocol.DiagnosticTask{Sources: refs}})
	}
	page, err = paginate(protocol.DiagnosticsRequest{Section: "tasks", Limit: 50}, items, 0, false, "cached_daemon_state")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if len(encoded) > maxPageBytes || page.Complete || page.NextCursor == "" || len(page.Items) == 0 {
		t.Fatalf("output bound not respected: bytes=%d items=%d", len(encoded), len(page.Items))
	}
}

func read(store *state.Store, req protocol.DiagnosticsRequest, pending bool) (protocol.DiagnosticsPage, error) {
	return Read(store, req, pending, integration.NewRegistry(workspace.Source{}))
}

func TestWorkspaceInventoryWithoutProviderFailsExplicitly(t *testing.T) {
	store, _, _ := fixture(t)
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "workspaces"}, false, integration.NewRegistry()); err == nil {
		t.Fatal("missing capability looked like no registrations")
	}
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "tasks"}, false, integration.NewRegistry()); err != nil {
		t.Fatal(err)
	}
}
