package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"radar/internal/cleanup"
	"radar/internal/integration"
	"radar/internal/integration/workspace"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestDiagnosticsRPCIsBoundedReadOnlyAndDoesNotFilterHiddenState(t *testing.T) {
	t.Setenv("RADAR_AGENT_ROOT", filepath.Join(t.TempDir(), "agents"))
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Attention: "done", Title: "private-title", SourceRefs: []protocol.SourceRef{{ID: "test:ABC-123", Source: "test", EntityID: "test:ABC-123", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", Metadata: map[string]string{"completed_at": "2026-01-02T10:20:30Z"}}}}})
	operationID := store.BeginOperation("cleanup", store.Records()[0].NumericID, "")
	store.FinishOperation(operationID, protocol.ManagedOperation{Status: "partial", CompletedTargets: 1, TotalTargets: 2})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must be hidden")
	}
	mutations := 0
	server := New(store, logger, func() { mutations++ }, func() error { mutations++; return nil }, nil, integration.NewRegistry(), cleanup.New(nil)).SetResourcePublication(func() { mutations++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for _, req := range []protocol.Request{{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "tasks", Limit: 1}}, {Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "source_refs"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "bindings"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "agents"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_schedules"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "gc_plan"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "integrations"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "integrations", State: "done"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "operations"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "operations", State: "running"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "operations"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_schedules", State: "done"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "agents", State: "done"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "workspace_resources"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "workspace_resources", State: "done"}},
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "tasks", ParentID: "must-not-be-ignored"}},
		{Method: "diagnostics"}, {Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "execute"}}} {
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if req.Method == "diagnostics" && req.Diagnostics != nil && req.Diagnostics.ParentID == "" && req.Diagnostics.State == "" && (req.Diagnostics.Section == "tasks" || req.Diagnostics.Section == "source_refs" || req.Diagnostics.Section == "bindings" || req.Diagnostics.Section == "agents" || req.Diagnostics.Section == "agent_schedules" || req.Diagnostics.Section == "integrations" || req.Diagnostics.Section == "operations") {
			expected := 1
			if req.Diagnostics.Section == "bindings" || req.Diagnostics.Section == "agents" || req.Diagnostics.Section == "agent_schedules" {
				expected = 0
			}
			if !response.OK || response.Diagnostics == nil || response.Diagnostics.Total != expected || response.Diagnostics.Schema != protocol.DiagnosticsVersion || !response.Diagnostics.ResourceRefreshPending {
				t.Fatalf("response=%+v", response)
			}
			if req.Diagnostics.Section == "integrations" && (response.Diagnostics.Items[0].Integration.Name != "test" || response.Diagnostics.Items[0].Integration.CachedStatus != "unknown" || response.Diagnostics.Items[0].Integration.CapabilityEvidence != "not_registered") {
				t.Fatal("retained source without registration/status must remain visible with unknown evidence")
			}
			if req.Diagnostics.Section == "operations" && (response.Diagnostics.Items[0].Operation.ID != operationID || response.Diagnostics.Items[0].Operation.Status != "partial" || *response.Diagnostics.Items[0].Operation.CompletedTargets != 1) {
				t.Fatal("diagnostic RPC lost native partial operation evidence")
			}
		} else if response.OK {
			t.Fatal("invalid request accepted")
		}
	}
	_ = client.Close()
	<-done
	if mutations != 0 {
		t.Fatal("inspection caused mutation or refresh")
	}
}

