package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"radar/internal/integration"
	"radar/internal/integration/workspace"
	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

func TestGCPlanPagesEveryProductionCandidateAndPreservesUnknownEligibility(t *testing.T) {
	store, root, path := fixture(t)
	value := task(path, "test:ABC-123")
	for i := range 24 {
		value.SourceRefs = append(value.SourceRefs, protocol.SourceRef{ID: fmt.Sprintf("workspace:extra-%d", i), Source: "workspace", Path: filepath.Join(root, fmt.Sprintf("extra-%02d", i)), ProvidesWorkspace: true, WorkspaceID: fmt.Sprintf("extra-%d", i)})
	}
	store.SetTasks([]protocol.Task{value})
	if len(store.Tasks()) != 0 {
		t.Fatal("fixture must include UI-hidden completed work")
	}
	beforeRecords, beforeRefs, revision := store.InspectionSnapshot()
	production, err := workspacegc.BuildPlan(store, time.Now(), workspacegc.Options{WorkspaceRoot: root})
	if err != nil || len(production.Candidates) != 25 {
		t.Fatalf("production plan=%+v err=%v", production, err)
	}
	req := protocol.DiagnosticsRequest{Section: "gc_plan", ParentID: beforeRecords[0].ID, Limit: 3}
	seen := map[string]bool{}
	for {
		page, err := read(store, req, true)
		if err != nil || page.Total != len(production.Candidates) || len(page.Items) > 3 || !page.ResourceRefreshPending {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		for _, item := range page.Items {
			view := item.GCPlan
			if view == nil || seen[item.ID] || view.Decision != "candidate" || view.NextStage != "expired_cleanup" || view.ExpiryAt != "2026-01-09T10:20:30Z" || view.ExecutionEligibility != "unknown_provider_ownership_lease_checks_not_run" || view.Evidence != "production_gc_plan_on_cached_inputs" {
				t.Fatalf("wrong candidate evidence: %+v", item)
			}
			seen[item.ID] = true
			selected, err := read(store, protocol.DiagnosticsRequest{Section: "gc_plan", ID: item.ID, ParentID: strconv.Itoa(view.TaskID)}, false)
			if err != nil || selected.Total != 1 || selected.Items[0].ID != item.ID {
				t.Fatalf("exact candidate selector=%+v %v", selected, err)
			}
		}
		encoded, _ := json.Marshal(page)
		for _, forbidden := range []string{"private-", "task_title", "operation\"", "cleanup_planner_not_run"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("private payload or incorrect limitation: %s", encoded)
			}
		}
		if page.Complete {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 25 {
		t.Fatal("candidate inventory truncated at nested summary bound")
	}
	afterRecords, afterRefs, afterRevision := store.InspectionSnapshot()
	if revision != afterRevision || !reflect.DeepEqual(beforeRecords, afterRecords) || !reflect.DeepEqual(beforeRefs, afterRefs) {
		t.Fatal("planning changed retained state")
	}
	for _, bad := range []protocol.DiagnosticsRequest{{Section: "gc_plan", ParentID: "unrelated"}, {Section: "gc_plan", State: "in_progress"}, {Section: "gc_plan", Workspace: path + "-sibling"}} {
		page, err := read(store, bad, false)
		if err != nil || page.Total != 0 {
			t.Fatalf("selector escaped scope: %+v %v", page, err)
		}
	}
	page, err := read(store, protocol.DiagnosticsRequest{Section: "gc_plan", Workspace: path}, false)
	if err != nil || page.Total != 25 {
		t.Fatalf("owning-task scope must retain complete related plan: %+v %v", page, err)
	}
	if _, err := Read(store, protocol.DiagnosticsRequest{Section: "gc_plan"}, false, integration.NewRegistry()); err == nil {
		t.Fatal("unsupported production planner reported an empty complete inventory")
	}
}

func TestGCPlanSharesGatesLocationAndNextStageWithoutProbes(t *testing.T) {
	_, root, path := fixture(t)
	now := time.Date(2026, 1, 5, 10, 20, 30, 0, time.UTC)
	providers := integration.NewRegistry(workspace.Source{})
	record := state.TaskRecord{ID: "retained", NumericID: 7, State: "done", DoneAt: "2026-01-02T10:20:30Z", Reason: "private-reason"}
	ref := state.SourceRefRecord{ID: "ref", TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{ID: "ref", Source: "workspace", Path: path, WorkspaceID: "group", ProvidesWorkspace: true, Metadata: map[string]string{"token": "private-token"}}}
	bin := t.TempDir()
	called := filepath.Join(bin, "called")
	for _, name := range []string{"sbx", "git", "tmux"} {
		if err := os.WriteFile(filepath.Join(bin, name), fmt.Appendf(nil, "#!/bin/sh\n: > %q\nexit 1\n", called), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, test := range []struct {
		name, state, done, path, decision, stage string
		held, active, workspace                  bool
	}{
		{"retire", "done", record.DoneAt, path, "candidate", "runtime_retirement", false, true, true},
		{"expired", "done", "2025-12-01T10:20:30Z", path, "candidate", "expired_cleanup", false, true, true},
		{"standalone", "done", record.DoneAt, path, "candidate", "safe_cleanup", false, true, false},
		{"outside", "done", record.DoneAt, root + "-sibling/item", "outside_workspace_root", "", false, true, true},
		{"root", "done", record.DoneAt, root, "outside_workspace_root", "", false, true, true},
		{"held", "done", record.DoneAt, path, "agent_work_or_review_pending", "", true, true, true},
		{"notdone", "in_progress", record.DoneAt, path, "task_not_done", "", false, true, true},
		{"missingtime", "done", "", path, "completion_time_missing_or_invalid", "", false, true, true},
		{"retention", "done", now.Add(-time.Hour).Format(time.RFC3339), path, "retention_pending", "", false, true, true},
		{"inactive", "done", record.DoneAt, path, "no_workspace_candidates", "", false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, child := record, ref
			r.State, r.DoneAt = test.state, test.done
			child.Active, child.Snapshot.Path, child.Snapshot.AgentReview = test.active, test.path, test.held
			if !test.workspace {
				child.Snapshot.WorkspaceID = ""
			}
			items, err := gcPlanItems(protocol.DiagnosticsRequest{Section: "gc_plan"}, []state.TaskRecord{r}, []state.SourceRefRecord{child}, now, providers)
			if err != nil || len(items) != 1 || items[0].GCPlan.Decision != test.decision || items[0].GCPlan.NextStage != test.stage {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			if items[0].GCPlan.Path != "" && len(items[0].GCPlan.Path) > 520 {
				t.Fatal("unbounded path")
			}
		})
	}
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatal("cached plan ran a provider/runtime probe")
	}
}

func TestGCPlanFullIdentityDuplicateCountsAndTimeDrift(t *testing.T) {
	_, root, _ := fixture(t)
	now := time.Date(2026, 1, 5, 10, 20, 30, 0, time.UTC)
	record := state.TaskRecord{ID: "record", NumericID: 7, State: "done", DoneAt: "2026-01-02T10:20:30Z"}
	prefix := root + "/" + strings.Repeat("x", 600)
	refs := []state.SourceRefRecord{}
	for i, suffix := range []string{"-one", "-two", "-one"} {
		refs = append(refs, state.SourceRefRecord{ID: fmt.Sprint(i), TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{ID: fmt.Sprint(i), Path: prefix + suffix, ProvidesWorkspace: true}})
	}
	providers := integration.NewRegistry(workspace.Source{})
	items, err := gcPlanItems(protocol.DiagnosticsRequest{Section: "gc_plan"}, []state.TaskRecord{record}, refs, now, providers)
	if err != nil || len(items) != 2 || items[0].ID == items[1].ID || items[0].GCPlan.Path != items[1].GCPlan.Path || items[0].GCPlan.Occurrences != 2 || items[1].GCPlan.Occurrences != 1 {
		t.Fatalf("full identity/duplicates: %+v %v", items, err)
	}
	refs[0].Snapshot.WorkspaceID = "group"
	refs = refs[:1]
	first, err := gcPlanItems(protocol.DiagnosticsRequest{Section: "gc_plan"}, []state.TaskRecord{record}, refs, now, providers)
	if err != nil {
		t.Fatal(err)
	}
	later, err := gcPlanItems(protocol.DiagnosticsRequest{Section: "gc_plan"}, []state.TaskRecord{record}, refs, now.Add(7*24*time.Hour), providers)
	if err != nil || first[0].ID != later[0].ID || first[0].GCPlan.NextStage == later[0].GCPlan.NextStage {
		t.Fatal("stage change either lost evidence or changed semantic identity")
	}
	// Snapshot hashes include the complete projected decision, not just the DB revision.
	before, _ := paginate(protocol.DiagnosticsRequest{Section: "gc_plan", Limit: 1}, first, 1, false, "production_gc_plan_on_cached_inputs")
	after, _ := paginate(protocol.DiagnosticsRequest{Section: "gc_plan", Limit: 1}, later, 1, false, "production_gc_plan_on_cached_inputs")
	if before.Snapshot == after.Snapshot {
		t.Fatal("time-based plan change did not change snapshot")
	}
}
