package diagnostics

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"radar/internal/integration"
	workspacegroup "radar/internal/integration/workspace/group"
	"radar/internal/protocol"
	"radar/internal/state"
)

func TestOperationInventoryIsScopedPrivateAndPreservesPartialEvidence(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-1"), task(path+"-other", "test:ABC-2")})
	var selected, other int
	// Resolve through the actual retained-reference join, not record ordering.
	records, refs, _ := store.InspectionSnapshot()
	for _, record := range records {
		for _, ref := range refs {
			if ref.TaskRecordID == record.ID && ref.Snapshot.Path == path {
				selected = record.NumericID
			}
		}
	}
	for _, record := range records {
		if record.NumericID != selected {
			other = record.NumericID
		}
	}
	if selected == 0 || other == 0 || selected == other {
		t.Fatal("invalid scope fixture")
	}
	running := store.BeginOperation("task-title", selected, "")
	partial := store.BeginOperation("cleanup", selected, "")
	store.FinishOperation(partial, protocol.ManagedOperation{Status: "partial", CompletedTargets: 1, TotalTargets: 3})
	reported := store.ReportResourceMutation(protocol.ResourceMutation{WorkspaceID: workspacegroup.ID(path)})
	store.ReportResourceMutation(protocol.ResourceMutation{TaskID: selected, WorkspaceID: workspacegroup.ID(path + "-other")})
	store.BeginOperation("task-note", other, "")
	store.BeginOperation("private-provider-body", 0, "")
	before := store.DashboardSnapshot()
	disk, err := os.ReadFile(os.Getenv("RADAR_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	providers := integration.NewRegistry(diagnosticIntegrationTrap{"trap"})
	page, err := Read(store, protocol.DiagnosticsRequest{Section: "operations", Workspace: path}, true, providers)
	if err != nil || page.Total != 3 || !page.Complete || !page.ResourceRefreshPending || page.Revision != before.Revision {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		seen[item.ID] = true
		op := item.Operation
		switch item.ID {
		case running:
			if op.Status != "running" || op.ExecutionEvidence != "daemon_handler_running" || op.CompletedTargets != nil || op.TotalTargets != nil {
				t.Fatalf("running=%+v", op)
			}
		case partial:
			if op.Status != "partial" || op.ExecutionEvidence != "daemon_handler_result_not_resource_verification" || op.TargetEvidence != "native_cleanup_successful_prefix_only" || op.CompletedTargets == nil || *op.CompletedTargets != 1 || op.TotalTargets == nil || *op.TotalTargets != 3 {
				t.Fatalf("partial=%+v", op)
			}
		case reported:
			if op.Status != "reported" || op.ExecutionEvidence != "unverified_client_report_not_daemon_execution" || op.CompletedTargets != nil {
				t.Fatalf("report=%+v", op)
			}
		default:
			t.Fatal("unrelated operation leaked into workspace scope")
		}
	}
	if len(seen) != 3 {
		t.Fatal("missing scoped evidence")
	}
	all, err := Read(store, protocol.DiagnosticsRequest{Section: "operations"}, false, providers)
	if err != nil || all.Total != 6 {
		t.Fatalf("all=%+v %v", all, err)
	}
	raw, _ := json.Marshal(all)
	for _, secret := range []string{"private-", path, "SourceRefs", "completed_at", "operation_id", "duration"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("private/unavailable detail leaked: %s", raw)
		}
	}
	afterDisk, _ := os.ReadFile(os.Getenv("RADAR_STATE"))
	if !reflect.DeepEqual(before, store.DashboardSnapshot()) || !reflect.DeepEqual(disk, afterDisk) {
		t.Fatal("operation inspection mutated state")
	}
}

func TestOperationPaginationTracksCompletionScopeAndDaemonLifetime(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-1")})
	id := store.BeginOperation("cleanup", store.Records()[0].NumericID, "")
	for i := 0; i < 80; i++ {
		store.ReportResourceMutation(protocol.ResourceMutation{WorkspaceID: workspacegroup.ID(path)})
	}
	req := protocol.DiagnosticsRequest{Section: "operations", Workspace: path, Limit: 7}
	seen := map[string]bool{}
	for {
		page, err := Read(store, req, false, integration.NewRegistry())
		if err != nil || page.Total != 65 {
			t.Fatalf("page=%+v %v", page, err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("duplicate operation page")
			}
			seen[item.ID] = true
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 65 || !seen[id] {
		t.Fatal("active operation lost or terminal history incomplete")
	}
	req.Cursor = ""
	first, err := Read(store, req, false, integration.NewRegistry())
	if err != nil || first.NextCursor == "" {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	changed := req
	changed.Workspace = path + "-other"
	if _, err := Read(store, changed, false, integration.NewRegistry()); err == nil {
		t.Fatal("scope cursor reused")
	}
	if _, err := Read(store, req, true, integration.NewRegistry()); err == nil {
		t.Fatal("pending-state drift accepted")
	}
	store.FinishOperation(id, protocol.ManagedOperation{Status: "failed", TotalTargets: 2})
	if _, err := Read(store, req, false, integration.NewRegistry()); err == nil {
		t.Fatal("stale completion page accepted")
	}
	one, err := Read(store, protocol.DiagnosticsRequest{Section: "operations", ID: id}, false, integration.NewRegistry())
	if err != nil || one.Total != 1 || one.Items[0].Operation.Status != "failed" || *one.Items[0].Operation.CompletedTargets != 0 {
		t.Fatalf("failure=%+v %v", one, err)
	}
	for _, invalid := range []protocol.DiagnosticsRequest{{Section: "operations", ParentID: strconv.Itoa(store.Records()[0].NumericID)}, {Section: "operations", State: "running"}, {Section: "operations", State: "done"}, {Section: "operations", CleanupMode: "manual"}, {Section: "operations", WorkspaceID: "target"}, {Section: "operations", Workspace: "relative"}, {Section: "operations", Limit: 51}} {
		if _, err := Read(store, invalid, false, integration.NewRegistry()); err == nil {
			t.Fatalf("invalid accepted: %+v", invalid)
		}
	}
	restarted, err := state.NewStore(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := Read(restarted, protocol.DiagnosticsRequest{Section: "operations"}, false, integration.NewRegistry())
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 || !empty.Complete {
		t.Fatalf("volatile evidence persisted: %+v %v", empty, err)
	}
}

func TestOperationScopeUsesRetainedEvidenceAndExplicitWorkspacePrecedence(t *testing.T) {
	store, _, path := fixture(t)
	store.SetTasks([]protocol.Task{task(path, "test:ABC-1")})
	id := store.BeginOperation("task-delete", store.Records()[0].NumericID, "")
	store.FinishOperation(id, protocol.ManagedOperation{Status: "succeeded"})
	store.SetTasks(nil) // Retained inactive history is still scope evidence.
	page, err := Read(store, protocol.DiagnosticsRequest{Section: "operations", Workspace: path}, false, integration.NewRegistry())
	if err != nil || page.Total != 1 {
		t.Fatalf("retained history excluded: %+v %v", page, err)
	}
	if page.Items[0].Operation.TotalTargets != nil {
		t.Fatal("unknown authoring counts became zero")
	}
	// A task-only operation may concern its entire bundle, but explicit workspace
	// reports are selected by their own ref and cannot borrow another task's scope.
	rows := operationItems(protocol.DiagnosticsRequest{Workspace: path}, []state.TaskRecord{{ID: "r", NumericID: 7}}, []state.SourceRefRecord{{TaskRecordID: "r", Snapshot: protocol.SourceRef{Path: path, WorkspaceID: "one"}}}, []protocol.ManagedOperation{{ID: "report", TaskID: 7, WorkspaceID: "other", Status: "reported", Kind: "workspace"}, {ID: "deleted", TaskID: 8, Status: "succeeded", Kind: "task-delete"}})
	if len(rows) != 0 {
		t.Fatal("unmatched/deleted target crossed scope")
	}
	rows = operationItems(protocol.DiagnosticsRequest{}, nil, nil, []protocol.ManagedOperation{{ID: "invalid", Kind: "cleanup", Status: "failed", CompletedTargets: 3, TotalTargets: 2}})
	if rows[0].Operation.CompletedTargets != nil || rows[0].Operation.TargetEvidence != "unknown_not_captured" {
		t.Fatal("invalid native counts became known")
	}
}

func TestOperationPagesBoundEscapedIdentitiesWithoutDroppingActiveEvidence(t *testing.T) {
	store, _, _ := fixture(t)
	for range 50 {
		store.BeginOperation("cleanup", 0, strings.Repeat("<", 256))
	}
	req := protocol.DiagnosticsRequest{Section: "operations", Limit: 50}
	seen := map[string]bool{}
	pages := 0
	for {
		page, err := Read(store, req, false, integration.NewRegistry())
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(page)
		if len(raw) > 32*1024 || page.Total != 50 {
			t.Fatal("escaped page exceeded budget or lost observations")
		}
		for _, row := range page.Items {
			if seen[row.ID] || row.Operation.CompletedTargets != nil {
				t.Fatal("duplicate or fabricated running counts")
			}
			seen[row.ID] = true
		}
		pages++
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 50 || pages < 2 {
		t.Fatal("output-bounded pagination incomplete")
	}

	for _, status := range []string{"succeeded", "failed", "partial"} {
		id := store.BeginOperation("cleanup", 0, "")
		store.FinishOperation(id, protocol.ManagedOperation{Status: status})
		page, err := Read(store, protocol.DiagnosticsRequest{Section: "operations", ID: id}, false, integration.NewRegistry())
		if err != nil || *page.Items[0].Operation.CompletedTargets != 0 || *page.Items[0].Operation.TotalTargets != 0 || page.Items[0].Operation.Status != status {
			t.Fatalf("zero native counts changed status: %+v %v", page, err)
		}
	}
}