func TestGCPlanRPCIncludesHiddenTasksWithoutCallingPreviewOrMutation(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
	if err := os.MkdirAll(filepath.Join(home, "config", "radar"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "radar", "config.yaml"), []byte("workspace: {root_dir: "+root+"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTasks([]protocol.Task{{Attention: "done", Title: "private-title", SourceRefs: []protocol.SourceRef{{ID: "workitem", EntityID: "workitem", Source: "test", Role: protocol.SourceRefRoleAuthoritative, Lifecycle: protocol.SourceRefLifecycleWorkItem, Authority: protocol.SourceRefAuthorityPrimary, Signal: "done", Metadata: map[string]string{"completed_at": "2026-01-02T10:20:30Z"}}, {ID: "workspace:group", Source: "workspace", Path: filepath.Join(root, "group"), WorkspaceID: "group", ProvidesWorkspace: true}}}})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must be UI hidden")
	}
	effects := 0
	service := cleanup.New([]integration.CleanupProvider{diagnosticNoCleanup{fakeCleanupSource: fakeCleanupSource{name: "fake"}, calls: &effects}})
	server := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(workspace.Source{}), service).SetResourcePublication(func() { effects++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for _, req := range []protocol.DiagnosticsRequest{{Section: "gc_plan"}, {Section: "gc_plan", ParentID: strconv.Itoa(store.Records()[0].NumericID)}, {Section: "gc_plan", State: "done", Workspace: root}, {Section: "gc_plan", ID: "absent"}} {
		if err := encoder.Encode(protocol.Request{Method: "diagnostics", Diagnostics: &req}); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if !response.OK || response.Diagnostics == nil || response.Diagnostics.Schema != protocol.DiagnosticsVersion || !response.Diagnostics.ResourceRefreshPending {
			t.Fatalf("response=%+v", response)
		}
		if req.ID == "" && (response.Diagnostics.Total != 1 || response.Diagnostics.Items[0].GCPlan.Decision != "candidate") {
			t.Fatalf("hidden record omitted: %+v", response.Diagnostics)
		}
	}
	_ = client.Close()
	<-done
	if effects != 0 {
		t.Fatal("cached diagnostic called provider preview, cleanup or refresh")
	}
}

type diagnosticNoCleanup struct {
	fakeCleanupSource
	calls *int
}

func (p diagnosticNoCleanup) PreviewCleanup(context.Context, integration.CleanupPreviewRequest) ([]protocol.CleanupTarget, error) {
	*p.calls++
	return nil, nil
}
func (p diagnosticNoCleanup) Cleanup(context.Context, integration.CleanupRequest) (protocol.CleanupTarget, error) {
	*p.calls++
	return protocol.CleanupTarget{}, nil
}

func TestRuntimeProbeRPCRequiresExplicitMethodExactScopeAndNeverMutates(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "workspaces")
	path := filepath.Join(root, "registered")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("RADAR_STATE", filepath.Join(home, "state.db"))
	if err := os.MkdirAll(filepath.Join(home, "config", "radar"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "radar", "config.yaml"), []byte("workspace: {root_dir: "+root+"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	group := workspacegroup.Workspace{ID: workspacegroup.ID(path), Name: "registered", Path: path, Sandbox: &workspacegroup.Sandbox{Name: "runtime-name", Agent: "shell"}}
	if err := workspacegroup.Save(root, workspacegroup.Registry{Version: workspacegroup.Version, Workspaces: []workspacegroup.Workspace{group}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	effects, probes := 0, 0
	source := diagnosticRuntimeSource{diagnosticNoCleanup: diagnosticNoCleanup{fakeCleanupSource: fakeCleanupSource{name: "sbx"}, calls: &effects}, probes: &probes}
	server := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(workspace.Source{}, source), cleanup.New([]integration.CleanupProvider{source})).SetResourcePublication(func() { effects++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for index, req := range []protocol.Request{
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: group.ID}},
		{Method: "diagnostics-probe"},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "runtime_probe"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "tasks", ParentID: group.ID}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: group.ID, Workspace: path + "-other"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "runtime_probe", ParentID: group.ID, Workspace: path}},
	} {
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if index < 5 {
			if response.OK || probes != 0 {
				t.Fatalf("implicit/unscoped probe accepted: %+v", response)
			}
		} else if !response.OK || response.Diagnostics == nil || response.Diagnostics.Total != 1 || !response.Diagnostics.ResourceRefreshPending || response.Diagnostics.Schema != protocol.DiagnosticsVersion || response.Diagnostics.Items[0].Runtime.Observation.Presence != "present" || probes != 1 {
			t.Fatalf("explicit probe missing: %+v", response)
		}
	}
	_ = client.Close()
	<-done
	if effects != 0 {
		t.Fatal("runtime observation called cleanup, collection or refresh")
	}
}

type diagnosticRuntimeSource struct {
	diagnosticNoCleanup
	probes *int
}

func (p diagnosticRuntimeSource) DiagnosticRuntime(context.Context, string) protocol.DiagnosticRuntimeObservation {
	*p.probes++
	return protocol.DiagnosticRuntimeObservation{Presence: "present", State: "running", ID: "native-lifetime"}
}

func TestAgentProbeRPCRequiresExplicitSingleRegisteredTarget(t *testing.T) {
	t.Setenv("RADAR_STATE", filepath.Join(t.TempDir(), "state.db"))
	root := t.TempDir()
	t.Setenv("RADAR_AGENT_ROOT", root)
	t.Setenv("RADAR_SOCKET", filepath.Join(root, "absent.sock"))
	dir := filepath.Join(root, "worker")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	reg := map[string]any{"version": 1, "id": "worker", "desired": false, "workspace": map[string]any{"id": "workspace", "path": filepath.Join(root, "workspace"), "mode": "local"}}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(filepath.Join(dir, "registration.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := state.NewStore(logger)
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	server := New(store, logger, func() { effects++ }, func() error { effects++; return nil }, nil, integration.NewRegistry(), cleanup.New(nil)).SetResourcePublication(func() { effects++ }, func() protocol.Response { return protocol.Response{ResourceRefreshPending: true} })
	host, client := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); server.handle(host) }()
	encoder, decoder := json.NewEncoder(client), json.NewDecoder(client)
	for index, req := range []protocol.Request{
		{Method: "diagnostics", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_probe", ParentID: "worker"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_probe"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_probe", ParentID: "worker", ID: "other"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_probe", ParentID: "worker", Cursor: "cursor"}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_probe", ParentID: "worker", Workspace: filepath.Join(root, "workspace-other")}},
		{Method: "diagnostics-probe", Diagnostics: &protocol.DiagnosticsRequest{Section: "agent_probe", ParentID: "worker"}},
	} {
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
		var response protocol.Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if index < 5 {
			if response.OK {
				t.Fatalf("implicit/unscoped request accepted: %+v", response)
			}
		} else if !response.OK || response.Diagnostics == nil || response.Diagnostics.Total != 1 || !response.Diagnostics.ResourceRefreshPending || response.Diagnostics.Items[0].AgentProbe.RunnerEvidence != "unknown_not_reached" || response.Diagnostics.Items[0].AgentProbe.Snapshot != nil {
			t.Fatalf("unknown evidence lost: %+v", response)
		}
	}
	client.Close()
	<-done
	if effects != 0 {
		t.Fatal("probe mutated/refreshed resources")
	}
}
